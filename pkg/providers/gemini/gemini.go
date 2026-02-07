package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
	"google.golang.org/genai"
)

var thoughtRegex = regexp.MustCompile(`(?s)<thought>(.*?)</thought>`)

type GeminiProvider struct {
	APIKey string
}

func (p *GeminiProvider) Name() string { return "gemini" }

func (p *GeminiProvider) List(ctx context.Context) ([]remote.Model, error) {
	client, err := p.client()
	if err != nil {
		return nil, err
	}

	var models []remote.Model
	for m, err := range client.Models.All(ctx) {
		if err != nil {
			return nil, err
		}
		models = append(models, remote.Model{Name: m.DisplayName, Slug: m.Name})
	}
	return models, nil
}

func (p *GeminiProvider) Chat(ctx context.Context, modelName string, messages []message.Message, options message.ChatOptions) (<-chan message.ChatResponse, error) {
	client, err := p.client()
	if err != nil {
		return nil, err
	}

	contents, systemInstruction := toGeminiContents(messages)
	config := &genai.GenerateContentConfig{}
	if systemInstruction != nil {
		config.SystemInstruction = systemInstruction
	}
	if len(options.Tools) > 0 {
		config.Tools = toGeminiTools(options.Tools)
	}
	if options.JSONMode {
		config.ResponseMIMEType = "application/json"
	}

	out := make(chan message.ChatResponse)
	go func() {
		defer close(out)

		slog.Debug("gemini_generate_start", "model", modelName, "msgs_len", len(messages))

		// Map to accumulate tool calls by their index/ID in the candidate parts
		toolCallsMap := make(map[string]*message.ToolCall)
		var toolCallOrder []string

		for resp, err := range client.Models.GenerateContentStream(ctx, modelName, contents, config) {
			if err != nil {
				slog.Error("gemini_stream_error", "error", err)
				out <- message.ChatResponse{Error: err}
				return
			}

			for _, cand := range resp.Candidates {
				if cand.Content == nil {
					continue
				}
				for i, part := range cand.Content.Parts {
					if part.Text != "" {
						if part.Thought {
							slog.Debug("gemini_thought_out", "len", len(part.Text))
							out <- message.ChatResponse{Content: "<thought>" + part.Text + "</thought>"}
						} else {
							out <- message.ChatResponse{Content: part.Text}
						}
					}
					if part.FunctionCall != nil {
						// Create a stable key for this part index in this candidate
						key := fmt.Sprintf("part_%d", i)

						args, _ := json.Marshal(part.FunctionCall.Args)
						sig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)

						id := part.FunctionCall.ID
						if id == "" {
							// If model doesn't provide ID, we check if we already assigned one for this part
							if existing, ok := toolCallsMap[key]; ok {
								id = existing.ID
							} else {
								id = fmt.Sprintf("%s-%d", part.FunctionCall.Name, time.Now().UnixNano())
							}
						}

						if _, ok := toolCallsMap[key]; !ok {
							toolCallOrder = append(toolCallOrder, key)
						}

						toolCallsMap[key] = &message.ToolCall{
							ID:               id,
							Name:             part.FunctionCall.Name,
							Arguments:        string(args),
							ThoughtSignature: sig,
						}
						slog.Debug("gemini_tool_call_buffered", "key", key, "name", part.FunctionCall.Name, "id", id)
					}
				}
			}
		}

		if len(toolCallOrder) > 0 {
			var finalCalls []message.ToolCall
			for _, key := range toolCallOrder {
				finalCalls = append(finalCalls, *toolCallsMap[key])
			}
			slog.Info("gemini_sending_buffered_tool_calls", "count", len(finalCalls))
			out <- message.ChatResponse{
				ToolCalls: finalCalls,
			}
		}

		slog.Debug("gemini_generate_done")
		out <- message.ChatResponse{Done: true}
	}()

	return out, nil
}

func (p *GeminiProvider) client() (*genai.Client, error) {
	return genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:  p.APIKey,
		Backend: genai.BackendGeminiAPI,
	})
}

func toGeminiContents(messages []message.Message) ([]*genai.Content, *genai.Content) {
	var contents []*genai.Content
	var system *genai.Content

	toolNames := make(map[string]string)
	for _, m := range messages {
		for _, p := range m.Parts {
			if tc, ok := p.(message.ToolCallPart); ok {
				toolNames[tc.ID] = tc.Name
			}
		}
	}

	for i, m := range messages {
		role := "user"
		if m.Role == message.RoleAssistant {
			role = "model"
		}

		c := &genai.Content{Role: role}

		for _, p := range m.Parts {
			switch v := p.(type) {
			case message.TextPart:
				if v.Text == "" {
					continue
				}
				text := v.Text
				lastIdx := 0
				matches := thoughtRegex.FindAllStringSubmatchIndex(text, -1)
				for _, match := range matches {
					if match[0] > lastIdx {
						preText := text[lastIdx:match[0]]
						if strings.TrimSpace(preText) != "" {
							c.Parts = append(c.Parts, genai.NewPartFromText(preText))
						}
					}
					thoughtContent := text[match[2]:match[3]]
					slog.Debug("gemini_thought_in", "msg_idx", i, "len", len(thoughtContent))
					c.Parts = append(c.Parts, &genai.Part{
						Text:    thoughtContent,
						Thought: true,
					})
					lastIdx = match[1]
				}
				if lastIdx < len(text) {
					postText := text[lastIdx:]
					if strings.TrimSpace(postText) != "" {
						c.Parts = append(c.Parts, genai.NewPartFromText(postText))
					}
				}

			case message.ToolCallPart:
				var args map[string]any
				json.Unmarshal([]byte(v.Arguments), &args)
				slog.Debug("gemini_tool_call_in", "msg_idx", i, "name", v.Name, "id", v.ID, "has_sig", v.ThoughtSignature != "")
				c.Parts = append(c.Parts, &genai.Part{
					FunctionCall: &genai.FunctionCall{
						ID:   v.ID,
						Name: v.Name,
						Args: args,
					},
					ThoughtSignature: func() []byte {
						b, _ := base64.StdEncoding.DecodeString(v.ThoughtSignature)
						return b
					}(),
				})

			case message.ToolResultPart:
				funcName := toolNames[v.ToolCallID]
				if funcName == "" {
					funcName = v.ToolCallID
				}
				slog.Debug("gemini_tool_result_in", "msg_idx", i, "id", v.ToolCallID, "name", funcName)
				c.Parts = append(c.Parts, &genai.Part{
					FunctionResponse: &genai.FunctionResponse{
						ID:   v.ToolCallID,
						Name: funcName,
						Response: map[string]any{
							"result": v.Content,
						},
					},
				})
			}
		}

		if m.Role == message.RoleSystem {
			system = c
			system.Role = "system"
		} else if len(c.Parts) > 0 {
			contents = append(contents, c)
		}
	}

	return contents, system
}

func toGeminiTools(tools []message.ToolDefinition) []*genai.Tool {
	decls := make([]*genai.FunctionDeclaration, len(tools))
	for i, t := range tools {
		decls[i] = &genai.FunctionDeclaration{
			Name:                 t.Name,
			Description:          t.Description,
			ParametersJsonSchema: t.Parameters,
		}
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}
}

func init() {
	remote.Register("gemini", func(name string, options map[string]string, _ remote.Resolver) (remote.Provider, error) {
		return &GeminiProvider{APIKey: options["api_key"]}, nil
	})
}
