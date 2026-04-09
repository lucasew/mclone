package server

import (
	"sync"

	"github.com/lucasew/mclone/pkg/message"
	anthropicprotocol "github.com/lucasew/mclone/pkg/protocol/anthropic"
	openaiprotocol "github.com/lucasew/mclone/pkg/protocol/openai"
	"github.com/lucasew/mclone/pkg/remote"
)

// Config holds the runtime configuration for the API server.
// It maps the configured remote provider and determines fallback behaviors
// such as default chat options and model overrides.
type Config struct {
	Provider           remote.Provider
	OverrideModel      string
	DefaultChatOptions message.ChatOptions
	SaveRawRequestPath string
	Verbose            bool
}

// Server handles incoming HTTP requests and routes them to the appropriate protocol writers.
// It maintains state across requests, such as the underlying writers for Anthropic and OpenAI
// protocols, and an in-memory store for response tracking.
type Server struct {
	cfg             Config
	anthropicWriter *anthropicprotocol.Writer
	openaiWriter    *openaiprotocol.Writer
	responsesStore  sync.Map
}
