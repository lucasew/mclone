package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/lucasew/mclone/pkg/config"
	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/spf13/cobra"
)

var chatCmd = &cobra.Command{
	Use:   "chat [remote]:[model]",
	Short: "Start a chat session with a model",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		parts := strings.Split(args[0], ":")
		if len(parts) < 2 {
			fmt.Println("Usage: mclone chat [remote]:[model]")
			return
		}
		remoteName := parts[0]
		modelName := strings.Join(parts[1:], ":")

		conf, err := config.LoadConfig()
		if err != nil {
			fmt.Printf("Error loading config: %v\n", err)
			return
		}

		resolve := remote.NewResolver(conf)
		p, err := resolve(remoteName)
		if err != nil {
			fmt.Printf("Error creating provider: %v\n", err)
			return
		}

		ctx := context.Background()
		var messages []message.Message

		scanner := bufio.NewScanner(os.Stdin)
		fmt.Printf("Chatting with %s on %s. Type 'exit' to quit.\n", modelName, remoteName)

		for {
			fmt.Print("> ")
			if !scanner.Scan() {
				break
			}
			input := scanner.Text()
			if input == "exit" || input == "quit" {
				break
			}

			messages = append(messages, message.TextParts(message.RoleUser, input))

			respChan, err := p.Chat(ctx, modelName, messages, message.ChatOptions{})
			if err != nil {
				fmt.Printf("Error: %v\n", err)
				continue
			}

			var fullResponse strings.Builder
			for resp := range respChan {
				if resp.Error != nil {
					fmt.Printf("\nError: %v\n", resp.Error)
					break
				}
				fmt.Print(resp.Content)
				fullResponse.WriteString(resp.Content)
			}
			fmt.Println()

			messages = append(messages, message.TextParts(message.RoleAssistant, fullResponse.String()))
		}
	},
}

func init() {
	rootCmd.AddCommand(chatCmd)
}
