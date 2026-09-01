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
	Short: "List exported models, or models in a specific remote",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		resolve := remote.NewResolver(config.LoaderFrom(cmd.Context()))

		if len(args) == 0 {
			listModels(resolve.Exported)
			return
		}

		remoteName := strings.TrimSuffix(args[0], ":")
		listModels(func() (remote.Provider, error) {
			return resolve.Provider(remoteName)
		})
	},
}

func listModels(resolveProvider func() (remote.Provider, error)) {
	p, err := resolveProvider()
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
}

func init() {
	rootCmd.AddCommand(lsCmd)
}
