package message

// Turn represents a normalized conversation turn.
type Turn struct {
	Role  Role
	Parts []Part
}

// Request is the provider-facing chat request.
type Request struct {
	Model   string
	Turns   []Turn
	Options ChatOptions
}

// Messages converts legacy messages into turns.
func Messages(messages []Message) []Turn {
	turns := make([]Turn, len(messages))
	for i, msg := range messages {
		turns[i] = Turn(msg)
	}
	return turns
}

// LegacyMessages converts turns back into legacy messages.
func LegacyMessages(turns []Turn) []Message {
	messages := make([]Message, len(turns))
	for i, turn := range turns {
		messages[i] = Message(turn)
	}
	return messages
}
