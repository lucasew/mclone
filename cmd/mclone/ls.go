package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/lucasew/mclone/pkg/config"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/spf13/cobra"
)

var lsCmd = &cobra.Command{
	Use:   "ls [remote]",
	Short: "List models in a remote",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		remoteName := strings.TrimSuffix(args[0], ":")

		conf, err := config.LoadConfig()
		if err != nil {
			fmt.Printf("Error loading config: %v\n", err)
			return
		}

		resolve := remote.NewResolver(conf)
		p, err := resolve.Provider(remoteName)
		if err != nil {
			fmt.Printf("Error creating provider: %v\n", err)
			return
		}

		models, err := p.List(context.Background())
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		t := table.NewWriter()
		t.SetOutputMirror(os.Stdout)
		t.AppendHeader(table.Row{"Slug", "Name"})
		for _, m := range models {
			t.AppendRow(table.Row{m.Slug, m.Name})
		}
		t.Render()
	},
}

func init() {
	rootCmd.AddCommand(lsCmd)
}
