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

var signatureCache sync.Map
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
		slog.Debug("gemini_generate_start", "model", modelName, "msgs_len", len(messages), "contents_len", len(contents))

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
								id = fmt.Sprintf("toolu_gen_%d_%d", time.Now().UnixNano()%1000000, i)
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
							currentArgs := make(map[string]interface{})
							if len(tc.Arguments) > 0 {
								json.Unmarshal(tc.Arguments, &currentArgs)
							}
							for k, v := range part.FunctionCall.Args {
								currentArgs[k] = v
							}
							b, _ := json.Marshal(currentArgs)
							tc.Arguments = json.RawMessage(b)
						}
					}
				}
			}
		}

		if len(toolCallOrder) > 0 {
			finalCalls := make([]message.ToolCall, 0, len(toolCallOrder))
			for _, idx := range toolCallOrder {
				tc := *toolCallsBuffer[idx]
				if def, ok := toolDefinitionCache.Load(tc.Name); ok {
					tDef := def.(message.ToolDefinition)
					if len(tDef.Parameters) > 0 {
						validateToolCall(tc, tDef.Parameters)
					}
				}
				finalCalls = append(finalCalls, tc)
			}
			out <- message.ChatResponse{ToolCalls: finalCalls}
		}
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
		slog.Warn("tool_validation_failed", "name", tc.Name, "id", tc.ID)
	}
	return nil
}

func (p *GeminiProvider) client() (*genai.Client, error) {
	return genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey: p.APIKey, Backend: genai.BackendGeminiAPI,
	})
}

