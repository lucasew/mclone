package message

// StopReason is the normalized completion reason.
type StopReason string

const (
	StopReasonEndTurn  StopReason = "end_turn"
	StopReasonToolCall StopReason = "tool_call"
)

// Event represents a single output event from a provider.
type Event interface {
	isEvent()
}

// TextDelta is streamed assistant-visible text.
type TextDelta struct {
	Text string
}

func (TextDelta) isEvent() {}

// ReasoningDelta is streamed internal reasoning emitted by some providers.
type ReasoningDelta struct {
	Text string
}

func (ReasoningDelta) isEvent() {}

// ToolCallDelta is a partial tool call update while streaming.
type ToolCallDelta struct {
	ID             string
	Name           string
	ArgumentsDelta string
}

func (ToolCallDelta) isEvent() {}

// ToolCallFinished is a completed tool call.
type ToolCallFinished struct {
	Call ToolCall
}

func (ToolCallFinished) isEvent() {}

// ResponseCompleted marks the end of a provider response.
type ResponseCompleted struct {
	Reason StopReason
}

func (ResponseCompleted) isEvent() {}

// ResponseError reports a provider error.
type ResponseError struct {
	Err error
}

func (ResponseError) isEvent() {}
