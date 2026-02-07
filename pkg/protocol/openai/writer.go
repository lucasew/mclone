package openai

import (
	"log/slog"
	"net/http"
	"strings"

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

	for resp := range ch {
		if resp.Error != nil {
			slog.Error("openai_stream_error", "error", resp.Error)
			continue
		}
		chunk := ChatCompletionChunk{
			ID:     "mclone",
			Object: "chat.completion.chunk",
			Model:  model,
			Choices: []ChunkChoice{{
				Delta: ChunkDelta{Content: resp.Content, ToolCalls: resp.ToolCalls},
			}},
		}
		if resp.Done {
			reason := "stop"
			if len(resp.ToolCalls) > 0 {
				reason = "tool_calls"
			}
			chunk.Choices[0].FinishReason = &reason
		}
		protocol.WriteSSEData(rw, chunk)
	}
	protocol.WriteSSERaw(rw, "[DONE]")
}

func (w *Writer) serveJSON(rw http.ResponseWriter, ch <-chan message.ChatResponse, model string) {
	var content strings.Builder
	var toolCalls []message.ToolCall
	for resp := range ch {
		if resp.Error != nil {
			slog.Error("openai_json_error", "error", resp.Error)
			continue
		}
		content.WriteString(resp.Content)
		toolCalls = append(toolCalls, resp.ToolCalls...)
	}

	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}

	resp := ChatCompletionResponse{
		ID:     "mclone",
		Object: "chat.completion",
		Model:  model,
		Choices: []Choice{{
			Message: Message{
				Role:      "assistant",
				Content:   content.String(),
				ToolCalls: toolCalls,
			},
			FinishReason: finishReason,
		}},
	}

	rw.Header().Set("Content-Type", "application/json")
	protocol.WriteJSON(rw, resp)
}
