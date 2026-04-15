package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/lucasew/mclone/pkg/config"
	"github.com/lucasew/mclone/pkg/monitor"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/lucasew/mclone/pkg/server"
	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve [remote]",
	Short: "Serve a remote via OpenAI or Anthropic compatible API",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		remoteName := ""
		if len(args) > 0 {
			remoteName = strings.TrimSuffix(args[0], ":")
		}
		port, _ := cmd.Flags().GetInt("port")
		host, _ := cmd.Flags().GetString("host")
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
		var provider remote.Provider
		if remoteName == "" {
			provider, err = resolve.Exported()
			if err != nil {
				monitor.ReportError(cmd.Context(), err, "action", "create_exported_provider")
				return
			}
		} else {
			provider, err = resolve.Provider(remoteName)
			if err != nil {
				monitor.ReportError(cmd.Context(), err, "action", "create_provider")
				return
			}
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

		slog.Info("starting server", "remote", remoteNameOrExported(remoteName), "host", host, "port", port)
		srv := server.New(cfg)
		if err := srv.ListenAndServe(fmt.Sprintf("%s:%d", host, port)); err != nil {
			monitor.ReportError(cmd.Context(), err, "action", "server_listen")
		}
	},
}

func init() {
	serveCmd.Flags().Int("port", 8080, "Port to listen on")
	serveCmd.Flags().StringP("host", "H", "127.0.0.1", "Host to listen on")
	serveCmd.Flags().String("model", "", "Force a specific model name (useful for Claude Code)")
	serveCmd.Flags().BoolP("verbose", "v", false, "Enable debug logs")
	serveCmd.Flags().String("save-raw-request", "", "Path to save the raw incoming request body (overwrites)")
	rootCmd.AddCommand(serveCmd)
}

func remoteNameOrExported(name string) string {
	if name == "" {
		return "exported"
	}
	return name
}
