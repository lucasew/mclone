package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/lucasew/mclone/pkg/config"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/spf13/cobra"
	"github.com/tmc/langchaingo/llms"
)

type openAIChatRequest struct {
	Model    string              `json:"model"`
	Messages []openAIChatMessage `json:"messages"`
	Stream   bool                `json:"stream"`
}

type openAIChatMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

func (m *openAIChatMessage) ToLangChain() llms.MessageContent {
	role := llms.ChatMessageTypeHuman
	switch m.Role {
	case "system":
		role = llms.ChatMessageTypeSystem
	case "assistant":
		role = llms.ChatMessageTypeAI
	}

	content := ""
	switch v := m.Content.(type) {
	case string:
		content = v
	case []interface{}:
		var parts []string
		for _, p := range v {
			if pm, ok := p.(map[string]interface{}); ok {
				if t, ok := pm["type"].(string); ok && t == "text" {
					if txt, ok := pm["text"].(string); ok {
						parts = append(parts, txt)
					}
				}
			}
		}
		content = strings.Join(parts, "")
	}
	return llms.TextParts(role, content)
}

type openAIChatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

type openAIChatDeltaResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

type anthropicChatRequest struct {
	Model    string              `json:"model"`
	Messages []openAIChatMessage `json:"messages"`
	Stream   bool                `json:"stream"`
}

var serveCmd = &cobra.Command{
	Use:   "serve [remote]",
	Short: "Serve a remote via OpenAI or Anthropic compatible API",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		remoteName := args[0]
		if strings.HasSuffix(remoteName, ":") {
			remoteName = remoteName[:len(remoteName)-1]
		}

		port, _ := cmd.Flags().GetInt("port")
		overrideModel, _ := cmd.Flags().GetString("model")

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

		handleOpenAI := func(w http.ResponseWriter, r *http.Request) {
			var req openAIChatRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				slog.Warn("failed to decode openai request", "error", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			model := req.Model
			if overrideModel != "" {
				model = overrideModel
			}

			var msgs []llms.MessageContent
			for _, m := range req.Messages {
				msgs = append(msgs, m.ToLangChain())
			}

			respChan, err := p.Chat(r.Context(), model, msgs)
			if err != nil {
				slog.Error("openai chat failed", "error", err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			if req.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				for resp := range respChan {
					if resp.Error != nil {
						fmt.Fprintf(w, "data: {\"error\": %q}\n\n", resp.Error.Error())
						return
					}
					openaiResp := openAIChatDeltaResponse{ID: "mclone", Object: "chat.completion.chunk", Model: req.Model}
					openaiResp.Choices = []struct {
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
						FinishReason *string `json:"finish_reason"`
					}{{}}
					openaiResp.Choices[0].Delta.Content = resp.Content
					if resp.Done {
						stop := "stop"
						openaiResp.Choices[0].FinishReason = &stop
					}
					data, _ := json.Marshal(openaiResp)
					fmt.Fprintf(w, "data: %s\n\n", data)
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}
				}
				fmt.Fprintf(w, "data: [DONE]\n\n")
			} else {
				var content strings.Builder
				for resp := range respChan {
					if resp.Error != nil {
						http.Error(w, resp.Error.Error(), http.StatusInternalServerError)
						return
					}
					content.WriteString(resp.Content)
				}
				openaiResp := openAIChatResponse{ID: "mclone", Object: "chat.completion", Model: req.Model}
				openaiResp.Choices = []struct {
					Message struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"message"`
					FinishReason string `json:"finish_reason"`
				}{{}}
				openaiResp.Choices[0].Message.Role = "assistant"
				openaiResp.Choices[0].Message.Content = content.String()
				openaiResp.Choices[0].FinishReason = "stop"
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(openaiResp)
			}
		}

		handleAnthropic := func(w http.ResponseWriter, r *http.Request) {
			var req anthropicChatRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				slog.Warn("failed to decode anthropic request", "error", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			model := req.Model
			if overrideModel != "" {
				model = overrideModel
			}

			var msgs []llms.MessageContent
			for _, m := range req.Messages {
				msgs = append(msgs, m.ToLangChain())
			}

			respChan, err := p.Chat(r.Context(), model, msgs)
			if err != nil {
				slog.Error("anthropic chat failed", "error", err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			if req.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				// Send message_start
				fmt.Fprintf(w, "event: message_start\ndata: %s\n\n", fmt.Sprintf(`{"type":"message_start","message":{"id":"mclone","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`, model))

				fmt.Fprintf(w, "event: content_block_start\ndata: %s\n\n", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)

				for resp := range respChan {
					if resp.Error != nil {
						return
					}
					fmt.Fprintf(w, "event: content_block_delta\ndata: %s\n\n", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, resp.Content))
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}
				}
				fmt.Fprintf(w, "event: content_block_stop\ndata: %s\n\n", `{"type":"content_block_stop","index":0}`)
				fmt.Fprintf(w, "event: message_delta\ndata: %s\n\n", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":0}}`)
				fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			} else {
				var content strings.Builder
				for resp := range respChan {
					content.WriteString(resp.Content)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"id":"mclone","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":%q}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}`, model, content.String())
			}
		}

		mux := http.NewServeMux()
		mux.HandleFunc("/v1/messages", handleAnthropic)
		mux.HandleFunc("/v1/chat/completions", handleOpenAI)
		mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
			models, err := p.List(r.Context())
			if err != nil {
				slog.Error("failed to list models", "error", err)
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
			}{
				Object: "list",
				Data:   []modelEntry{},
			}
			for _, m := range models {
				resp.Data = append(resp.Data, modelEntry{ID: m.Name, Object: "model", Created: 1677610602, OwnedBy: "mclone"})
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		})

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			slog.Info("request", "method", r.Method, "path", r.URL.Path)
			mux.ServeHTTP(w, r)
		})

		slog.Info("starting server", "remote", remoteName, "port", port)
		if err := http.ListenAndServe(fmt.Sprintf(":%d", port), handler); err != nil {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	},
}

func init() {
	serveCmd.Flags().Int("port", 8080, "Port to listen on")
	serveCmd.Flags().String("model", "", "Force a specific model name (useful for Claude Code)")
	rootCmd.AddCommand(serveCmd)
}
