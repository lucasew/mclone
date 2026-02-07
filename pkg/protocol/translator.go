package protocol

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lucasew/mclone/pkg/message"
)

type IncomingMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func splitID(id string) (string, string) {
	if parts := strings.SplitN(id, "||", 2); len(parts) == 2 {
		return parts[0], parts[1]
	}
	return id, ""
}

func (m *IncomingMessage) ToMessage() (message.Message, error) {
	role := message.RoleUser
	switch m.Role {
	case "system":
		role = message.RoleSystem
	case "assistant":
		role = message.RoleAssistant
	case "user":
		role = message.RoleUser
	case "tool":
		id, _ := splitID(m.ToolCallID)
		return message.Message{
			Role: message.RoleTool,
			Parts: []message.Part{
				message.ToolResultPart{
					ToolCallID: id,
					Content:    fmt.Sprintf("%v", m.Content),
				},
			},
		}, nil
	}

	var parts []message.Part
	switch v := m.Content.(type) {
	case string:
		parts = append(parts, message.TextPart{Text: v})
	case []any:
		for _, p := range v {
			if pm, ok := p.(map[string]any); ok {
				t, _ := pm["type"].(string)
				switch t {
				case "text":
					if txt, ok := pm["text"].(string); ok {
						parts = append(parts, message.TextPart{Text: txt})
					}
				case "tool_use":
					idRaw, _ := pm["id"].(string)
					name, _ := pm["name"].(string)
					var args string
					if input, ok := pm["input"].(map[string]any); ok {
						b, _ := json.Marshal(input)
						args = string(b)
					}
					id, signature := splitID(idRaw)
					parts = append(parts, message.ToolCallPart{
						ID: id, Name: name, Arguments: args,
						ThoughtSignature: signature,
					})
				case "tool_result":
					idRaw, _ := pm["tool_use_id"].(string)
					id, _ := splitID(idRaw)
					contentStr := extractContent(pm["content"])
					parts = append(parts, message.ToolResultPart{
						ToolCallID: id, Content: contentStr,
					})
				}
			}
		}
	}

	// Merge consecutive text parts only for user messages
	finalParts := parts
	if m.Role == "user" {
		finalParts = mergeTextParts(parts)
	}

	// Append OpenAI-style tool_calls from assistant messages
	if m.Role == "assistant" && len(m.ToolCalls) > 0 {
		for _, tc := range m.ToolCalls {
			id, signature := splitID(tc.ID)
			finalParts = append(finalParts, message.ToolCallPart{
				ID: id, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
				ThoughtSignature: signature,
			})
		}
	}

	return message.Message{Role: role, Parts: finalParts}, nil
}

func extractContent(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if block, ok := item.(map[string]any); ok {
				if txt, ok := block["text"].(string); ok {
					parts = append(parts, txt)
				}
			}
		}
		return strings.Join(parts, "")
	default:
		b, _ := json.Marshal(raw)
		return string(b)
	}
}

func mergeTextParts(parts []message.Part) []message.Part {
	var result []message.Part
	var currentText []string

	flush := func() {
		if len(currentText) > 0 {
			result = append(result, message.TextPart{Text: strings.Join(currentText, "")})
			currentText = nil
		}
	}

	for _, p := range parts {
		if tp, ok := p.(message.TextPart); ok {
			currentText = append(currentText, tp.Text)
		} else {
			flush()
			result = append(result, p)
		}
	}
	flush()

	return result
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

func (t Tool) ToDefinition() message.ToolDefinition {
	params := t.Parameters
	if params == nil {
		params = t.InputSchema
	}
	return message.ToolDefinition{
		Name:        t.Name,
		Description: t.Description,
		Parameters:  params,
	}
}
