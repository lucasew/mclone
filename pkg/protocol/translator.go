package protocol

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tmc/langchaingo/llms"
)

type IncomingMessage struct {
	Role       string          `json:"role"`
	Content    interface{}     `json:"content"`
	ToolCalls  []llms.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

func (m *IncomingMessage) ToLangChain() (llms.MessageContent, error) {
	role := llms.ChatMessageTypeHuman
	switch m.Role {
	case "system":
		role = llms.ChatMessageTypeSystem
	case "assistant":
		role = llms.ChatMessageTypeAI
	case "user":
		role = llms.ChatMessageTypeHuman
	case "tool":
		return llms.MessageContent{
			Role: llms.ChatMessageTypeTool,
			Parts: []llms.ContentPart{
				llms.ToolCallResponse{
					ToolCallID: m.ToolCallID,
					Content:    fmt.Sprintf("%v", m.Content),
				},
			},
		}, nil
	}

	var parts []llms.ContentPart
	switch v := m.Content.(type) {
	case string:
		parts = append(parts, llms.TextPart(v))
	case []interface{}:
		for _, p := range v {
			if pm, ok := p.(map[string]interface{}); ok {
				t, _ := pm["type"].(string)
				switch t {
				case "text":
					if txt, ok := pm["text"].(string); ok {
						parts = append(parts, llms.TextPart(txt))
					}
				case "tool_use":
					id, _ := pm["id"].(string)
					name, _ := pm["name"].(string)
					var args string
					if input, ok := pm["input"].(map[string]interface{}); ok {
						b, _ := json.Marshal(input)
						args = string(b)
					}
					parts = append(parts, llms.ToolCall{
						ID:   id,
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      name,
							Arguments: args,
						},
					})
				case "tool_result":
					id, _ := pm["tool_use_id"].(string)
					rawContent := pm["content"]
					contentStr := ""
					if s, ok := rawContent.(string); ok {
						contentStr = s
					} else {
						// Flatten blocks if it's a list
						if list, ok := rawContent.([]interface{}); ok {
							for _, item := range list {
								if block, ok := item.(map[string]interface{}); ok {
									if txt, ok := block["text"].(string); ok {
										contentStr += txt
									}
								}
							}
						} else {
							b, _ := json.Marshal(rawContent)
							contentStr = string(b)
						}
					}
					parts = append(parts, llms.ToolCallResponse{
						ToolCallID: id,
						Content:    contentStr,
					})
				}
			}
		}
	}

	// For Ollama/GoogleAI, we need to ensure there's only one TextPart at the beginning if there are other parts
	// But actually, LangChainGo should handle it. The issue is when there are MULTIPLE TextParts.

	finalParts := []llms.ContentPart{}
	var currentText []string

	flushText := func() {
		if len(currentText) > 0 {
			finalParts = append(finalParts, llms.TextPart(strings.Join(currentText, "")))
			currentText = nil
		}
	}

	for _, p := range parts {
		if tp, ok := p.(llms.TextContent); ok {
			currentText = append(currentText, tp.Text)
		} else {
			flushText()
			finalParts = append(finalParts, p)
		}
	}
	flushText()

	if m.Role == "assistant" && len(m.ToolCalls) > 0 {
		for _, tc := range m.ToolCalls {
			finalParts = append(finalParts, tc)
		}
	}

	return llms.MessageContent{
		Role:  role,
		Parts: finalParts,
	}, nil
}

type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

func (t Tool) ToLangChain() llms.Tool {
	params := t.Parameters
	if params == nil {
		params = t.InputSchema
	}
	return llms.Tool{
		Type: "function",
		Function: &llms.FunctionDefinition{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		},
	}
}
