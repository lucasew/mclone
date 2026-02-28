package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
	builtBy = "unknown"
)

var rootCmd = &cobra.Command{
	Use:     "mclone",
	Short:   "mclone is rclone for LLMs",
	Long:    `A unified LLM management and serving layer. Exposes multiple providers behind OpenAI and Anthropic-compatible APIs.`,
	Version: version,
}

func init() {
	rootCmd.SetVersionTemplate(fmt.Sprintf("mclone %s (commit: %s, built: %s, by: %s)\n", version, commit, date, builtBy))
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
