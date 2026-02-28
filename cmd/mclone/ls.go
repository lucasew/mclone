package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/lucasew/mclone/pkg/config"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/spf13/cobra"
)

var lsCmd = &cobra.Command{
	Use:   "ls [remote]",
	Short: "List remotes, or models in a remote",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) == 0 {
			listRemotes(cmd)
			return
		}

		remoteName := strings.TrimSuffix(args[0], ":")

		resolve := remote.NewResolver(config.LoaderFrom(cmd.Context()))
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

func listRemotes(cmd *cobra.Command) {
	loader := config.LoaderFrom(cmd.Context())
	cfg, err := loader.Load()
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		return
	}

	if len(cfg.Remotes) == 0 {
		fmt.Println("No remotes configured.")
		return
	}

	names := make([]string, 0, len(cfg.Remotes))
	for name := range cfg.Remotes {
		names = append(names, name)
	}
	sort.Strings(names)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tTYPE")
	for _, name := range names {
		fmt.Fprintf(w, "%s\t%s\n", name, cfg.Remotes[name].Type)
	}
	w.Flush()
}

func init() {
	rootCmd.AddCommand(lsCmd)
}
