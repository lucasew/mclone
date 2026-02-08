package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/lucasew/mclone/pkg/config"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage remotes (interactive by default)",
	Run: func(cmd *cobra.Command, args []string) {
		interactiveConfig()
	},
}

func interactiveConfig() {
	reader := bufio.NewReader(os.Stdin)
	for {
		conf, err := config.LoadConfig()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		fmt.Println("\nCurrent remotes:")
		names := make([]string, 0, len(conf.Remotes))
		for name := range conf.Remotes {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			r := conf.Remotes[name]
			fmt.Printf("- %s (%s)\n", name, r.Type)
		}

		fmt.Println("\ne) Edit existing remote")
		fmt.Println("n) New remote")
		fmt.Println("d) Delete remote")
		fmt.Println("q) Quit config")
		fmt.Print("Select: ")

		choice, _ := reader.ReadString('\n')
		choice = strings.TrimSpace(choice)

		switch choice {
		case "n":
			fmt.Print("Enter name for new remote: ")
			name, _ := reader.ReadString('\n')
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}

			types := remote.ListTypes()
			sort.Strings(types)
			fmt.Println("Available provider types:")
			for i, t := range types {
				fmt.Printf("%d) %s\n", i+1, t)
			}
			fmt.Print("Select type (number or name): ")
			typeChoice, _ := reader.ReadString('\n')
			typeChoice = strings.TrimSpace(typeChoice)

			var selectedType string
			for _, t := range types {
				if typeChoice == t {
					selectedType = t
					break
				}
			}
			if selectedType == "" {
				var idx int
				fmt.Sscanf(typeChoice, "%d", &idx)
				if idx > 0 && idx <= len(types) {
					selectedType = types[idx-1]
				}
			}

			if selectedType == "" {
				fmt.Println("Invalid type")
				continue
			}

			options := make(map[string]string)
			switch selectedType {
			case "local":
				fmt.Print("Enter path: ")
				path, _ := reader.ReadString('\n')
				options["path"] = strings.TrimSpace(path)
			case "ollama":
				fmt.Print("Enter base URL (default http://localhost:11434): ")
				url, _ := reader.ReadString('\n')
				url = strings.TrimSpace(url)
				if url == "" {
					url = "http://localhost:11434"
				}
				options["base_url"] = url
			case "openai":
				fmt.Print("Enter base URL (default https://api.openai.com/v1): ")
				url, _ := reader.ReadString('\n')
				url = strings.TrimSpace(url)
				options["base_url"] = url
				fmt.Print("Enter API Key: ")
				key, _ := reader.ReadString('\n')
				options["api_key"] = strings.TrimSpace(key)
			case "anthropic":
				fmt.Print("Enter API Key: ")
				key, _ := reader.ReadString('\n')
				options["api_key"] = strings.TrimSpace(key)
			case "gemini":
				fmt.Print("Enter API Key: ")
				key, _ := reader.ReadString('\n')
				options["api_key"] = strings.TrimSpace(key)
			case "huggingface":
				fmt.Print("Enter namespace (user or org): ")
				ns, _ := reader.ReadString('\n')
				options["namespace"] = strings.TrimSpace(ns)
			case "geminioauth":
				fmt.Println("No specific options required (uses hardcoded client ID).")
			case "ddg":
				fmt.Println("No options required for DuckDuckGo search.")
			case "search":
				fmt.Println("The Search provider wraps another provider (e.g., gemini) and uses a search backend (e.g., ddg).")

				fmt.Print("Enter name of base provider (e.g. gemini): ")
				prov, _ := reader.ReadString('\n')
				options["provider"] = strings.TrimSpace(prov)

				fmt.Print("Enter name of search backend (default: ddg): ")
				searcher, _ := reader.ReadString('\n')
				searcher = strings.TrimSpace(searcher)
				if searcher == "" {
					searcher = "ddg"
				}
				options["search"] = searcher
			case "webfetch":
				fmt.Println("The WebFetch provider wraps another provider to allow reading web pages.")
				fmt.Print("Enter name of base provider (e.g. search_wrapper): ")
				prov, _ := reader.ReadString('\n')
				options["provider"] = strings.TrimSpace(prov)
			}

			conf.Remotes[name] = config.RemoteConfig{
				Type:    selectedType,
				Options: options,
			}
			conf.Save()
			fmt.Printf("Remote %q added.\n", name)

			if selectedType == "geminioauth" {
				fmt.Print("Authenticate now? [Y/n]: ")
				authChoice, _ := reader.ReadString('\n')
				authChoice = strings.TrimSpace(strings.ToLower(authChoice))
				if authChoice == "" || authChoice == "y" || authChoice == "yes" {
					fmt.Println("Starting authentication flow...")
					// Import "context" needed
					// But we are in main package, need to check imports.
					// We need to use remote.NewProvider to instantiate and trigger flow.
					prov, err := remote.NewProvider(selectedType, name, options)
					if err != nil {
						fmt.Printf("Failed to create provider: %v\n", err)
					} else {
						// Trigger List to force auth
						// We need context.
						// Assuming context package is imported or we use context.Background() fully qualified if not?
						// config.go imports: bufio, fmt, os, sort, strings, config, remote, cobra.
						// context is NOT imported.
						// I'll add context to imports in next Edit.
						_, err := prov.List(context.Background())
						if err == nil {
							fmt.Println("Authentication successful! Token saved.")
						} else {
							fmt.Printf("Authentication failed: %v\n", err)
						}
					}
				}
			}

		case "d":
			fmt.Print("Enter name of remote to delete: ")
			name, _ := reader.ReadString('\n')
			name = strings.TrimSpace(name)
			if _, ok := conf.Remotes[name]; ok {
				delete(conf.Remotes, name)
				conf.Save()
				fmt.Printf("Remote %q deleted.\n", name)
			} else {
				fmt.Println("Remote not found")
			}
		case "q":
			return
		}
	}
}

var configAddCmd = &cobra.Command{
	Use:   "add [name] [type]",
	Short: "Add a new remote (non-interactive)",
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
