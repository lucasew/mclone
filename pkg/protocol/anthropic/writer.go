package anthropic

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/protocol"
)

type Writer struct{}

func NewWriter() *Writer { return &Writer{} }

func (w *Writer) ServeResponse(rw http.ResponseWriter, ch <-chan message.ChatResponse, model string, stream bool) {
	if stream {
		w.serveStream(rw, ch, model)
	} else {
		w.serveJSON(rw, ch, model)
	}
}

func (w *Writer) serveStream(rw http.ResponseWriter, ch <-chan message.ChatResponse, model string) {
	protocol.SetStreamHeaders(rw)

	contentIndex := 0
	hasCalledTool := false

	protocol.WriteSSE(rw, "message_start", MessageStartEvent{
		Type: "message_start",
		Message: MessageResponse{
			ID: "mclone", Type: "message", Role: "assistant", Model: model,
			Content: []ContentBlock{}, StopSequence: nil,
			Usage: Usage{},
		},
	})

	protocol.WriteSSE(rw, "content_block_start", ContentBlockStartEvent{
		Type:         "content_block_start",
		Index:        contentIndex,
		ContentBlock: ContentBlock{Type: "text", Text: ""},
	})

	for resp := range ch {
		if resp.Error != nil {
			slog.Error("anthropic_stream_error", "error", resp.Error)
			continue
		}
		if resp.Thought != "" {
			protocol.WriteSSE(rw, "content_block_start", ContentBlockStartEvent{
				Type:         "content_block_start",
				Index:        contentIndex,
				ContentBlock: ContentBlock{Type: "text", Text: ""},
			})
			protocol.WriteSSE(rw, "content_block_delta", ContentBlockDeltaEvent{
				Type:  "content_block_delta",
				Index: contentIndex,
				Delta: BlockDelta{Type: "text_delta", Text: "<thought>" + resp.Thought + "</thought>"},
			})
			protocol.WriteSSE(rw, "content_block_stop", ContentBlockStopEvent{
				Type: "content_block_stop", Index: contentIndex,
			})
			contentIndex++
		}
		if resp.Content != "" {
			protocol.WriteSSE(rw, "content_block_delta", ContentBlockDeltaEvent{
				Type:  "content_block_delta",
				Index: contentIndex,
				Delta: BlockDelta{Type: "text_delta", Text: resp.Content},
			})
		}

		for _, tc := range resp.ToolCalls {
			hasCalledTool = true

			protocol.WriteSSE(rw, "content_block_stop", ContentBlockStopEvent{
				Type: "content_block_stop", Index: contentIndex,
			})
			contentIndex++

			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("toolu_%d", time.Now().UnixNano())
			}

			slog.Info("sending_tool_use", "name", tc.Name, "id", id)
			protocol.WriteSSE(rw, "content_block_start", ContentBlockStartEvent{
				Type:  "content_block_start",
				Index: contentIndex,
				ContentBlock: ContentBlock{
					Type: "tool_use", ID: id, Name: tc.Name, Input: tc.Arguments,
				},
			})
			protocol.WriteSSE(rw, "content_block_stop", ContentBlockStopEvent{
				Type: "content_block_stop", Index: contentIndex,
			})
			contentIndex++
		}
	}

	protocol.WriteSSE(rw, "content_block_stop", ContentBlockStopEvent{
		Type: "content_block_stop", Index: contentIndex,
	})

	stopReason := "end_turn"
	if hasCalledTool {
		stopReason = "tool_use"
	}

	protocol.WriteSSE(rw, "message_delta", MessageDeltaEvent{
		Type:  "message_delta",
		Delta: MessageDelta{StopReason: stopReason},
		Usage: Usage{},
	})
	protocol.WriteSSE(rw, "message_stop", MessageStopEvent{Type: "message_stop"})
}

func (w *Writer) serveJSON(rw http.ResponseWriter, ch <-chan message.ChatResponse, model string) {
	var content strings.Builder
	var toolCalls []message.ToolCall
	for resp := range ch {
		if resp.Error != nil {
			slog.Error("anthropic_json_error", "error", resp.Error)
			continue
		}
		content.WriteString(resp.Content)
		toolCalls = append(toolCalls, resp.ToolCalls...)
	}

	resp := MessageResponse{
		ID: "mclone", Type: "message", Role: "assistant", Model: model,
		Content: []ContentBlock{}, StopReason: "end_turn",
	}

	if content.Len() > 0 {
		resp.Content = append(resp.Content, ContentBlock{Type: "text", Text: content.String()})
	}

	for _, tc := range toolCalls {
		resp.StopReason = "tool_use"
		id := tc.ID
		if id == "" {
			id = fmt.Sprintf("toolu_%d", time.Now().UnixNano())
		}
		resp.Content = append(resp.Content, ContentBlock{
			Type: "tool_use", ID: id, Name: tc.Name,
			Input: tc.Arguments,
		})
	}

	rw.Header().Set("Content-Type", "application/json")
	protocol.WriteJSON(rw, resp)
}
