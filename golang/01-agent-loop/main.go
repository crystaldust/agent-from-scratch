package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

const MODEL = "deepseek-flash"
const promptPrefix = "\033[36ms01 >> \033[0m"
const SYSTEM_PROMPT_TEMPLATE = "You're a coding agent at %s. Use bash to solve tasks. Act, don't explain"

var bashTool = anthropic.ToolParam{
	Name:        "bash",
	Description: anthropic.String("Run a shell commmand in the current working directory and return its combined stdout/stderr"),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"command": map[string]any{
				"type":         "string",
				"descriptioin": "The shell command to run",
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
var history = []anthropic.MessageParam{}

func agentLoop(ctx context.Context, systemPrompts []anthropic.TextBlockParam, messages []anthropic.MessageParam) error {
	for {
		resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
			System:    systemPrompts,
			Messages:  messages,
			Model:     MODEL,
			MaxTokens: 1000,
			Tools:     TOOLS,
		})

		history = append(history, resp.ToParam())

		if errors.Is(err, context.Canceled) {
			fmt.Println("\n用户中断输入")
			return fmt.Errorf("messages.new: %w", err)
		}
		if err != nil {
			log.Fatal(err)
			return err
		}

		// Execute related tool if needed
		toolUseExists := false
		for _, content := range resp.Content {
			toolUse, yes := content.AsAny().(anthropic.ToolUseBlock)
			if yes {
				fmt.Println(content.Type)
				toolUseExists = true
				var bashInput BashInput
				if err := json.Unmarshal(toolUse.Input, &bashInput); err != nil {
					fmt.Printf("Failed to unmarshal ToolUse input: %w\n", err)
				} else {
					fmt.Printf("we should execute command: %s", bashInput.Command)
					// Execute and append the result as user message to history
					// This is important, or the loop won't stop since the model won't return the
					// final answer, it will always ask us to execute the command
				}
			}

			// if content.Type == "tool_use" {
			// 	fmt.Println("Handle the tool use")
			// }
		}
		if !toolUseExists {
			return nil
		}
	}

}

func buildSystemPromptStr() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("Failed to get working dir: %w", err)
	}
	return fmt.Sprintf(SYSTEM_PROMPT_TEMPLATE, cwd), nil
}

func main() {
	// history := []string
	// client := anthropic.NewClient()
	// print(client.Models)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	systemPromptStr, err := buildSystemPromptStr()
	if err != nil {
		log.Fatalf("Failed to build system prompt")
	}

	systemPrompts := []anthropic.TextBlockParam{{Text: systemPromptStr}}
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

		agentLoop(ctx, systemPrompts, history)

	}

}
