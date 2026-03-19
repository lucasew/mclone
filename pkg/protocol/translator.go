package protocol

import (
	"strings"
	"unicode"

	"github.com/goccy/go-json"
	"github.com/lucasew/mclone/pkg/message"
)

type IncomingMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

func (m *IncomingMessage) ToTurn() (message.Turn, error) {
	role := message.RoleUser
	switch m.Role {
	case "system":
		role = message.RoleSystem
	case "assistant":
		role = message.RoleAssistant
	case "user":
		role = message.RoleUser
	case "tool":
		var contentStr string
		if err := json.Unmarshal(m.Content, &contentStr); err != nil {
			contentStr = string(m.Content)
		}
		return message.Turn{
			Role: message.RoleTool,
			Parts: []message.Part{
				message.ToolResultPart{
					ToolCallID: m.ToolCallID,
					Content:    contentStr,
				},
			},
		}, nil
	}

	var parts []message.Part

	// Fast path based on first byte
	firstByte := firstNonSpaceByte(m.Content)

	if firstByte == '"' {
		// Try parsing as string
		var contentStr string
		if err := json.Unmarshal(m.Content, &contentStr); err == nil {
			parts = append(parts, parseText(contentStr)...)
		}
	} else if firstByte == '[' {
		// Try parsing as list of blocks
		var blocks []ContentBlock
		if err := json.Unmarshal(m.Content, &blocks); err == nil {
			for _, b := range blocks {
				switch b.Type {
				case "text":
					parts = append(parts, parseText(b.Text)...)
				case "tool_use":
					parts = append(parts, message.ToolCallPart{
						ID: b.ID, Name: b.Name, Arguments: b.Input,
					})
				case "tool_result":
					id := b.ToolUseID
					if id == "" {
						id = b.ID
					}
					parts = append(parts, message.ToolResultPart{
						ToolCallID: id, Content: extractContent(b.Content),
					})
				}
			}
		}
	} else {
		// Fallback for unknown types or failed fast-path
		var contentStr string
		if err := json.Unmarshal(m.Content, &contentStr); err == nil {
			parts = append(parts, parseText(contentStr)...)
		} else {
			var blocks []ContentBlock
			if err := json.Unmarshal(m.Content, &blocks); err == nil {
				for _, b := range blocks {
					if b.Type == "text" {
						parts = append(parts, parseText(b.Text)...)
					}
				}
			}
		}
	}

	if m.Role == "assistant" && len(m.ToolCalls) > 0 {
		for _, tc := range m.ToolCalls {
			parts = append(parts, message.ToolCallPart{
				ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
			})
		}
	}

	if role == message.RoleUser {
		parts = mergeTextParts(parts)
	}

	return message.Turn{Role: role, Parts: parts}, nil
}

func parseText(text string) []message.Part {
	if text == "" {
		return nil
	}
	var parts []message.Part
	current := text
	for {
		start := strings.Index(current, "<thought>")
		if start == -1 {
			if current != "" {
				parts = append(parts, message.TextPart{Text: current})
			}
			break
		}
		if start > 0 {
			parts = append(parts, message.TextPart{Text: current[:start]})
		}
		end := strings.Index(current[start:], "</thought>")
		if end == -1 {
			parts = append(parts, message.TextPart{Text: current[start:]})
			break
		}
		thoughtContent := current[start+len("<thought>") : start+end]
		parts = append(parts, message.ThoughtPart{Text: thoughtContent})
		current = current[start+end+len("</thought>"):]
	}
	return parts
}

func extractContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	firstByte := firstNonSpaceByte(raw)
	if firstByte == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
	} else if firstByte == '[' {
		var blocks []ContentBlock
		if err := json.Unmarshal(raw, &blocks); err == nil {
			var parts []string
			for _, b := range blocks {
				if b.Type == "text" {
					parts = append(parts, b.Text)
				}
			}
			return strings.Join(parts, "")
		}
	}

	return string(raw)
}

func firstNonSpaceByte(data []byte) byte {
	for _, b := range data {
		if !unicode.IsSpace(rune(b)) {
			return b
		}
	}
	return 0
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
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

func (t Tool) ToDefinition() message.ToolDefinition {
	params := t.Parameters
	if len(params) == 0 {
		params = t.InputSchema
	}
	typ := t.Type
	if typ == "" || typ == "function" {
		typ = "function"
	}
	return message.ToolDefinition{
		Type:        typ,
		Name:        t.Name,
		Description: t.Description,
		Parameters:  params,
	}
}
