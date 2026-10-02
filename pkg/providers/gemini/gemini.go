package gemini

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	json "github.com/goccy/go-json"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/monitor"
	"github.com/lucasew/mclone/pkg/remote"
	"google.golang.org/genai"
)

// listHTTPClient bounds List so a hung models endpoint cannot block forever.
// genai.NewClient defaults to &http.Client{} with no Timeout when HTTPClient is nil.
var listHTTPClient = &http.Client{Timeout: 30 * time.Second}

// streamHTTPClient is used for Chat SSE. No overall Timeout so long streams can
// complete; dial and response-header deadlines still bound connection stalls.
// The request context cancels the body read when the caller aborts.
var streamHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	},
}

var signatureCache sync.Map
var toolDefinitionCache sync.Map

type ClientFactory func(ctx context.Context) (*genai.Client, error)

type GeminiProvider struct {
	APIKey        string
	ClientFactory ClientFactory
}

type GeminiConfig struct {
	APIKey string `mapstructure:"api_key"`
}

func (p *GeminiProvider) Name() string { return "gemini" }

func (p *GeminiProvider) List(ctx context.Context) ([]remote.Model, error) {
	client, err := p.client(ctx, listHTTPClient)
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

func (p *GeminiProvider) Chat(ctx context.Context, req message.Request) (<-chan message.Event, error) {
	client, err := p.client(ctx, streamHTTPClient)
	if err != nil {
		return nil, err
	}

	for _, t := range req.Options.Tools {
		toolDefinitionCache.Store(t.Name, t)
	}

	contents, systemInstruction := ToGeminiContents(req.Turns)
	config := &genai.GenerateContentConfig{
		SystemInstruction: systemInstruction,
	}
	if req.Options.Temperature != nil {
		t := float32(*req.Options.Temperature)
		config.Temperature = &t
	}
	if req.Options.MaxTokens != nil {
		config.MaxOutputTokens = int32(*req.Options.MaxTokens)
	}
	if req.Options.TopP != nil {
		t := float32(*req.Options.TopP)
		config.TopP = &t
	}
	if len(req.Options.Stop) > 0 {
		config.StopSequences = req.Options.Stop
	}
	if len(req.Options.Tools) > 0 {
		config.Tools = ToGeminiTools(req.Options.Tools)
	}
	if req.Options.JSONMode {
		config.ResponseMIMEType = "application/json"
	}

	out := make(chan message.Event)
	go func() {
		defer close(out)
		slog.Debug("gemini_generate_start", "model", req.Model, "msgs_len", len(req.Turns), "contents_len", len(contents))

		toolCallsBuffer := make(map[int]*message.ToolCall)
		var toolCallOrder []int

		for resp, err := range client.Models.GenerateContentStream(ctx, req.Model, contents, config) {
			if err != nil {
				monitor.ReportError(ctx, err, "action", "gemini_stream_error")
				out <- message.ResponseError{Err: err}
				return
			}

			for _, cand := range resp.Candidates {
				if cand.Content == nil {
					continue
				}
				for i, part := range cand.Content.Parts {
					if part.Text != "" {
						if part.Thought {
							out <- message.ReasoningDelta{Text: part.Text}
						} else {
							out <- message.TextDelta{Text: part.Text}
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
								if err := json.Unmarshal(tc.Arguments, &currentArgs); err != nil {
									monitor.ReportError(ctx, err, "action", "gemini_arg_merge_error")
								}
							}
							for k, v := range part.FunctionCall.Args {
								currentArgs[k] = v
							}
							b, err := json.Marshal(currentArgs)
							if err != nil {
								monitor.ReportError(ctx, err, "action", "gemini_arg_marshal_error")
								continue
							}
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
						tc.Arguments = CleanToolCallArgs(tc, tDef.Parameters)
					}
				}
				finalCalls = append(finalCalls, tc)
			}
			for _, call := range finalCalls {
				out <- message.ToolCallFinished{Call: call}
			}
			out <- message.ResponseCompleted{Reason: message.StopReasonToolCall}
			return
		}
		out <- message.ResponseCompleted{Reason: message.StopReasonEndTurn}
	}()
	return out, nil
}

func CleanToolCallArgs(tc message.ToolCall, schema json.RawMessage) json.RawMessage {
	var schemaDef struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &schemaDef); err != nil || schemaDef.Properties == nil {
		return tc.Arguments
	}

	var args map[string]interface{}
	if err := json.Unmarshal(tc.Arguments, &args); err != nil {
		return tc.Arguments
	}

	var removed []string
	for k := range args {
		if _, ok := schemaDef.Properties[k]; !ok {
			removed = append(removed, k)
			delete(args, k)
		}
	}

	if len(removed) > 0 {
		slog.Warn("tool_args_cleaned", "name", tc.Name, "id", tc.ID, "removed", strings.Join(removed, ", "))
	}

	cleaned, err := json.Marshal(args)
	if err != nil {
		return tc.Arguments
	}
	return json.RawMessage(cleaned)
}

func (p *GeminiProvider) client(ctx context.Context, httpClient *http.Client) (*genai.Client, error) {
	if p.ClientFactory != nil {
		return p.ClientFactory(ctx)
	}
	return genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:     p.APIKey,
		Backend:    genai.BackendGeminiAPI,
		HTTPClient: httpClient,
	})
}

