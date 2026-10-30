//go:build ignore

// 参考实现：和 main.go 同目录，用 ignore 标签排除在包外，避免两个 main() 冲突。
// 运行：cd golang/01-agent-loop && go run 01-agentloop.byclaude.go
// 需要先在环境变量里设置 ANTHROPIC_API_KEY（以及可选的 ANTHROPIC_BASE_URL）。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

const (
	model                = "deepseek-flash" // 走网关时用网关提供的模型名；直连 Anthropic 用 "claude-opus-5-5"
	maxTokens            = 8000
	promptPrefix         = "\033[36ms01 >> \033[0m"
	systemPromptTemplate = "You're a coding agent at %s. Use bash to solve tasks. Act, don't explain."
)

// ANSI 转义序列：ESC [ 参数 命令字母，由终端解释执行，不会显示出来
const (
	cursorUp  = "\033[1A" // 光标上移一行
	clearLine = "\033[2K" // 清除光标所在的整行（光标位置不变）
)

const (
	bashTimeout    = 30 * time.Second
	maxOutputBytes = 30_000
	waitDelay      = 2 * time.Second
)

var bashTool = anthropic.ToolParam{
	Name:        "bash",
	Description: anthropic.String("Run a shell command in the current working directory and return its combined stdout/stderr"),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command to run",
			},
		},
		Required: []string{"command"},
	},
}

var tools = []anthropic.ToolUnionParam{{OfTool: &bashTool}}

type BashInput struct {
	Command string `json:"command"`
}

// ---------- 执行 bash ----------

// limitedBuffer 是有字节上限的 io.Writer：超出部分丢弃，读取时附上截断提示。
// stdout 和 stderr 可能被同时写入，所以要加锁。
type limitedBuffer struct {
	mu        sync.Mutex
	buf       strings.Builder
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil // 必须返回 len(p)，否则命令会因为写失败而出错
	}
	if len(p) > remaining {
		b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.truncated {
		return b.buf.String() + fmt.Sprintf("\n...[output truncated at %d bytes]", b.limit)
	}
	return b.buf.String()
}

// safeEnv 只把必要的环境变量传给子进程，避免 API key 泄漏到命令输出里。
func safeEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"LANG=" + os.Getenv("LANG"),
	}
}

// runBash 执行一条命令。父 ctx 被取消（用户中断）时返回 ctx.Err()；
// 超时或非 0 退出码时返回输出和错误，由调用方交给模型。
func runBash(ctx context.Context, workDir, command string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, bashTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "bash", "-c", command)
	cmd.Dir = workDir
	cmd.Env = safeEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // 自成进程组
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // 杀掉整个进程组，包括后台子进程
	}
	cmd.WaitDelay = waitDelay

	out := &limitedBuffer{limit: maxOutputBytes}
	cmd.Stdout = out
	cmd.Stderr = out

	err := cmd.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
		return out.String(), fmt.Errorf("command timed out after %s", bashTimeout)
	}
	if err != nil {
		return out.String(), fmt.Errorf("command failed: %w", err)
	}
	return out.String(), nil
}

// ---------- agent loop ----------

// handleToolUse 执行一个 tool_use，返回对应的 tool_result 块。
// 只有用户中断才返回 error；其他失败都作为 is_error 的结果交给模型。
func handleToolUse(ctx context.Context, cwd string, use anthropic.ToolUseBlock) (anthropic.ContentBlockParamUnion, error) {
	if use.Name != bashTool.Name {
		return anthropic.NewToolResultBlock(use.ID, "unknown tool: "+use.Name, true), nil
	}

	var in BashInput
	if err := json.Unmarshal(use.Input, &in); err != nil {
		return anthropic.NewToolResultBlock(use.ID, "invalid input: "+err.Error(), true), nil
	}

	fmt.Printf("\033[33m$ %s\033[0m\n", in.Command)
	output, err := runBash(ctx, cwd, in.Command)
	if errors.Is(err, context.Canceled) {
		return anthropic.ContentBlockParamUnion{}, err
	}
	if err != nil {
		return anthropic.NewToolResultBlock(use.ID, output+"\n"+err.Error(), true), nil
	}
	return anthropic.NewToolResultBlock(use.ID, output, false), nil
}

