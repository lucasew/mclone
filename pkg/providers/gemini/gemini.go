package gemini

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
	"google.golang.org/genai"
)

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
		for resp, err := range client.Models.GenerateContentStream(ctx, modelName, contents, config) {
			if err != nil {
				slog.Error("gemini_stream_error", "error", err)
				out <- message.ChatResponse{Error: err}
				return
			}

			if text := resp.Text(); text != "" {
				out <- message.ChatResponse{Content: text}
			}

			for _, fc := range resp.FunctionCalls() {
				args, _ := json.Marshal(fc.Args)
				slog.Info("gemini_tool_call", "name", fc.Name, "id", fc.ID)
				out <- message.ChatResponse{
					ToolCalls: []message.ToolCall{{
						ID:        fc.ID,
						Name:      fc.Name,
						Arguments: string(args),
					}},
				}
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

	// Build tool call ID → function name lookup
	toolNames := make(map[string]string)
	for _, m := range messages {
		for _, p := range m.Parts {
			if tc, ok := p.(message.ToolCallPart); ok {
				toolNames[tc.ID] = tc.Name
			}
		}
	}

	for _, m := range messages {
		switch m.Role {
		case message.RoleSystem:
			var parts []*genai.Part
			for _, p := range m.Parts {
				if tp, ok := p.(message.TextPart); ok && tp.Text != "" {
					parts = append(parts, genai.NewPartFromText(tp.Text))
				}
			}
			if len(parts) > 0 {
				system = &genai.Content{Parts: parts, Role: "user"}
			}

		case message.RoleUser:
			c := &genai.Content{Role: "user"}
			for _, p := range m.Parts {
				switch v := p.(type) {
				case message.TextPart:
					if v.Text != "" {
						c.Parts = append(c.Parts, genai.NewPartFromText(v.Text))
					}
				case message.ToolResultPart:
					funcName := toolNames[v.ToolCallID]
					if funcName == "" {
						funcName = v.ToolCallID
					}
					c.Parts = append(c.Parts, genai.NewPartFromFunctionResponse(funcName, map[string]any{
						"result": v.Content,
					}))
				}
			}
			if len(c.Parts) > 0 {
				contents = append(contents, c)
			}

		case message.RoleAssistant:
			c := &genai.Content{Role: "model"}
			for _, p := range m.Parts {
				switch v := p.(type) {
				case message.TextPart:
					if v.Text != "" {
						c.Parts = append(c.Parts, genai.NewPartFromText(v.Text))
					}
				case message.ToolCallPart:
					var args map[string]any
					json.Unmarshal([]byte(v.Arguments), &args)
					c.Parts = append(c.Parts, &genai.Part{
						FunctionCall: &genai.FunctionCall{
							ID:   v.ID,
							Name: v.Name,
							Args: args,
						},
					})
				}
			}
			if len(c.Parts) > 0 {
				contents = append(contents, c)
			}

		case message.RoleTool:
			c := &genai.Content{Role: "user"}
			for _, p := range m.Parts {
				if v, ok := p.(message.ToolResultPart); ok {
					funcName := toolNames[v.ToolCallID]
					if funcName == "" {
						funcName = v.ToolCallID
					}
					c.Parts = append(c.Parts, genai.NewPartFromFunctionResponse(funcName, map[string]any{
						"result": v.Content,
					}))
				}
			}
			if len(c.Parts) > 0 {
				contents = append(contents, c)
			}
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