func ToGeminiContents(messages []message.Turn) ([]*genai.Content, *genai.Content) {
	var system *genai.Content
	var rawTurns []*genai.Content

	// Collect tool call names and determine which have valid thought signatures
	toolNames := make(map[string]string)
	unsignedCalls := make(map[string]bool)
	for _, m := range messages {
		for _, p := range m.Parts {
			if tc, ok := p.(message.ToolCallPart); ok {
				name := tc.Name
				if name == "" {
					name = "tool"
				}
				toolNames[tc.ID] = name
				sig := tc.ThoughtSignature
				if len(sig) == 0 {
					if cached, ok := signatureCache.Load(tc.ID); ok {
						sig = cached.([]byte)
					}
				}
				if len(sig) == 0 {
					unsignedCalls[tc.ID] = true
				}
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
				if unsignedCalls[v.ID] {
					// No thought signature available — drop from history
					slog.Debug("gemini_skip_unsigned_call", "id", v.ID, "name", v.Name)
					continue
				}
				name := v.Name
				if name == "" {
					name = "tool"
				}
				sdkArgs := map[string]interface{}{}
				if len(v.Arguments) > 0 {
					if err := json.Unmarshal(v.Arguments, &sdkArgs); err != nil {
						var fallback any
						if err2 := json.Unmarshal(v.Arguments, &fallback); err2 == nil {
							sdkArgs = map[string]interface{}{"input": fallback}
						} else {
							monitor.ReportError(context.Background(), err, "action", "gemini_arg_unmarshal_error")
						}
					}
				}
				sig := v.ThoughtSignature
				if len(sig) == 0 {
					if cached, ok := signatureCache.Load(v.ID); ok {
						sig = cached.([]byte)
					}
				}
				parts = append(parts, &genai.Part{
					FunctionCall:     &genai.FunctionCall{ID: v.ID, Name: name, Args: sdkArgs},
					ThoughtSignature: sig,
				})
			case message.ToolResultPart:
				name := toolNames[v.ToolCallID]
				if name == "" {
					// Orphaned tool result (no matching tool call in history) — skip it
					slog.Debug("gemini_skip_orphaned_tool_result", "tool_call_id", v.ToolCallID)
					continue
				}
				if unsignedCalls[v.ToolCallID] {
					// Corresponding call was dropped — drop result too
					slog.Debug("gemini_skip_unsigned_result", "tool_call_id", v.ToolCallID, "name", name)
					continue
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

func ToGeminiTools(tools []message.ToolDefinition) []*genai.Tool {
	decls := make([]*genai.FunctionDeclaration, 0, len(tools))
	for i, t := range tools {
		if t.Type != "" && t.Type != "function" {
			slog.Debug("gemini_skip_tool", "name", t.Name, "type", t.Type)
			continue
		}
		var params map[string]interface{}
		if err := json.Unmarshal(t.Parameters, &params); err != nil || params == nil {
			slog.Debug("gemini_tool_schema_fallback", "name", t.Name, "raw_len", len(t.Parameters))
			params = map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			}
		}
		CleanSchema(params)
		desc := t.Description
		if desc == "" {
			desc = t.Name
		}
		name := sanitizeGeminiToolName(t.Name, i)
		decls = append(decls, &genai.FunctionDeclaration{
			Name: name, Description: desc, ParametersJsonSchema: params,
		})
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}
}

func sanitizeGeminiToolName(name string, index int) string {
	if name == "" {
		name = fmt.Sprintf("tool_%d", index)
	}

	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '.' || r == ':' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= 128 {
			break
		}
	}

	sanitized := b.String()
	if sanitized == "" {
		return fmt.Sprintf("tool_%d", index)
	}
	first := sanitized[0]
	if (first < 'a' || first > 'z') && (first < 'A' || first > 'Z') && first != '_' {
		sanitized = "_" + sanitized
	}
	if len(sanitized) > 128 {
		sanitized = sanitized[:128]
	}
	return sanitized
}

// cleanSchema recursively removes JSON Schema fields that Gemini doesn't support.
func CleanSchema(m map[string]interface{}) {
	delete(m, "$schema")
	delete(m, "additionalProperties")

	// exclusiveMinimum/Maximum: Draft 2020-12 uses numbers, Gemini doesn't support them
	delete(m, "exclusiveMinimum")
	delete(m, "exclusiveMaximum")

	// Recurse into properties
	if props, ok := m["properties"].(map[string]interface{}); ok {
		for _, v := range props {
			if sub, ok := v.(map[string]interface{}); ok {
				CleanSchema(sub)
			}
		}
	}
	// Recurse into items
	if items, ok := m["items"].(map[string]interface{}); ok {
		CleanSchema(items)
	}
}

func init() {
	remote.Register("gemini", func(name string, options map[string]any, _ remote.Resolver) (remote.Provider, error) {
		var cfg GeminiConfig
		if err := remote.DecodeOptions(options, &cfg); err != nil {
			return nil, err
		}
		return &GeminiProvider{APIKey: cfg.APIKey}, nil
	})
}
