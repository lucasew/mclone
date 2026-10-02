package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/lucasew/mclone/pkg/config"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/spf13/cobra"
)

// readInputLine reads a trimmed line from interactive stdin.
// EOF with partial content is accepted; other read errors are logged.
func readInputLine(reader *bufio.Reader) string {
	s, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		slog.Error("read_input_line", "error", err)
	}
	return strings.TrimSpace(s)
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage remotes (interactive by default)",
	Run: func(cmd *cobra.Command, args []string) {
		interactiveConfig(cmd.Context())
	},
}

func toAnyMap(m map[string]string) map[string]any {
	out := make(map[string]any)
	for k, v := range m {
		out[k] = v
	}
	return out
}

type baseURLPreset struct {
	Label string
	URL   string
}

func promptBaseURL(reader *bufio.Reader, provider string, presets []baseURLPreset, defaultURL string) string {
	fmt.Printf("Select %s base URL:\n", provider)
	for i, preset := range presets {
		fmt.Printf("%d) %s", i+1, preset.Label)
		if preset.URL != "" {
			fmt.Printf(" (%s)", preset.URL)
		}
		fmt.Println()
	}
	fmt.Printf("%d) Custom URL\n", len(presets)+1)
	fmt.Print("Choice (number or URL, blank for default): ")

	choice := readInputLine(reader)
	if choice == "" {
		return defaultURL
	}

	var idx int
	if _, err := fmt.Sscanf(choice, "%d", &idx); err == nil {
		if idx > 0 && idx <= len(presets) {
			return presets[idx-1].URL
		}
		if idx == len(presets)+1 {
			fmt.Print("Enter custom base URL: ")
			return readInputLine(reader)
		}
	}

	if strings.HasPrefix(choice, "http://") || strings.HasPrefix(choice, "https://") {
		return choice
	}

	return defaultURL
}

