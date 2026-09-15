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

func (w *Writer) ServeResponse(rw http.ResponseWriter, ch <-chan message.Event, model string, stream bool) {
	if stream {
		w.serveStream(rw, ch, model)
	} else {
		w.serveJSON(rw, ch, model)
	}
}

func (w *Writer) serveStream(rw http.ResponseWriter, ch <-chan message.Event, model string) {
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

	for event := range ch {
		switch ev := event.(type) {
		case message.ResponseError:
			slog.Error("anthropic_stream_error", "error", ev.Err)
			// Generic message only — never leak backend err.Error() to clients.
			protocol.WriteSSE(rw, "error", map[string]any{
				"type": "error",
				"error": map[string]any{
					"type":    "api_error",
					"message": "internal server error",
				},
			})
			return
		case message.ReasoningDelta:
			protocol.WriteSSE(rw, "content_block_start", ContentBlockStartEvent{
				Type:         "content_block_start",
				Index:        contentIndex,
				ContentBlock: ContentBlock{Type: "text", Text: ""},
			})
			protocol.WriteSSE(rw, "content_block_delta", ContentBlockDeltaEvent{
				Type:  "content_block_delta",
				Index: contentIndex,
				Delta: BlockDelta{Type: "text_delta", Text: "<thought>" + ev.Text + "</thought>"},
			})
			protocol.WriteSSE(rw, "content_block_stop", ContentBlockStopEvent{
				Type: "content_block_stop", Index: contentIndex,
			})
			contentIndex++
		case message.TextDelta:
			protocol.WriteSSE(rw, "content_block_delta", ContentBlockDeltaEvent{
				Type:  "content_block_delta",
				Index: contentIndex,
				Delta: BlockDelta{Type: "text_delta", Text: ev.Text},
			})
		case message.ToolCallFinished:
			hasCalledTool = true

			protocol.WriteSSE(rw, "content_block_stop", ContentBlockStopEvent{
				Type: "content_block_stop", Index: contentIndex,
			})
			contentIndex++

			id := ev.Call.ID
			if id == "" {
				id = fmt.Sprintf("toolu_%d", time.Now().UnixNano())
			}

			slog.Info("sending_tool_use", "name", ev.Call.Name, "id", id)
			protocol.WriteSSE(rw, "content_block_start", ContentBlockStartEvent{
				Type:  "content_block_start",
				Index: contentIndex,
				ContentBlock: ContentBlock{
					Type: "tool_use", ID: id, Name: ev.Call.Name, Input: []byte("{}"),
				},
			})
			protocol.WriteSSE(rw, "content_block_delta", ContentBlockDeltaEvent{
				Type:  "content_block_delta",
				Index: contentIndex,
				Delta: BlockDelta{Type: "input_json_delta", PartialJSON: string(ev.Call.Arguments)},
			})
			protocol.WriteSSE(rw, "content_block_stop", ContentBlockStopEvent{
				Type: "content_block_stop", Index: contentIndex,
			})
			contentIndex++
		case message.ResponseCompleted:
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

func (w *Writer) serveJSON(rw http.ResponseWriter, ch <-chan message.Event, model string) {
	var content strings.Builder
	var toolCalls []message.ToolCall
	for event := range ch {
		switch ev := event.(type) {
		case message.TextDelta:
			content.WriteString(ev.Text)
		case message.ToolCallFinished:
			toolCalls = append(toolCalls, ev.Call)
		case message.ResponseError:
			slog.Error("anthropic_json_error", "error", ev.Err)
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusInternalServerError)
			protocol.WriteJSON(rw, map[string]any{
				"type": "error",
				"error": map[string]any{
					"type":    "api_error",
					"message": "internal server error",
				},
			})
			return
		}
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
