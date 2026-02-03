package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lucasew/mclone/pkg/config"
	"github.com/lucasew/mclone/pkg/protocol"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/spf13/cobra"
	"github.com/tmc/langchaingo/llms"
)

type chatRequest struct {
	Model        string                     `json:"model"`
	Messages     []protocol.IncomingMessage `json:"messages"`
	Tools        []protocol.Tool            `json:"tools,omitempty"`
	System       interface{}                `json:"system,omitempty"`
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
		remoteName := args[0]
		if strings.HasSuffix(remoteName, ":") {
			remoteName = remoteName[:len(remoteName)-1]
		}

		port, _ := cmd.Flags().GetInt("port")
		overrideModel, _ := cmd.Flags().GetString("model")

		opts_slog := &slog.HandlerOptions{Level: slog.LevelDebug}
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, opts_slog)))

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

		handleRequest := func(w http.ResponseWriter, r *http.Request, isAnthropic bool) {
			body, _ := io.ReadAll(r.Body)
			slog.Debug("raw_request", "body", string(body))

			var req chatRequest
			if err := json.Unmarshal(body, &req); err != nil {
				slog.Warn("failed to decode request", "error", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			responseModel := req.Model
			chatModel := req.Model
			if overrideModel != "" {
				chatModel = overrideModel
			}

			slog.Info("incoming request",
				"protocol", func() string {
					if isAnthropic {
						return "anthropic"
					}
					return "openai"
				}(),
				"req_model", req.Model,
				"chat_model", chatModel,
			)

			var msgs []llms.MessageContent
			if req.System != nil {
				systemText := ""
				switch v := req.System.(type) {
				case string:
					systemText = v
				case []interface{}:
					var parts []string
					for _, p := range v {
						if pm, ok := p.(map[string]interface{}); ok {
							if txt, ok := pm["text"].(string); ok {
								parts = append(parts, txt)
							}
						}
					}
					systemText = strings.Join(parts, "\n")
				}
				if systemText != "" {
					msgs = append(msgs, llms.TextParts(llms.ChatMessageTypeSystem, systemText))
				}
			}

			for i, m := range req.Messages {
				lcMsg, err := m.ToLangChain()
				if err != nil {
					slog.Error("failed to convert message", "index", i, "error", err)
					continue
				}
				msgs = append(msgs, lcMsg)
			}

			var opts []llms.CallOption
			if len(req.Tools) > 0 {
				lcTools := make([]llms.Tool, len(req.Tools))
				for i, t := range req.Tools {
					lcTools[i] = t.ToLangChain()
					slog.Debug("tool_defined", "name", t.Name)
				}
				opts = append(opts, llms.WithTools(lcTools))
			}
			if req.OutputConfig != nil && req.OutputConfig.Format.Type == "json_schema" {
				opts = append(opts, llms.WithJSONMode())
			}

			// Log total prompt size
			promptLen := 0
			for _, m := range msgs {
				for _, p := range m.Parts {
					if tc, ok := p.(llms.TextContent); ok {
						promptLen += len(tc.Text)
					}
				}
			}
			slog.Info("sending_to_provider", "model", chatModel, "total_prompt_chars", promptLen)

			respChan, err := p.Chat(r.Context(), chatModel, msgs, opts...)
			if err != nil {
				slog.Error("chat failed", "error", err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			if req.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				if isAnthropic {
					serveAnthropicStream(w, respChan, responseModel)
				} else {
					serveOpenAIStream(w, respChan, responseModel)
				}
			} else {
				w.Header().Set("Content-Type", "application/json")
				if isAnthropic {
					serveAnthropicJSON(w, respChan, responseModel)
				} else {
					serveOpenAIJSON(w, respChan, responseModel)
				}
			}
		}

		mux := http.NewServeMux()
		mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) { handleRequest(w, r, true) })
		mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) { handleRequest(w, r, false) })

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
		http.ListenAndServe(fmt.Sprintf(":%d", port), handler)
	},
}

