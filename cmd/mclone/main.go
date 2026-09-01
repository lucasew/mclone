package main

import (
	"fmt"
	"os"

	"github.com/lucasew/mclone/pkg/config"
	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
	builtBy = "unknown"

	configLoader config.ConfigLoader
)

var rootCmd = &cobra.Command{
	Use:     "mclone",
	Short:   "mclone is rclone for LLMs",
	Long:    `A unified LLM management and serving layer. Exposes multiple providers behind OpenAI and Anthropic-compatible APIs.`,
	Version: version,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		ctx := config.WithLoader(cmd.Context(), &configLoader)
		cmd.SetContext(ctx)
	},
}

func init() {
	rootCmd.SetVersionTemplate(fmt.Sprintf("mclone %s (commit: %s, built: %s, by: %s)\n", version, commit, date, builtBy))
	rootCmd.PersistentFlags().StringVar(&configLoader.Location, "config", "", "Path to config file (default ~/.config/mclone/mclone.conf)")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
