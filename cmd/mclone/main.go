package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "mclone",
	Short: "mclone is rclone for LLMs",
	Long:  `A command line tool to manage and sync LLMs across different providers like Ollama, Hugging Face, and Local storage.`,
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