func interactiveConfig(ctx context.Context) {
	loader := config.LoaderFrom(ctx)
	reader := bufio.NewReader(os.Stdin)
	for {
		conf, err := loader.Load()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		fmt.Println("\nCurrent remotes:")
		names := make([]string, 0, len(conf.Remotes))
		for name := range conf.Remotes {
			names = append(names, name)
		}
		slices.Sort(names)

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "NAME\tTYPE")
		for _, name := range names {
			r := conf.Remotes[name]
			fmt.Fprintf(w, "%s\t%s\n", name, r.Type)
		}
		w.Flush()

		fmt.Println("\ne) Edit existing remote")
		fmt.Println("n) New remote")
		fmt.Println("d) Delete remote")
		fmt.Println("q) Quit config")
		fmt.Print("Select: ")

		choice := readInputLine(reader)

		if choice == "d" {
			fmt.Print("Enter name of remote to delete: ")
			name := readInputLine(reader)
			if _, ok := conf.Remotes[name]; ok {
				delete(conf.Remotes, name)
				if err := loader.Save(conf); err != nil {
					fmt.Printf("Error saving config: %v\n", err)
				}
				fmt.Printf("Remote %q deleted.\n", name)
			} else {
				fmt.Println("Remote not found")
			}
			continue
		} else if choice == "q" {
			return
		}

		if choice == "n" {
			fmt.Print("Enter name for new remote: ")
			name := readInputLine(reader)
			if name == "" {
				continue
			}

			types := remote.ListTypes()
			slices.Sort(types)
			fmt.Println("Available provider types:")

			typeWriter := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintln(typeWriter, "#\tTYPE")
			for i, t := range types {
				fmt.Fprintf(typeWriter, "%d\t%s\n", i+1, t)
			}
			typeWriter.Flush()

			fmt.Print("Select type (number or name): ")
			typeChoice := readInputLine(reader)

			var selectedType string
			for _, t := range types {
				if typeChoice == t {
					selectedType = t
					break
				}
			}
			if selectedType == "" {
				var idx int
				if _, err := fmt.Sscanf(typeChoice, "%d", &idx); err == nil {
					if idx > 0 && idx <= len(types) {
						selectedType = types[idx-1]
					}
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
				options["path"] = readInputLine(reader)
			case "ollama":
				fmt.Print("Enter base URL (default http://localhost:11434): ")
				url := readInputLine(reader)
				if url == "" {
					url = "http://localhost:11434"
				}
				options["base_url"] = url
			case "openai":
				url := promptBaseURL(reader, "OpenAI", []baseURLPreset{
					{Label: "OpenAI", URL: "https://api.openai.com/v1"},
					{Label: "OpenRouter", URL: "https://openrouter.ai/api/v1"},
					{Label: "LM Studio", URL: "http://localhost:1234/v1"},
					{Label: "Ollama OpenAI API", URL: "http://localhost:11434/v1"},
				}, "https://api.openai.com/v1")
				options["base_url"] = url
				fmt.Print("Enter API Key: ")
				options["api_key"] = readInputLine(reader)
			case "anthropic":
				url := promptBaseURL(reader, "Anthropic", []baseURLPreset{
					{Label: "Anthropic", URL: "https://api.anthropic.com"},
					{Label: "Anthropic-compatible local/proxy", URL: "http://localhost:8080"},
				}, "https://api.anthropic.com")
				options["base_url"] = url
				fmt.Print("Enter API Key: ")
				options["api_key"] = readInputLine(reader)
			case "gemini":
				fmt.Print("Enter API Key: ")
				options["api_key"] = readInputLine(reader)
			case "huggingface":
				fmt.Print("Enter namespace (user or org): ")
				options["namespace"] = readInputLine(reader)
			case "antigravity":
				fmt.Println("No specific options required (uses hardcoded client ID).")
			case "ddg":
				fmt.Println("No options required for DuckDuckGo search.")
			case "search":
				fmt.Println("The Search provider wraps another provider (e.g., gemini) and uses a search backend (e.g., ddg).")

				fmt.Print("Enter name of base provider (e.g. gemini): ")
				options["provider"] = readInputLine(reader)

				fmt.Print("Enter name of search backend (default: ddg): ")
				searcher := readInputLine(reader)
				if searcher == "" {
					searcher = "ddg"
				}
				options["search"] = searcher
			case "webfetch":
				fmt.Println("The WebFetch provider wraps another provider to allow reading web pages.")
				fmt.Print("Enter name of base provider (e.g. search_wrapper): ")
				options["provider"] = readInputLine(reader)
			}

			conf.Remotes[name] = config.RemoteConfig{
				Type:    selectedType,
				Options: toAnyMap(options),
			}
			if err := loader.Save(conf); err != nil {
				fmt.Printf("Error saving config: %v\n", err)
			}
			fmt.Printf("Remote %q added.\n", name)

			if selectedType == "antigravity" {
				fmt.Print("Authenticate now? [Y/n]: ")
				authChoice := strings.ToLower(readInputLine(reader))
				if authChoice == "" || authChoice == "y" || authChoice == "yes" {
					fmt.Println("Starting authentication flow...")

					// Create a resolver linked to the config so UpdateOptions works
					resolve := remote.NewResolver(loader)

					// Use resolve.Provider instead of NewProvider to ensure dependencies are injected
					prov, err := resolve.Provider(name)
					if err != nil {
						fmt.Printf("Failed to create provider: %v\n", err)
					} else {
						// Trigger List to force auth
						_, err := prov.List(context.Background())
						if err == nil {
							fmt.Println("Authentication successful! Token saved.")
						} else {
							fmt.Printf("Authentication failed: %v\n", err)
						}
					}
				}
			}
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

		loader := config.LoaderFrom(cmd.Context())
		conf, err := loader.Load()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		options, err := cmd.Flags().GetStringToString("opt")
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		rc := config.RemoteConfig{
			Type:    typeName,
			Options: toAnyMap(options),
		}

		conf.Remotes[name] = rc
		if err := loader.Save(conf); err != nil {
			fmt.Printf("Error saving config: %v\n", err)
			return
		}
		if typeName == "antigravity" {
			fmt.Println("Starting authentication flow...")
			resolve := remote.NewResolver(loader)
			prov, err := resolve.Provider(name)
			if err != nil {
				fmt.Printf("Failed to create provider: %v\n", err)
				return
			}
			if _, err := prov.List(context.Background()); err != nil {
				fmt.Printf("Authentication failed: %v\n", err)
				return
			}
			fmt.Println("Authentication successful! Token saved.")
		}
		fmt.Printf("Remote %q added successfully\n", name)
	},
}

func init() {
	configAddCmd.Flags().StringToStringP("opt", "o", nil, "Provider options (key=value)")
	configCmd.AddCommand(configAddCmd)
	rootCmd.AddCommand(configCmd)
}
