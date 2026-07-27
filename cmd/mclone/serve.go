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
	RunE: func(cmd *cobra.Command, args []string) error {
		remoteName := ""
		if len(args) > 0 {
			remoteName = strings.TrimSuffix(args[0], ":")
		}
		port, err := cmd.Flags().GetInt("port")
		if err != nil {
			return err
		}
		overrideModel, err := cmd.Flags().GetString("model")
		if err != nil {
			return err
		}
		verbose, err := cmd.Flags().GetBool("verbose")
		if err != nil {
			return err
		}
		saveRawRequest, err := cmd.Flags().GetString("save-raw-request")
		if err != nil {
			return err
		}

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
			return fmt.Errorf("load config: %w", err)
		}

		resolve := remote.NewResolver(loader)
		var provider remote.Provider
		if remoteName == "" {
			provider, err = resolve.Exported()
			if err != nil {
				return fmt.Errorf("create exported provider: %w", err)
			}
		} else {
			provider, err = resolve.Provider(remoteName)
			if err != nil {
				return fmt.Errorf("create provider: %w", err)
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

		slog.Info("starting server", "remote", remoteNameOrExported(remoteName), "port", port)
		srv := server.New(cfg)
		if err := srv.ListenAndServe(fmt.Sprintf(":%d", port)); err != nil {
			return fmt.Errorf("server listen: %w", err)
		}
		return nil
	},
}

func init() {
	serveCmd.Flags().Int("port", 8080, "Port to listen on")
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
