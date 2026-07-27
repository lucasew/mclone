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
	Use:           "ls [remote]",
	Short:         "List exported models, or models in a specific remote",
	Args:          cobra.MaximumNArgs(1),
	SilenceErrors: true, // main prints the returned error once
	SilenceUsage:  true, // operational failures are not flag misuse
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		resolve := remote.NewResolver(config.LoaderFrom(ctx))

		if len(args) == 0 {
			return listModels(ctx, resolve.Exported)
		}

		remoteName := strings.TrimSuffix(args[0], ":")
		return listModels(ctx, func(ctx context.Context) (remote.Provider, error) {
			return resolve.Provider(remoteName)
		})
	},
}

func listModels(ctx context.Context, resolveProvider func(context.Context) (remote.Provider, error)) error {
	p, err := resolveProvider(ctx)
	if err != nil {
		return fmt.Errorf("creating provider: %w", err)
	}

	models, err := p.List(ctx)
	if err != nil {
		return fmt.Errorf("listing models: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "SLUG\tNAME")
	for _, m := range models {
		fmt.Fprintf(w, "%s\t%s\n", m.Slug, m.Name)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("writing model list: %w", err)
	}
	return nil
}

func init() {
	rootCmd.AddCommand(lsCmd)
}
