package message

import (
	"context"
	"encoding/json"

	"github.com/tmc/langchaingo/llms"
)

// ToLangChainMessages converts internal messages to langchaingo format.
func ToLangChainMessages(msgs []Message) []llms.MessageContent {
	out := make([]llms.MessageContent, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, toLangChainMessage(m))
	}
	return out
}

func toLangChainMessage(m Message) llms.MessageContent {
	role := llms.ChatMessageTypeHuman
	switch m.Role {
	case RoleSystem:
		role = llms.ChatMessageTypeSystem
	case RoleAssistant:
		role = llms.ChatMessageTypeAI
	case RoleUser:
		role = llms.ChatMessageTypeHuman
	case RoleTool:
		role = llms.ChatMessageTypeTool
	}

	parts := make([]llms.ContentPart, 0, len(m.Parts))
	for _, p := range m.Parts {
		switch v := p.(type) {
		case TextPart:
			parts = append(parts, llms.TextContent{Text: v.Text})
		case ThoughtPart:
			parts = append(parts, llms.TextContent{Text: "<thought>" + v.Text + "</thought>"})
		case ToolCallPart:
			parts = append(parts, llms.ToolCall{
				ID:   v.ID,
				Type: "function",
				FunctionCall: &llms.FunctionCall{
					Name:      v.Name,
					Arguments: string(v.Arguments),
				},
			})
		case ToolResultPart:
			parts = append(parts, llms.ToolCallResponse{
				ToolCallID: v.ToolCallID,
				Content:    v.Content,
			})
		}
	}

	return llms.MessageContent{Role: role, Parts: parts}
}

// ToLangChainTools converts internal tool definitions to langchaingo format.
func ToLangChainTools(tools []ToolDefinition) []llms.Tool {
	out := make([]llms.Tool, len(tools))
	for i, t := range tools {
		var params map[string]interface{}
		json.Unmarshal(t.Parameters, &params)
		out[i] = llms.Tool{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		}
	}
	return out
}

// ToLangChainOptions converts ChatOptions to langchaingo call options.
func ToLangChainOptions(opts ChatOptions, streamFunc func(ctx context.Context, chunk []byte) error) []llms.CallOption {
	var out []llms.CallOption
	if len(opts.Tools) > 0 {
		out = append(out, llms.WithTools(ToLangChainTools(opts.Tools)))
	}
	if opts.JSONMode {
		out = append(out, llms.WithJSONMode())
	}
	if streamFunc != nil {
		out = append(out, llms.WithStreamingFunc(streamFunc))
	}
	return out
}

// ToolCallFromLangChain converts a langchaingo ToolCall to our internal type.
func ToolCallFromLangChain(tc llms.ToolCall) ToolCall {
	return ToolCall{
		ID:        tc.ID,
		Name:      tc.FunctionCall.Name,
		Arguments: json.RawMessage(tc.FunctionCall.Arguments),
	}
}

// ToolCallsFromLangChain converts a slice of langchaingo ToolCalls.
func ToolCallsFromLangChain(tcs []llms.ToolCall) []ToolCall {
	if len(tcs) == 0 {
		return nil
	}
	out := make([]ToolCall, len(tcs))
	for i, tc := range tcs {
		out[i] = ToolCallFromLangChain(tc)
	}
	return out
}
