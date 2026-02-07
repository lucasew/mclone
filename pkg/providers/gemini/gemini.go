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

	// NORMALIZATION PASS 1: Merge consecutive turns of the same role
	var merged []*genai.Content
	for _, t := range rawTurns {
		if len(merged) > 0 && merged[len(merged)-1].Role == t.Role {
			merged[len(merged)-1].Parts = append(merged[len(merged)-1].Parts, t.Parts...)
		} else {
			merged = append(merged, t)
		}
	}

	// NORMALIZATION PASS 2: Structural Integrity
	// - Moves Call parts to the end of model turns.
	// - Ensures User turns with Responses follow Model turns with Calls immediately.
	var normalized []*genai.Content
	for i := 0; i < len(merged); i++ {
		turn := merged[i]

		if turn.Role == "model" {
			// Rearrange model parts: [Thoughts/Text] then [Calls]
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
		}

		hasResponse := false
		for _, p := range turn.Parts {
			if p.FunctionResponse != nil {
				hasResponse = true
				break
			}
		}

		if hasResponse && len(normalized) > 0 {
			// Find the model turn that HAD the call
			var lastModelWithCallIdx = -1
			for j := len(normalized) - 1; j >= 0; j-- {
				if normalized[j].Role == "model" {
					hasCall := false
					for _, p := range normalized[j].Parts {
						if p.FunctionCall != nil {
							hasCall = true
							break
						}
					}
					if hasCall {
						lastModelWithCallIdx = j
						break
					}
				}
			}

			if lastModelWithCallIdx != -1 {
				// Rule violation: if there are turns between lastModelWithCall and current Response turn,
				// they MUST be moved into the model turn (before the calls).

				// Identify parts to move
				var partsToMove []*genai.Part
				for j := lastModelWithCallIdx + 1; j < len(normalized); j++ {
					partsToMove = append(partsToMove, normalized[j].Parts...)
				}

				// Re-organize model turn: [Original Text] + [Moved Text] + [Calls]
				originalModel := normalized[lastModelWithCallIdx]
				var mText []*genai.Part
				var mCalls []*genai.Part
				for _, p := range originalModel.Parts {
					if p.FunctionCall != nil {
						mCalls = append(mCalls, p)
					} else {
						mText = append(mText, p)
					}
				}

				originalModel.Parts = append(mText, partsToMove...)
				originalModel.Parts = append(originalModel.Parts, mCalls...)

				// Truncate history to the corrected model turn
				normalized = normalized[:lastModelWithCallIdx+1]

				// Now, the response turn MUST be only responses.
				var responsesOnly []*genai.Part
				var userText []*genai.Part
				for _, p := range turn.Parts {
					if p.FunctionResponse != nil {
						responsesOnly = append(responsesOnly, p)
					} else {
						userText = append(userText, p)
					}
				}

				// Append Response turn
				normalized = append(normalized, &genai.Content{Role: "user", Parts: responsesOnly})

				// Append User text turn if any (this maintains alternation because the next turn will be Model)
				if len(userText) > 0 {
					normalized = append(normalized, &genai.Content{Role: "user", Parts: userText})
				}
				continue
			}
		}
		normalized = append(normalized, turn)
	}

	// FINAL PASS: Role Alternation
	var result []*genai.Content
	for _, t := range normalized {
		if len(result) > 0 && result[len(result)-1].Role == t.Role {
			result[len(result)-1].Parts = append(result[len(result)-1].Parts, t.Parts...)
		} else {
			result = append(result, t)
		}
	}

	// Debug sequence
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
		slog.Debug("gemini_final_sequence", "idx", i, "role", t.Role, "parts", strings.Join(pt, ","))
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
