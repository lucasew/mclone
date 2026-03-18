package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"
	"strings"

	"github.com/goccy/go-json"
	"github.com/lucasew/mclone/pkg/config"
	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/monitor"
	"github.com/lucasew/mclone/pkg/protocol"
	"github.com/lucasew/mclone/pkg/protocol/anthropic"
	"github.com/lucasew/mclone/pkg/protocol/openai"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/spf13/cobra"
)

type chatRequest struct {
	Model        string                     `json:"model"`
	Messages     []protocol.IncomingMessage `json:"messages"`
	Tools        []protocol.Tool            `json:"tools,omitempty"`
	System       any                        `json:"system,omitempty"`
	Stream       bool                       `json:"stream"`
	OutputConfig *struct {
		Format struct {
			Type string `json:"type"`
		} `json:"format"`
	} `json:"output_config,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"top_p,omitempty"`
	MaxTokens     *int     `json:"max_tokens,omitempty"`
	Stop          any      `json:"stop,omitempty"`
	StopSequences []string `json:"stop_sequences,omitempty"`
}

var serveCmd = &cobra.Command{
	Use:   "serve [remote]",
	Short: "Serve a remote via OpenAI or Anthropic compatible API",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		remoteName := strings.TrimSuffix(args[0], ":")
		port, _ := cmd.Flags().GetInt("port")
		overrideModel, _ := cmd.Flags().GetString("model")
		verbose, _ := cmd.Flags().GetBool("verbose")

		level := slog.LevelInfo
		if verbose {
			level = slog.LevelDebug
		}
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

		loader := config.LoaderFrom(cmd.Context())
		conf, err := loader.Load()
		if err != nil {
			monitor.ReportError(cmd.Context(), err, "action", "load_config")
			return
		}

		resolve := remote.NewResolver(loader)
		p, err := resolve.Provider(remoteName)
		if err != nil {
			monitor.ReportError(cmd.Context(), err, "action", "create_provider")
			return
		}

		// Parse generation defaults from config options
		var defaultOpts message.ChatOptions
		if rc, ok := conf.Remotes[remoteName]; ok {
			defaultOpts = parseGenerationDefaults(rc.Options)
		}

		anthropicWriter := anthropic.NewWriter()
		openaiWriter := openai.NewWriter()

		mux := http.NewServeMux()
		mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
			serveChatRequest(w, r, p, overrideModel, anthropicWriter, cmd, defaultOpts)
		})
		mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
			serveChatRequest(w, r, p, overrideModel, openaiWriter, cmd, defaultOpts)
		})
		mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
			serveModels(w, r, p)
		})

		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			slog.Info("request", "method", r.Method, "path", r.URL.Path)
			mux.ServeHTTP(w, r)
		})

		slog.Info("starting server", "remote", remoteName, "port", port)
		if err := http.ListenAndServe(fmt.Sprintf(":%d", port), handler); err != nil {
			monitor.ReportError(cmd.Context(), err, "action", "server_listen")
		}
	},
}

