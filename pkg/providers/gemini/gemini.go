package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/xeipuuv/gojsonschema"
	"google.golang.org/genai"
)

// signatureCache stores ThoughtSignatures indexed by tool call ID.
var signatureCache sync.Map

// toolDefinitionCache stores ToolDefinitions indexed by name for validation.
var toolDefinitionCache sync.Map

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

	// Update tool cache for validation
	for _, t := range options.Tools {
		toolDefinitionCache.Store(t.Name, t)
	}

	contents, systemInstruction := toGeminiContents(messages)
	config := &genai.GenerateContentConfig{
		SystemInstruction: systemInstruction,
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

		toolCallsBuffer := make(map[int]*message.ToolCall)
		var toolCallOrder []int

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
							out <- message.ChatResponse{Thought: part.Text}
						} else {
							out <- message.ChatResponse{Content: part.Text}
						}
					}
					if part.FunctionCall != nil {
						tc, ok := toolCallsBuffer[i]
						if !ok {
							id := part.FunctionCall.ID
							if id == "" {
								id = fmt.Sprintf("call_%d_%d", time.Now().UnixNano()%1000, i)
							}
							tc = &message.ToolCall{ID: id, Name: part.FunctionCall.Name}
							toolCallsBuffer[i] = tc
							toolCallOrder = append(toolCallOrder, i)
						}

						if part.FunctionCall.Name != "" {
							tc.Name = part.FunctionCall.Name
						}
						if len(part.ThoughtSignature) > 0 {
							tc.ThoughtSignature = part.ThoughtSignature
							signatureCache.Store(tc.ID, part.ThoughtSignature)
						}

						if len(part.FunctionCall.Args) > 0 {
							// Typed merge using json.RawMessage and map[string]json.RawMessage
							currentArgs := make(map[string]json.RawMessage)
							if len(tc.Arguments) > 0 {
								json.Unmarshal(tc.Arguments, &currentArgs)
							}
							for k, v := range part.FunctionCall.Args {
								vb, _ := json.Marshal(v)
								currentArgs[k] = vb
							}
							b, _ := json.Marshal(currentArgs)
							tc.Arguments = b
						}
					}
				}
			}
		}

		if len(toolCallOrder) > 0 {
			finalCalls := make([]message.ToolCall, 0, len(toolCallOrder))
			for _, idx := range toolCallOrder {
				tc := *toolCallsBuffer[idx]

				// Validate against JSON Schema if available
				if def, ok := toolDefinitionCache.Load(tc.Name); ok {
					tDef := def.(message.ToolDefinition)
					if len(tDef.Parameters) > 0 {
						if err := validateToolCall(tc, tDef.Parameters); err != nil {
							slog.Warn("tool_validation_failed", "name", tc.Name, "error", err)
						}
					}
				}

				finalCalls = append(finalCalls, tc)
			}
			out <- message.ChatResponse{ToolCalls: finalCalls}
		}

		slog.Debug("gemini_generate_done")
		out <- message.ChatResponse{Done: true}
	}()

	return out, nil
}

func validateToolCall(tc message.ToolCall, schema json.RawMessage) error {
	schemaLoader := gojsonschema.NewBytesLoader(schema)
	documentLoader := gojsonschema.NewBytesLoader(tc.Arguments)

	result, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err != nil {
		return err
	}

	if !result.Valid() {
		var errors []string
		for _, desc := range result.Errors() {
			errors = append(errors, desc.String())
		}
		return fmt.Errorf("validation failed: %s", strings.Join(errors, ", "))
	}
	return nil
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

	for _, m := range messages {
		role := "user"
		if m.Role == message.RoleAssistant {
			role = "model"
		}

		var parts []*genai.Part
		for _, p := range m.Parts {
			switch v := p.(type) {
			case message.TextPart:
				if v.Text != "" {
					parts = append(parts, genai.NewPartFromText(v.Text))
				}
			case message.ThoughtPart:
				if v.Text != "" {
					parts = append(parts, &genai.Part{Text: v.Text, Thought: true})
				}
			case message.ToolCallPart:
				args := make(map[string]json.RawMessage)
				json.Unmarshal(v.Arguments, &args)

				sig := v.ThoughtSignature
				if len(sig) == 0 {
					if cached, ok := signatureCache.Load(v.ID); ok {
						sig = cached.([]byte)
					}
				}

				// The genai.FunctionCall.Args expects map[string]interface{}.
				// Since we must use the SDK, we have to cast back to map[string]interface{}
				// but we do it only at the boundary.
				sdkArgs := make(map[string]interface{})
				for k, raw := range args {
					var val interface{}
					json.Unmarshal(raw, &val)
					sdkArgs[k] = val
				}

				parts = append(parts, &genai.Part{
					FunctionCall: &genai.FunctionCall{
						ID:   v.ID,
						Name: v.Name,
						Args: sdkArgs,
					},
					ThoughtSignature: sig,
				})
			case message.ToolResultPart:
				name := toolNames[v.ToolCallID]
				if name == "" {
					name = v.ToolCallID
				}
				parts = append(parts, &genai.Part{
					FunctionResponse: &genai.FunctionResponse{
						ID:       v.ToolCallID,
						Name:     name,
						Response: map[string]interface{}{"result": v.Content},
					},
				})
			}
		}

		if len(parts) > 0 {
			if m.Role == message.RoleSystem {
				if system == nil {
					system = &genai.Content{Role: "system", Parts: parts}
				} else {
					system.Parts = append(system.Parts, parts...)
				}
			} else {
				if len(contents) > 0 && contents[len(contents)-1].Role == role {
					contents[len(contents)-1].Parts = append(contents[len(contents)-1].Parts, parts...)
				} else {
					contents = append(contents, &genai.Content{Role: role, Parts: parts})
				}
			}
		}
	}

	return contents, system
}

func toGeminiTools(tools []message.ToolDefinition) []*genai.Tool {
	decls := make([]*genai.FunctionDeclaration, len(tools))
	for i, t := range tools {
		var params map[string]interface{}
		json.Unmarshal(t.Parameters, &params)

		decls[i] = &genai.FunctionDeclaration{
			Name:                 t.Name,
			Description:          t.Description,
			ParametersJsonSchema: params,
		}
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}
}

func init() {
	remote.Register("gemini", func(name string, options map[string]string, _ remote.Resolver) (remote.Provider, error) {
		return &GeminiProvider{APIKey: options["api_key"]}, nil
	})
}
