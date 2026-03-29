package message

// Turn represents a normalized, atomic exchange in the conversation history.
// Providers expect sequences of Turns (typically alternating between User and Assistant)
// rather than raw strings, enabling rich context passing including multimodal payloads and tool states.
type Turn struct {
	Role  Role
	Parts []Part
}

// Request is the provider-agnostic container for initiating a chat generation.
// This structure normalizes prompt inputs so the routing layer can seamlessly proxy
// the same Request across different backend providers (e.g. from OpenAI to Anthropic) without modification.
type Request struct {
	// Model specifies the target LLM identifier (e.g. "gpt-4-turbo", "claude-3-opus").
	Model   string
	// Turns contains the complete conversational lineage leading up to the current generation.
	Turns   []Turn
	// Options configures the generation behavior (temperature, JSON mode, available tools, etc).
	Options ChatOptions
}
