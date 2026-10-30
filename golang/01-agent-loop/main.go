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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

const MODEL = "deepseek-flash"
const promptPrefix = "\033[36ms01 >> \033[0m"
const SYSTEM_PROMPT_TEMPLATE = "You're a coding agent at %s. Use bash to solve tasks. Act, don't explain"

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

var bashToolParam = anthropic.ToolUnionParam{
	OfTool: &bashTool,
}

type BashInput struct {
	Command string `json:"command"`
}

var TOOLS = []anthropic.ToolUnionParam{bashToolParam}

var client = anthropic.NewClient()

const (
	bashTimeout    = 30 * time.Second
	maxOutputBytes = 30_000
	waitDelay      = 2 * time.Second
)

type limitedBuffer struct {
	mu        sync.Mutex
	buf       strings.Builder
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	// TODO Get to know more about the buffer writing
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
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

func safeEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"LANG=" + os.Getenv("LANG"),
	}

}
func runBash(ctx context.Context, workDir, command string) (string, error) {
	bashRunCtx, cancel := context.WithTimeout(ctx, bashTimeout)
	defer cancel()

	cmd := exec.CommandContext(bashRunCtx, "bash", "-c", command)
	cmd.Dir = workDir
	cmd.Env = safeEnv()

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	cmd.WaitDelay = waitDelay

	out := &limitedBuffer{limit: maxOutputBytes}
	cmd.Stdout = out
	cmd.Stderr = out

	err := cmd.Run()
	if ctx.Err() != nil {
		// User interrupt
		return "", ctx.Err()
	}
	if errors.Is(bashRunCtx.Err(), context.DeadlineExceeded) {
		return out.String(), fmt.Errorf("command timed out after %s", bashTimeout)
	}
	if err != nil {
		return out.String(), fmt.Errorf("command failed: %w", err)
	}

	return out.String(), nil
}

func handleToolUse(ctx context.Context, cwd string, use anthropic.ToolUseBlock) (anthropic.ContentBlockParamUnion, error) {
	var bashInput BashInput
	if err := json.Unmarshal(use.Input, &bashInput); err != nil {
		fmt.Printf("Failed to unmarshal ToolUse input: %v\n", err)
		return anthropic.NewToolResultBlock(use.ID, "invalid input: "+err.Error(), true), nil
	}
	fmt.Printf("we should execute command: %s", bashInput.Command)
	// Execute and append the result as user message to history
	// This is important, or the loop won't stop since the model won't return the
	// final answer, it will always ask us to execute the command
	output, err := runBash(ctx, cwd, bashInput.Command)
	if errors.Is(err, context.Canceled) {
		return anthropic.ContentBlockParamUnion{}, err
	}
	if err != nil {
		return anthropic.NewToolResultBlock(use.ID, output+"\n"+err.Error(), true), nil
	}
	return anthropic.NewToolResultBlock(use.ID, output, false), nil

}

func agentLoop(ctx context.Context, systemPrompts []anthropic.TextBlockParam, messages []anthropic.MessageParam, cwd string) ([]anthropic.MessageParam, error) {
	for {
		resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
			System:    systemPrompts,
			Messages:  messages,
			Model:     MODEL,
			MaxTokens: 1000,
			Tools:     TOOLS,
		})

		if errors.Is(err, context.Canceled) {
			fmt.Println("\n用户中断输入")
			return messages, fmt.Errorf("messages.new: %w", err)
		}
		if err != nil {
			// log.Fatal will terminate the whole process
			// log.Fatal(err)
			return messages, err
		}
		messages = append(messages, resp.ToParam())

		// Execute related tool if needed
		toolResults := []anthropic.ContentBlockParamUnion{}
		for _, block := range resp.Content {
			switch b := block.AsAny().(type) {
			case anthropic.TextBlock:
				fmt.Println(b.Text)
			case anthropic.ToolUseBlock:
				result, err := handleToolUse(ctx, cwd, b)
				if err != nil {
					return messages, err
				}
				toolResults = append(toolResults, result)
			}
		}
		if len(toolResults) == 0 {
			return messages, nil // Come to the final answer without tool use
		}
		// Make sure we have a tool use result, append it to messages
		messages = append(messages, anthropic.NewUserMessage(toolResults...))
	}

}

func buildSystemPromptStr(cwd string) string {

	return fmt.Sprintf(SYSTEM_PROMPT_TEMPLATE, cwd)
}

func main() {
	// history := []string
	// client := anthropic.NewClient()
	// print(client.Models)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Failed to build system prompt")
	}

	systemPromptStr := buildSystemPromptStr(cwd)
	systemPrompts := []anthropic.TextBlockParam{{Text: systemPromptStr}}

	history := []anthropic.MessageParam{}

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print(promptPrefix)
		line, err := reader.ReadString('\n')
		if err != nil {
			fmt.Fprintln(os.Stderr, "read error", err)
			return
		}

		userInput := strings.TrimSpace(line)

		if userInput == "" {
			// TODO What if we want to skip the empty input and hang there waiting?(Just like nothing happend)
			continue
		}

		if s := strings.ToLower(userInput); s == "exit" || s == "quit" {
			break
		}

		userInputMsgParam := anthropic.NewUserMessage(anthropic.NewTextBlock(userInput))
		history = append(history, userInputMsgParam)

		history, err = agentLoop(ctx, systemPrompts, history, cwd)
		if errors.Is(err, context.Canceled) {
			fmt.Println("\n用户中断")
			continue
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "本轮失败:", err)
			continue
		}

	}

}