func serveChatRequest(w http.ResponseWriter, r *http.Request, p remote.Provider, overrideModel string, writer protocol.Writer, cmd *cobra.Command, defaultOpts message.ChatOptions) {
	var req chatRequest
	var bodyReader io.Reader = r.Body

	if path, _ := cmd.Flags().GetString("save-raw-request"); path != "" {
		body, _ := io.ReadAll(r.Body)
		err := os.WriteFile(path, body, 0644)
		if err != nil {
			monitor.ReportError(r.Context(), err, "action", "save_raw_request", "path", path)
		} else {
			slog.Debug("saved raw request", "path", path)
		}
		bodyReader = bytes.NewReader(body)
	}

	if err := json.NewDecoder(bodyReader).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	responseModel := req.Model
	chatModel := req.Model
	if overrideModel != "" {
		chatModel = overrideModel
	}

	slog.Info("incoming request", "req_model", req.Model, "chat_model", chatModel)

	turns := parseMessages(r.Context(), req)

	opts := message.ChatOptions{}
	if len(req.Tools) > 0 {
		for _, t := range req.Tools {
			opts.Tools = append(opts.Tools, t.ToDefinition())
		}
		slog.Info("tools_configured", "count", len(opts.Tools))
	}
	if req.OutputConfig != nil && req.OutputConfig.Format.Type == "json_schema" {
		opts.JSONMode = true
	}

	opts.Temperature = req.Temperature
	opts.TopP = req.TopP
	opts.MaxTokens = req.MaxTokens
	opts.Stop = mergeStop(req.Stop, req.StopSequences)
	opts = opts.WithDefaults(defaultOpts)

	respChan, err := p.Chat(r.Context(), message.Request{
		Model:   chatModel,
		Turns:   turns,
		Options: opts,
	})
	if err != nil {
		monitor.ReportError(r.Context(), err, "action", "chat_failed")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writer.ServeResponse(w, respChan, responseModel, req.Stream)
}

func parseMessages(ctx context.Context, req chatRequest) []message.Turn {
	var msgs []message.Turn

	if req.System != nil {
		systemText := ""
		switch v := req.System.(type) {
		case string:
			systemText = v
		case []any:
			var parts []string
			for _, p := range v {
				if pm, ok := p.(map[string]any); ok {
					if txt, ok := pm["text"].(string); ok {
						parts = append(parts, txt)
					}
				}
			}
			systemText = strings.Join(parts, "\n")
		}
		if systemText != "" {
			msgs = append(msgs, message.TextTurn(message.RoleSystem, systemText))
		}
	}

	for i, m := range req.Messages {
		msg, err := m.ToTurn()
		if err != nil {
			monitor.ReportError(ctx, err, "action", "convert_message", "index", i)
			continue
		}
		msgs = append(msgs, msg)
	}

	return msgs
}

func serveModels(w http.ResponseWriter, r *http.Request, p remote.Provider) {
	models, err := p.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type modelEntry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}

	resp := struct {
		Object string       `json:"object"`
		Data   []modelEntry `json:"data"`
	}{Object: "list", Data: []modelEntry{}}

	for _, m := range models {
		resp.Data = append(resp.Data, modelEntry{
			ID: m.Slug, Object: "model", Created: 1677610602, OwnedBy: "mclone",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		monitor.ReportError(r.Context(), err, "action", "serve_models_encode_error")
	}
}

func parseGenerationDefaults(opts map[string]any) message.ChatOptions {
	var co message.ChatOptions

	if v, ok := opts["temperature"]; ok {
		switch val := v.(type) {
		case float64:
			co.Temperature = &val
		case string:
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				co.Temperature = &f
			}
		}
	}

	if v, ok := opts["top_p"]; ok {
		switch val := v.(type) {
		case float64:
			co.TopP = &val
		case string:
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				co.TopP = &f
			}
		}
	}

	if v, ok := opts["max_tokens"]; ok {
		switch val := v.(type) {
		case int64:
			n := int(val)
			co.MaxTokens = &n
		case float64:
			n := int(val)
			co.MaxTokens = &n
		case string:
			if n, err := strconv.Atoi(val); err == nil {
				co.MaxTokens = &n
			}
		}
	}

	if v, ok := opts["stop"]; ok {
		switch val := v.(type) {
		case string:
			co.Stop = strings.Split(val, ",")
		case []string:
			co.Stop = val
		case []any:
			for _, s := range val {
				if str, ok := s.(string); ok {
					co.Stop = append(co.Stop, str)
				}
			}
		}
	}
	return co
}

func mergeStop(stopField any, stopSequences []string) []string {
	var result []string
	switch v := stopField.(type) {
	case string:
		if v != "" {
			result = append(result, v)
		}
	case []any:
		for _, s := range v {
			if str, ok := s.(string); ok {
				result = append(result, str)
			}
		}
	}
	if len(result) == 0 {
		result = stopSequences
	}
	return result
}

func init() {
	serveCmd.Flags().Int("port", 8080, "Port to listen on")
	serveCmd.Flags().String("model", "", "Force a specific model name (useful for Claude Code)")
	serveCmd.Flags().BoolP("verbose", "v", false, "Enable debug logs")
	serveCmd.Flags().String("save-raw-request", "", "Path to save the raw incoming request body (overwrites)")
	rootCmd.AddCommand(serveCmd)
}
