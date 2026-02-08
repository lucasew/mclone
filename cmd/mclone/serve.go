package main

import (
	"bytes"
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

		conf, err := config.LoadConfig()
		if err != nil {
			slog.Error("failed to load config", "error", err)
			return
		}

		resolve := remote.NewResolver(conf)
		p, err := resolve.Provider(remoteName)
		if err != nil {
			slog.Error("failed to create provider", "error", err)
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
			slog.Error("server failed", "error", err)
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
			slog.Error("failed to save raw request", "path", path, "error", err)
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

	msgs := parseMessages(req)

	opts := message.ChatOptions{}
	hasSearchTool := false
	if len(req.Tools) > 0 {
		for _, t := range req.Tools {
			def := t.ToDefinition()
			if normalized, ok := normalizeSearchTool(def); ok {
				if !hasSearchTool {
					opts.Tools = append(opts.Tools, normalized)
					hasSearchTool = true
					slog.Debug("tool_normalized", "from", def.Name, "type", def.Type)
				}
				continue
			}
			opts.Tools = append(opts.Tools, def)
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

	respChan, err := p.Chat(r.Context(), chatModel, msgs, opts)
	if err != nil {
		slog.Error("chat failed", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writer.ServeResponse(w, respChan, responseModel, req.Stream)
}

func parseMessages(req chatRequest) []message.Message {
	var msgs []message.Message

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
			msgs = append(msgs, message.TextParts(message.RoleSystem, systemText))
		}
	}

	for i, m := range req.Messages {
		msg, err := m.ToMessage()
		if err != nil {
			slog.Error("failed to convert message", "index", i, "error", err)
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
	json.NewEncoder(w).Encode(resp)
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

var webSearchSchema = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Search query"}},"required":["query"]}`)

func normalizeSearchTool(def message.ToolDefinition) (message.ToolDefinition, bool) {
	switch {
	case def.Name == "WebSearch",
		def.Name == "WebFetch",
		def.Name == "web_search" && def.Type != "function",
		def.Type != "" && def.Type != "function":
		return message.ToolDefinition{
			Type:        "function",
			Name:        "web_search",
			Description: "Search the web for current information. Returns relevant results with titles, URLs, and snippets.",
			Parameters:  webSearchSchema,
		}, true
	}
	return def, false
}

func init() {
	serveCmd.Flags().Int("port", 8080, "Port to listen on")
	serveCmd.Flags().String("model", "", "Force a specific model name (useful for Claude Code)")
	serveCmd.Flags().BoolP("verbose", "v", false, "Enable debug logs")
	serveCmd.Flags().String("save-raw-request", "", "Path to save the raw incoming request body (overwrites)")
	rootCmd.AddCommand(serveCmd)
}
