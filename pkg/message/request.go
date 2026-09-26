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
