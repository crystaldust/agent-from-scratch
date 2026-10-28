package main

import "fmt"
import "strings"
import "os"
import "bufio"


const promptHeader = "\033[36ms01 >> \033[0m"

// func agent_loop(messages: [map[string]]) {
//
// }

func main() {
	history := []string

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