// runTools 依次执行响应里的所有 tool_use，返回 tool_result 块（顺序与 tool_use 一致），
// 同时打印文本块。没有 tool_use 时返回空切片。
func runTools(ctx context.Context, cwd string, content []anthropic.ContentBlockUnion) ([]anthropic.ContentBlockParamUnion, error) {
	results := []anthropic.ContentBlockParamUnion{}
	for _, block := range content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			fmt.Println(b.Text)
		case anthropic.ToolUseBlock:
			result, err := handleToolUse(ctx, cwd, b)
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		}
	}
	return results, nil
}

// agentLoop 反复调用模型、执行工具，直到模型不再请求工具。
// 返回追加了本轮所有 assistant / tool_result 消息的新切片。
func agentLoop(ctx context.Context, client anthropic.Client, system []anthropic.TextBlockParam, messages []anthropic.MessageParam, cwd string) ([]anthropic.MessageParam, error) {
	for {
		resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
			Model:     model,
			MaxTokens: maxTokens,
			System:    system,
			Messages:  messages,
			Tools:     tools,
		})
		if err != nil {
			return nil, fmt.Errorf("messages.new: %w", err)
		}
		messages = append(messages, resp.ToParam()) // 先追加 assistant 消息（含 tool_use）

		if resp.StopReason == anthropic.StopReasonMaxTokens {
			fmt.Fprintln(os.Stderr, "[warning] response hit max_tokens and may be truncated")
		}

		results, err := runTools(ctx, cwd, resp.Content)
		if err != nil {
			return nil, err
		}
		if len(results) == 0 {
			return messages, nil // 没有工具调用：模型给出了最终回答
		}
		messages = append(messages, anthropic.NewUserMessage(results...)) // 再追加 tool_result
	}
}

// runTurn 处理一次用户输入。只在这一轮执行期间接管 Ctrl+C：
// 执行中按 Ctrl+C 只中断本轮；在提示符下按 Ctrl+C 仍然直接退出程序。
func runTurn(client anthropic.Client, system []anthropic.TextBlockParam, history []anthropic.MessageParam, cwd, input string) ([]anthropic.MessageParam, error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Clip 让 append 一定分配新数组，turn 和 history 互不影响
	turn := append(slices.Clip(history), anthropic.NewUserMessage(anthropic.NewTextBlock(input)))
	return agentLoop(ctx, client, system, turn, cwd)
}

// ---------- main ----------

func buildSystemPrompt(cwd string) []anthropic.TextBlockParam {
	return []anthropic.TextBlockParam{{Text: fmt.Sprintf(systemPromptTemplate, cwd)}}
}

// isTerminal 判断 f 是否连接到终端（字符设备），而不是管道或普通文件。
// 注意 /dev/null 也是字符设备；要严格判断可以用 golang.org/x/term 的 IsTerminal。
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func isExitCommand(input string) bool {
	s := strings.ToLower(input)
	return s == "exit" || s == "quit"
}

func main() {
	fmt.Println("written by claude code")
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("get working directory: %v", err)
	}

	client := anthropic.NewClient()
	system := buildSystemPrompt(cwd)
	history := []anthropic.MessageParam{}
	reader := bufio.NewReader(os.Stdin)
	// 只有输入和输出都是终端时才输出转义序列；管道或重定向到文件时它们会变成乱码
	interactive := isTerminal(os.Stdin) && isTerminal(os.Stdout)

	for {
		fmt.Print(promptPrefix)
		line, err := reader.ReadString('\n')
		if err != nil { // Ctrl+D 等 EOF
			fmt.Println()
			return
		}

		input := strings.TrimSpace(line)
		if input == "" {
			// 按回车时，换行是终端驱动回显的，程序拦不住，只能事后擦掉：
			// 光标回到提示符那一行、移到行首、清空整行，再由循环开头重新打印提示符
			if interactive {
				fmt.Print(cursorUp + "\r" + clearLine)
			}
			continue
		}
		if isExitCommand(input) {
			return
		}

		next, err := runTurn(client, system, history, cwd, input)
		if errors.Is(err, context.Canceled) {
			fmt.Println("\n[interrupted] 本轮已中断，history 未改变")
			continue
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "[error]", err)
			continue
		}
		history = next // 只有成功的一轮才写入 history
	}
}
