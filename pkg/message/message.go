package message

import json "github.com/goccy/go-json"

// Role represents the role of a message sender.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// TextTurn creates a turn with a single text part.
func TextTurn(role Role, text string) Turn {
	return Turn{Role: role, Parts: []Part{TextPart{Text: text}}}
}

// Part is a content part within a message.
type Part interface {
	isPart()
}

// TextPart is a plain text content part.
type TextPart struct {
	Text string
}

func (TextPart) isPart() {}

// ThoughtPart represents a chain-of-thought or internal reasoning part.
type ThoughtPart struct {
	Text string
}

func (ThoughtPart) isPart() {}

// ToolCallPart represents a tool/function call made by the assistant.
type ToolCallPart struct {
	ID               string
	Name             string
	Arguments        json.RawMessage // raw JSON
	ThoughtSignature []byte          // Original binary signature from Gemini
}

func (ToolCallPart) isPart() {}

// ToolResultPart represents the result of a tool call.
type ToolResultPart struct {
	ToolCallID string
	Content    string
}

func (ToolResultPart) isPart() {}

// ToolDefinition describes a tool available for the model to call.
type ToolDefinition struct {
	Type        string // "function" (default), "web_search_20250305", etc.
	Name        string
	Description string
	Parameters  json.RawMessage // JSON Schema
}

// ChatOptions holds options for a chat request.
type ChatOptions struct {
	Tools       []ToolDefinition
	JSONMode    bool
	Temperature *float64
	TopP        *float64
	MaxTokens   *int
	Stop        []string
}

// WithDefaults fills nil fields from d (config defaults).
func (o ChatOptions) WithDefaults(d ChatOptions) ChatOptions {
	if o.Temperature == nil {
		o.Temperature = d.Temperature
	}
	if o.TopP == nil {
		o.TopP = d.TopP
	}
	if o.MaxTokens == nil {
		o.MaxTokens = d.MaxTokens
	}
	if len(o.Stop) == 0 {
		o.Stop = d.Stop
	}
	return o
}

// ToolCall represents a tool call in a response.
type ToolCall struct {
	ID               string
	Name             string
	Arguments        json.RawMessage // raw JSON
	ThoughtSignature []byte
}

// ParseArguments unmarshals the arguments JSON into the target.
func ParseArguments[T any](tc ToolCall, target *T) error {
	return json.Unmarshal(tc.Arguments, target)
}
