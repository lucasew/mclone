package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

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

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "SLUG\tNAME")
		for _, m := range models {
			fmt.Fprintf(w, "%s\t%s\n", m.Slug, m.Name)
		}
		w.Flush()
	},
}

func init() {
	rootCmd.AddCommand(lsCmd)
}
