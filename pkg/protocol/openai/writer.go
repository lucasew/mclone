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

func (w *Writer) ServeResponse(rw http.ResponseWriter, ch <-chan message.Event, model string, stream bool) {
	if stream {
		w.serveStream(rw, ch, model)
	} else {
		w.serveJSON(rw, ch, model)
	}
}

func (w *Writer) serveStream(rw http.ResponseWriter, ch <-chan message.Event, model string) {
	protocol.SetStreamHeaders(rw)

	for event := range ch {
		switch ev := event.(type) {
		case message.TextDelta:
			protocol.WriteSSEData(rw, ChatCompletionChunk{
				ID:      "mclone",
				Object:  "chat.completion.chunk",
				Model:   model,
				Choices: []ChunkChoice{{Delta: ChunkDelta{Content: ev.Text}}},
			})
		case message.ReasoningDelta:
			protocol.WriteSSEData(rw, ChatCompletionChunk{
				ID:      "mclone",
				Object:  "chat.completion.chunk",
				Model:   model,
				Choices: []ChunkChoice{{Delta: ChunkDelta{Content: "<thought>" + ev.Text + "</thought>"}}},
			})
		case message.ToolCallFinished:
			protocol.WriteSSEData(rw, ChatCompletionChunk{
				ID:      "mclone",
				Object:  "chat.completion.chunk",
				Model:   model,
				Choices: []ChunkChoice{{Delta: ChunkDelta{ToolCalls: []message.ToolCall{ev.Call}}}},
			})
		case message.ResponseCompleted:
			reason := "stop"
			if ev.Reason == message.StopReasonToolCall {
				reason = "tool_calls"
			}
			protocol.WriteSSEData(rw, ChatCompletionChunk{
				ID:      "mclone",
				Object:  "chat.completion.chunk",
				Model:   model,
				Choices: []ChunkChoice{{Delta: ChunkDelta{}, FinishReason: &reason}},
			})
		case message.ResponseError:
			slog.Error("openai_stream_error", "error", ev.Err)
		}
	}
	protocol.WriteSSERaw(rw, "[DONE]")
}

func (w *Writer) serveJSON(rw http.ResponseWriter, ch <-chan message.Event, model string) {
	var content strings.Builder
	var toolCalls []message.ToolCall
	finishReason := "stop"
	for event := range ch {
		switch ev := event.(type) {
		case message.TextDelta:
			content.WriteString(ev.Text)
		case message.ReasoningDelta:
			content.WriteString("<thought>")
			content.WriteString(ev.Text)
			content.WriteString("</thought>")
		case message.ToolCallFinished:
			toolCalls = append(toolCalls, ev.Call)
		case message.ResponseCompleted:
			if ev.Reason == message.StopReasonToolCall {
				finishReason = "tool_calls"
			}
		case message.ResponseError:
			slog.Error("openai_json_error", "error", ev.Err)
		}
	}

	resp := ChatCompletionResponse{
		ID:     "mclone",
		Object: "chat.completion",
		Model:  model,
		Choices: []Choice{{
			Message: Message{
				Role:    "assistant",
				Content: content.String(),
			},
			FinishReason: finishReason,
		}},
	}

	resp.Choices[0].Message.ToolCalls = append(resp.Choices[0].Message.ToolCalls, toolCalls...)

	rw.Header().Set("Content-Type", "application/json")
	protocol.WriteJSON(rw, resp)
}
