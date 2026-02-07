package message

import "encoding/json"

// Role represents the role of a message sender.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message represents a single message in a conversation.
type Message struct {
	Role  Role
	Parts []Part
}

// TextParts creates a Message with a single text part.
func TextParts(role Role, text string) Message {
	return Message{Role: role, Parts: []Part{TextPart{Text: text}}}
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

// ToolCallPart represents a tool/function call made by the assistant.
type ToolCallPart struct {
	ID        string
	Name      string
	Arguments string // raw JSON
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
	Name        string
	Description string
	Parameters  map[string]any
}

// ChatOptions holds options for a chat request.
type ChatOptions struct {
	Tools    []ToolDefinition
	JSONMode bool
}

// ToolCall represents a tool call in a response.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // raw JSON
}

// ParseArguments unmarshals the arguments JSON into the target.
func (tc ToolCall) ParseArguments(target any) error {
	return json.Unmarshal([]byte(tc.Arguments), target)
}

// ChatResponse represents a streaming chunk from a provider.
type ChatResponse struct {
	Content   string
	ToolCalls []ToolCall
	Done      bool
	Error     error
}
