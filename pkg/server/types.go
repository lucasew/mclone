package server

import (
	"sync"

	"github.com/lucasew/mclone/pkg/message"
	anthropicprotocol "github.com/lucasew/mclone/pkg/protocol/anthropic"
	openaiprotocol "github.com/lucasew/mclone/pkg/protocol/openai"
	"github.com/lucasew/mclone/pkg/remote"
)

type Config struct {
	Provider           remote.Provider
	OverrideModel      string
	DefaultChatOptions message.ChatOptions
	SaveRawRequestPath string
	Verbose            bool
}

type Server struct {
	cfg             Config
	anthropicWriter *anthropicprotocol.Writer
	openaiWriter    *openaiprotocol.Writer
	responsesStore  sync.Map
}
