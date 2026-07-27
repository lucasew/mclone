package openai

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/monitor"
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
				Choices: []ChunkChoice{{Delta: ChunkDelta{ToolCalls: []ToolCall{toOpenAIToolCall(ev.Call)}}}},
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
			monitor.ReportError(context.Background(), ev.Err, "msg", "openai_stream_error")
			// Generic message only — never leak backend err.Error() to clients.
			protocol.WriteSSEData(rw, map[string]any{
				"error": map[string]any{
					"message": "internal server error",
					"type":    "server_error",
				},
			})
			protocol.WriteSSERaw(rw, "[DONE]")
			return
		}
	}
	protocol.WriteSSERaw(rw, "[DONE]")
}

func (w *Writer) serveJSON(rw http.ResponseWriter, ch <-chan message.Event, model string) {
	var content strings.Builder
	var toolCalls []ToolCall
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
			toolCalls = append(toolCalls, toOpenAIToolCall(ev.Call))
		case message.ResponseCompleted:
			if ev.Reason == message.StopReasonToolCall {
				finishReason = "tool_calls"
			}
		case message.ResponseError:
			monitor.ReportError(context.Background(), ev.Err, "msg", "openai_json_error")
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusInternalServerError)
			protocol.WriteJSON(rw, map[string]any{
				"error": map[string]any{
					"message": "internal server error",
					"type":    "server_error",
				},
			})
			return
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

func toOpenAIToolCall(call message.ToolCall) ToolCall {
	id := call.ID
	if id == "" {
		id = fmt.Sprintf("call_%s", call.Name)
	}
	return ToolCall{
		ID:   id,
		Type: "function",
		Function: ToolCallFunction{
			Name:      call.Name,
			Arguments: string(call.Arguments),
		},
	}
}