func serveAnthropicStream(w http.ResponseWriter, respChan <-chan remote.ChatResponse, model string) {
	fmt.Fprintf(w, "event: message_start\ndata: %s\n\n", fmt.Sprintf(`{"type":"message_start","message":{"id":"mclone","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`, model))

	contentIndex := 0
	fmt.Fprintf(w, "event: content_block_start\ndata: %s\n\n", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, contentIndex))

	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	hasCalledTool := false

	for resp := range respChan {
		if resp.Content != "" {
			fmt.Fprintf(w, "event: content_block_delta\ndata: %s\n\n", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":%q}}`, contentIndex, resp.Content))
			slog.Debug("sent_delta", "len", len(resp.Content))
		}

		for _, tc := range resp.ToolCalls {
			hasCalledTool = true
			fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", contentIndex)
			contentIndex++

			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("toolu_%d", time.Now().UnixNano())
			}

			var input interface{}
			if err := json.Unmarshal([]byte(tc.FunctionCall.Arguments), &input); err != nil {
				input = map[string]interface{}{}
			}
			if input == nil {
				input = map[string]interface{}{}
			}
			inputJSON, _ := json.Marshal(input)

			slog.Info("sending_tool_use", "name", tc.FunctionCall.Name, "id", id)
			fmt.Fprintf(w, "event: content_block_start\ndata: %s\n\n", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":%q,"name":%q,"input":%s}}`, contentIndex, id, tc.FunctionCall.Name, string(inputJSON)))
			fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", contentIndex)
			contentIndex++

			fmt.Fprintf(w, "event: content_block_start\ndata: %s\n\n", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, contentIndex))
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}

	fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", contentIndex)

	stopReason := "end_turn"
	if hasCalledTool {
		stopReason = "tool_use"
	}

	fmt.Fprintf(w, "event: message_delta\ndata: %s\n\n", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"output_tokens":0}}`, stopReason))
	fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
}

func serveAnthropicJSON(w http.ResponseWriter, respChan <-chan remote.ChatResponse, model string) {
	var content strings.Builder
	var toolCalls []llms.ToolCall
	for resp := range respChan {
		content.WriteString(resp.Content)
		toolCalls = append(toolCalls, resp.ToolCalls...)
	}

	type anthropicContent struct {
		Type  string      `json:"type"`
		Text  string      `json:"text,omitempty"`
		ID    string      `json:"id,omitempty"`
		Name  string      `json:"name,omitempty"`
		Input interface{} `json:"input,omitempty"`
	}

	resp := struct {
		ID         string             `json:"id"`
		Role       string             `json:"role"`
		Model      string             `json:"model"`
		Content    []anthropicContent `json:"content"`
		StopReason string             `json:"stop_reason"`
	}{
		ID: "mclone", Role: "assistant", Model: model, Content: []anthropicContent{}, StopReason: "end_turn",
	}

	if content.Len() > 0 {
		resp.Content = append(resp.Content, anthropicContent{Type: "text", Text: content.String()})
	}
	if len(toolCalls) > 0 {
		resp.StopReason = "tool_use"
		for _, tc := range toolCalls {
			var input interface{}
			json.Unmarshal([]byte(tc.FunctionCall.Arguments), &input)
			if input == nil {
				input = map[string]interface{}{}
			}
			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("toolu_%d", time.Now().UnixNano())
			}
			resp.Content = append(resp.Content, anthropicContent{
				Type: "tool_use", ID: id, Name: tc.FunctionCall.Name, Input: input,
			})
		}
	}
	json.NewEncoder(w).Encode(resp)
}

func serveOpenAIStream(w http.ResponseWriter, respChan <-chan remote.ChatResponse, model string) {
	for resp := range respChan {
		openaiResp := struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}{
			ID: "mclone", Object: "chat.completion.chunk", Model: model,
		}
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
}

func serveOpenAIJSON(w http.ResponseWriter, respChan <-chan remote.ChatResponse, model string) {
	var content strings.Builder
	var toolCalls []llms.ToolCall
	for resp := range respChan {
		content.WriteString(resp.Content)
		toolCalls = append(toolCalls, resp.ToolCalls...)
	}

	type openAIChoice struct {
		Message struct {
			Role      string          `json:"role"`
			Content   string          `json:"content"`
			ToolCalls []llms.ToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	}

	resp := struct {
		ID      string         `json:"id"`
		Object  string         `json:"object"`
		Model   string         `json:"model"`
		Choices []openAIChoice `json:"choices"`
	}{
		ID: "mclone", Object: "chat.completion", Model: model,
	}

	choice := openAIChoice{}
	choice.Message.Role = "assistant"
	choice.Message.Content = content.String()
	choice.Message.ToolCalls = toolCalls
	choice.FinishReason = "stop"
	if len(toolCalls) > 0 {
		choice.FinishReason = "tool_calls"
	}
	resp.Choices = []openAIChoice{choice}
	json.NewEncoder(w).Encode(resp)
}

func init() {
	serveCmd.Flags().Int("port", 8080, "Port to listen on")
	serveCmd.Flags().String("model", "", "Force a specific model name (useful for Claude Code)")
	rootCmd.AddCommand(serveCmd)
}
