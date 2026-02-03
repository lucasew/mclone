package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/lucasew/mclone/pkg/config"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/spf13/cobra"
)

var copyCmd = &cobra.Command{
	Use:   "copy [src_remote]:[model] [dest_remote]:[model]",
	Short: "Copy a model between remotes",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		srcParts := strings.SplitN(args[0], ":", 2)
		destParts := strings.SplitN(args[1], ":", 2)

		if len(srcParts) < 2 || len(destParts) < 2 {
			fmt.Println("Usage: mclone copy [src_remote]:[model] [dest_remote]:[model]")
			return
		}

		srcRemote, srcModel := srcParts[0], srcParts[1]
		destRemote, destModel := destParts[0], destParts[1]

		conf, err := config.LoadConfig()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		// Setup src
		sc, ok := conf.Remotes[srcRemote]
		if !ok {
			fmt.Printf("Source remote %q not found\n", srcRemote)
			return
		}
		sp, err := remote.NewProvider(sc.Type, srcRemote, sc.Options)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		// Setup dest
		dc, ok := conf.Remotes[destRemote]
		if !ok {
			fmt.Printf("Destination remote %q not found\n", destRemote)
			return
		}
		dp, err := remote.NewProvider(dc.Type, destRemote, dc.Options)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		fmt.Printf("Copying %s from %s to %s...\n", srcModel, srcRemote, destRemote)

		ctx := context.Background()
		reader, size, err := sp.Get(ctx, srcModel)
		if err != nil {
			fmt.Printf("Error getting source: %v\n", err)
			return
		}
		defer reader.Close()

		err = dp.Put(ctx, destModel, size, reader)
		if err != nil {
			fmt.Printf("Error putting destination: %v\n", err)
			return
		}

		fmt.Println("Copy successful!")
	},
}

func init() {
	rootCmd.AddCommand(copyCmd)
}
