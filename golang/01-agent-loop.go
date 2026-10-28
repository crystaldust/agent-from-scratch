package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

const promptHeader = "\033[36ms01 >> \033[0m"

// func agent_loop(messages: [map[string]]) {
//
// }

func main() {
	// history := []string
	// client := anthropic.NewClient()
	// print(client.Models)

	for {

		fmt.Print(promptHeader)
		reader := bufio.NewReader(os.Stdin)

		line, err := reader.ReadString('\n')
		if err != nil {
			fmt.Fprintln(os.Stderr, "read error", err)
			return
		}

		userInput := strings.TrimSpace(line)

		if userInput == "" {
			continue
		}

		if s := strings.ToLower(userInput); s == "exit" || s == "quit" {
			break
		}

		fmt.Println(userInput)
	}

}