func toGeminiContents(messages []message.Message) ([]*genai.Content, *genai.Content) {
	var system *genai.Content
	var rawTurns []*genai.Content

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
				if strings.TrimSpace(v.Text) != "" {
					parts = append(parts, genai.NewPartFromText(v.Text))
				}
			case message.ThoughtPart:
				if strings.TrimSpace(v.Text) != "" {
					parts = append(parts, &genai.Part{Text: v.Text, Thought: true})
				}
			case message.ToolCallPart:
				sdkArgs := make(map[string]interface{})
				json.Unmarshal(v.Arguments, &sdkArgs)
				sig := v.ThoughtSignature
				if len(sig) == 0 {
					if cached, ok := signatureCache.Load(v.ID); ok {
						sig = cached.([]byte)
					}
				}
				parts = append(parts, &genai.Part{
					FunctionCall:     &genai.FunctionCall{ID: v.ID, Name: v.Name, Args: sdkArgs},
					ThoughtSignature: sig,
				})
			case message.ToolResultPart:
				name := toolNames[v.ToolCallID]
				if name == "" {
					name = v.ToolCallID
				}
				content := v.Content
				if strings.Contains(content, "output was truncated") {
					content += "\n\nNote: Output truncated. Use Read tool on the path above for full content."
				}
				parts = append(parts, &genai.Part{
					FunctionResponse: &genai.FunctionResponse{
						ID: v.ToolCallID, Name: name,
						Response: map[string]interface{}{"result": content},
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
				rawTurns = append(rawTurns, &genai.Content{Role: role, Parts: parts})
			}
		}
	}

	// Step 1: Force role alternation and merge consecutive turns of the same role
	var merged []*genai.Content
	for _, t := range rawTurns {
		if len(merged) > 0 && merged[len(merged)-1].Role == t.Role {
			merged[len(merged)-1].Parts = append(merged[len(merged)-1].Parts, t.Parts...)
		} else {
			merged = append(merged, t)
		}
	}

	// Step 2: Structural integrity pass (strictly enforced)
	// Rules:
	// - Model turn: [Thoughts/Text] then [Calls]. Nothing after Calls.
	// - After a turn with Calls, the NEXT turn MUST be User and MUST start with Responses.

	var final []*genai.Content
	for i := 0; i < len(merged); i++ {
		turn := merged[i]

		if turn.Role == "model" {
			// Organize Model turn: [Non-Calls] then [Calls]
			var nonCalls []*genai.Part
			var calls []*genai.Part
			for _, p := range turn.Parts {
				if p.FunctionCall != nil {
					calls = append(calls, p)
				} else {
					nonCalls = append(nonCalls, p)
				}
			}
			turn.Parts = append(nonCalls, calls...)
			final = append(final, turn)
			continue
		}

		// If it's a User turn, check if it contains responses.
		hasResponse := false
		for _, p := range turn.Parts {
			if p.FunctionResponse != nil {
				hasResponse = true
				break
			}
		}

		if hasResponse {
			// Find the model turn that HAD the calls (it should be the last model turn in final)
			var lastModelIdx = -1
			for j := len(final) - 1; j >= 0; j-- {
				if final[j].Role == "model" {
					lastModelIdx = j
					break
				}
			}

			if lastModelIdx != -1 {
				// Rule: Any turns between the last Model turn and this User response turn
				// must be flattened into their respective preceding turns.

				// 1. Move everything between lastModel and current turn to either Model or User turn.
				for j := lastModelIdx + 1; j < len(final); j++ {
					interTurn := final[j]
					if interTurn.Role == "model" {
						// This shouldn't happen due to Step 1 merge, but safety first.
						final[lastModelIdx].Parts = append(final[lastModelIdx].Parts, interTurn.Parts...)
					} else {
						// Move intermediate user text to follow the responses in the current turn.
						turn.Parts = append(turn.Parts, interTurn.Parts...)
					}
				}
				final = final[:lastModelIdx+1]

				// 2. Re-organize the last Model turn to put Calls at the absolute end.
				var mOther []*genai.Part
				var mCalls []*genai.Part
				for _, p := range final[lastModelIdx].Parts {
					if p.FunctionCall != nil {
						mCalls = append(mCalls, p)
					} else {
						mOther = append(mOther, p)
					}
				}
				final[lastModelIdx].Parts = append(mOther, mCalls...)

				// 3. Re-organize the current User turn to put Responses at the absolute start.
				var uResponses []*genai.Part
				var uOther []*genai.Part
				for _, p := range turn.Parts {
					if p.FunctionResponse != nil {
						uResponses = append(uResponses, p)
					} else {
						uOther = append(uOther, p)
					}
				}
				turn.Parts = append(uResponses, uOther...)
			}
		}

		final = append(final, turn)
	}

	// Final Step: Role Alternation Pass (guarantees alternation even after repairs)
	var result []*genai.Content
	for _, t := range final {
		if len(result) > 0 && result[len(result)-1].Role == t.Role {
			result[len(result)-1].Parts = append(result[len(result)-1].Parts, t.Parts...)
		} else {
			result = append(result, t)
		}
	}

	// Logging for sequence validation
	for i, t := range result {
		var pt []string
		for _, p := range t.Parts {
			if p.FunctionCall != nil {
				pt = append(pt, "Call")
			}
			if p.FunctionResponse != nil {
				pt = append(pt, "Resp")
			}
			if p.Text != "" {
				if p.Thought {
					pt = append(pt, "Thought")
				} else {
					pt = append(pt, "Text")
				}
			}
		}
		slog.Debug("gemini_normalized_sequence", "idx", i, "role", t.Role, "parts", strings.Join(pt, ","))
	}

	return result, system
}

func toGeminiTools(tools []message.ToolDefinition) []*genai.Tool {
	decls := make([]*genai.FunctionDeclaration, len(tools))
	for i, t := range tools {
		var params map[string]interface{}
		json.Unmarshal(t.Parameters, &params)
		decls[i] = &genai.FunctionDeclaration{
			Name: t.Name, Description: t.Description, ParametersJsonSchema: params,
		}
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}
}

func init() {
	remote.Register("gemini", func(name string, options map[string]string, _ remote.Resolver) (remote.Provider, error) {
		return &GeminiProvider{APIKey: options["api_key"]}, nil
	})
}
