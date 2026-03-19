package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/lucasew/mclone/pkg/config"
	"github.com/lucasew/mclone/pkg/monitor"
	"github.com/lucasew/mclone/pkg/server"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve [remote]",
	Short: "Serve a remote via OpenAI or Anthropic compatible API",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		remoteName := strings.TrimSuffix(args[0], ":")
		port, _ := cmd.Flags().GetInt("port")
		overrideModel, _ := cmd.Flags().GetString("model")
		verbose, _ := cmd.Flags().GetBool("verbose")
		saveRawRequest, _ := cmd.Flags().GetString("save-raw-request")

		level := slog.LevelInfo
		if verbose {
			level = slog.LevelDebug
		}
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

		shutdownTracing := server.SetupTracing(verbose)
		defer func() {
			if err := shutdownTracing(cmd.Context()); err != nil {
				monitor.ReportError(cmd.Context(), err, "action", "shutdown_tracing")
			}
		}()

		loader := config.LoaderFrom(cmd.Context())
		conf, err := loader.Load()
		if err != nil {
			monitor.ReportError(cmd.Context(), err, "action", "load_config")
			return
		}

		resolve := remote.NewResolver(loader)
		provider, err := resolve.Provider(remoteName)
		if err != nil {
			monitor.ReportError(cmd.Context(), err, "action", "create_provider")
			return
		}

		cfg := server.Config{
			Provider:           provider,
			OverrideModel:      overrideModel,
			SaveRawRequestPath: saveRawRequest,
			Verbose:            verbose,
		}
		if rc, ok := conf.Remotes[remoteName]; ok {
			cfg.DefaultChatOptions = server.ParseGenerationDefaults(rc.Options)
		}

		slog.Info("starting server", "remote", remoteName, "port", port)
		srv := server.New(cfg)
		if err := srv.ListenAndServe(fmt.Sprintf(":%d", port)); err != nil {
			monitor.ReportError(cmd.Context(), err, "action", "server_listen")
		}
	},
}

func init() {
	serveCmd.Flags().Int("port", 8080, "Port to listen on")
	serveCmd.Flags().String("model", "", "Force a specific model name (useful for Claude Code)")
	serveCmd.Flags().BoolP("verbose", "v", false, "Enable debug logs")
	serveCmd.Flags().String("save-raw-request", "", "Path to save the raw incoming request body (overwrites)")
	rootCmd.AddCommand(serveCmd)
}
