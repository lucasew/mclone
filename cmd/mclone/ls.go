package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/lucasew/mclone/pkg/config"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/spf13/cobra"
)

var lsCmd = &cobra.Command{
	Use:   "ls [remote]",
	Short: "List models in a remote",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		remoteName := args[0]
		if strings.HasSuffix(remoteName, ":") {
			remoteName = remoteName[:len(remoteName)-1]
		}

		conf, err := config.LoadConfig()
		if err != nil {
			fmt.Printf("Error loading config: %v\n", err)
			return
		}

		rc, ok := conf.Remotes[remoteName]
		if !ok {
			fmt.Printf("Remote %q not found in config\n", remoteName)
			return
		}

		p, err := remote.NewProvider(rc.Type, remoteName, rc.Options)
		if err != nil {
			fmt.Printf("Error creating provider: %v\n", err)
			return
		}

		models, err := p.List(context.Background())
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		fmt.Printf("%-30s %10s\n", "NAME", "SIZE")
		for _, m := range models {
			fmt.Printf("%-30s %10s\n", m.Name, formatSize(m.Size))
		}
	},
}

func formatSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func init() {
	rootCmd.AddCommand(lsCmd)
}
