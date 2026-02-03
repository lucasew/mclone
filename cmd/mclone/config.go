package main

import (
	"fmt"

	"github.com/lucasew/mclone/pkg/config"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage remotes",
}

var configAddCmd = &cobra.Command{
	Use:   "add [name] [type]",
	Short: "Add a new remote",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		typeName := args[1]

		conf, err := config.LoadConfig()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		options, _ := cmd.Flags().GetStringToString("opt")

		rc := config.RemoteConfig{
			Type:    typeName,
			Options: options,
		}

		conf.Remotes[name] = rc
		if err := conf.Save(); err != nil {
			fmt.Printf("Error saving config: %v\n", err)
			return
		}
		fmt.Printf("Remote %q added successfully\n", name)
	},
}

func init() {
	configAddCmd.Flags().StringToStringP("opt", "o", nil, "Provider options (key=value)")
	configCmd.AddCommand(configAddCmd)
	rootCmd.AddCommand(configCmd)
}
