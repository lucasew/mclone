package message

// StopReason normalizes why the model finished generating text or tool requests.
// It abstracts away the provider-specific termination flags so downstream clients
// can reliably determine if generation is complete or awaiting further action.
type StopReason string

const (
	// StopReasonEndTurn indicates the model fully completed its logical response.
	StopReasonEndTurn  StopReason = "end_turn"
	// StopReasonToolCall indicates the model paused generation to request an external function execution.
	StopReasonToolCall StopReason = "tool_call"
)

// Event is the atomic unit of output streamed from a provider during generation.
// This interface allows multiplexing text, reasoning, tool updates, and lifecycle signals
// over a single async Go channel.
type Event interface {
	isEvent() // unexported marker method enforcing interface compliance
}

// TextDelta represents a contiguous chunk of string output from the model.
// When concatenated, TextDeltas form the final user-facing text payload.
type TextDelta struct {
	Text string
}

func (TextDelta) isEvent() {}

// ReasoningDelta carries chain-of-thought progression emitted before the final text response.
// Handlers can log this separately from user-facing text for debugging or enhanced transparency.
type ReasoningDelta struct {
	Text string
}

func (ReasoningDelta) isEvent() {}

// ToolCallDelta is a partial segment of a JSON argument stream intended for a tool execution.
// The consumer must aggregate these deltas (by ID) until the tool call finishes.
type ToolCallDelta struct {
	ID             string
	Name           string
	ArgumentsDelta string
}

func (ToolCallDelta) isEvent() {}

// ToolCallFinished signals that a specific tool request has fully streamed its parameters.
// Once emitted, the executing layer can safely parse `Call.Arguments` and invoke the tool logic.
type ToolCallFinished struct {
	Call ToolCall
}

func (ToolCallFinished) isEvent() {}

// ResponseCompleted is definitively emitted when generation halts.
// This is the signal for consumers to drain any remaining buffers and inspect `Reason`
// to decide whether to prompt the user or handle a tool execution automatically.
type ResponseCompleted struct {
	Reason StopReason
}

func (ResponseCompleted) isEvent() {}

// ResponseError wraps unexpected infrastructure or execution errors emitted mid-stream.
// Encountering this typically indicates an abrupt halt and the stream will be closed.
type ResponseError struct {
	Err error
}

func (ResponseError) isEvent() {}
