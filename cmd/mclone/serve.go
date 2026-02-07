package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

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
}

var serveCmd = &cobra.Command{
	Use:   "serve [remote]",
	Short: "Serve a remote via OpenAI or Anthropic compatible API",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		remoteName := strings.TrimSuffix(args[0], ":")
		port, _ := cmd.Flags().GetInt("port")
		overrideModel, _ := cmd.Flags().GetString("model")

		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))

		conf, err := config.LoadConfig()
		if err != nil {
			slog.Error("failed to load config", "error", err)
			return
		}

		rc, ok := conf.Remotes[remoteName]
		if !ok {
			slog.Error("remote not found", "remote", remoteName)
			return
		}

		p, err := remote.NewProvider(rc.Type, remoteName, rc.Options)
		if err != nil {
			slog.Error("failed to create provider", "error", err)
			return
		}

		anthropicWriter := anthropic.NewWriter()
		openaiWriter := openai.NewWriter()

		mux := http.NewServeMux()
		mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
			serveChatRequest(w, r, p, overrideModel, anthropicWriter)
		})
		mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
			serveChatRequest(w, r, p, overrideModel, openaiWriter)
		})
		mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
			serveModels(w, r, p)
		})

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

func serveChatRequest(w http.ResponseWriter, r *http.Request, p remote.Provider, overrideModel string, writer protocol.Writer) {
	body, _ := io.ReadAll(r.Body)

	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
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
	if len(req.Tools) > 0 {
		opts.Tools = make([]message.ToolDefinition, len(req.Tools))
		for i, t := range req.Tools {
			opts.Tools[i] = t.ToDefinition()
			slog.Debug("tool_received", "name", t.Name, "has_input_schema", t.InputSchema != nil, "has_parameters", t.Parameters != nil)
		}
		slog.Info("tools_configured", "count", len(opts.Tools))
	}
	if req.OutputConfig != nil && req.OutputConfig.Format.Type == "json_schema" {
		opts.JSONMode = true
	}

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
		ID       string `json:"id"`
		Object   string `json:"object"`
		Created  int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}

	resp := struct {
		Object string       `json:"object"`
		Data   []modelEntry `json:"data"`
	}{Object: "list", Data: []modelEntry{}}

	for _, m := range models {
		resp.Data = append(resp.Data, modelEntry{
			ID: m.Name, Object: "model", Created: 1677610602, OwnedBy: "mclone",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func init() {
	serveCmd.Flags().Int("port", 8080, "Port to listen on")
	serveCmd.Flags().String("model", "", "Force a specific model name (useful for Claude Code)")
	rootCmd.AddCommand(serveCmd)
}
