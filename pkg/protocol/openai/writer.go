package openai

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/protocol"
)

type Writer struct{}

func NewWriter() *Writer { return &Writer{} }

func (w *Writer) ServeResponse(ctx context.Context, rw http.ResponseWriter, ch <-chan message.Event, model string, stream bool) {
	if stream {
		w.serveStream(ctx, rw, ch, model)
	} else {
		w.serveJSON(ctx, rw, ch, model)
	}
}

func (w *Writer) serveStream(ctx context.Context, rw http.ResponseWriter, ch <-chan message.Event, model string) {
	protocol.SetStreamHeaders(rw)

	for event := range ch {
		switch ev := event.(type) {
		case message.TextDelta:
			protocol.WriteSSEData(ctx, rw, ChatCompletionChunk{
				ID:      "mclone",
				Object:  "chat.completion.chunk",
				Model:   model,
				Choices: []ChunkChoice{{Delta: ChunkDelta{Content: ev.Text}}},
			})
		case message.ReasoningDelta:
			protocol.WriteSSEData(ctx, rw, ChatCompletionChunk{
				ID:      "mclone",
				Object:  "chat.completion.chunk",
				Model:   model,
				Choices: []ChunkChoice{{Delta: ChunkDelta{Content: "<thought>" + ev.Text + "</thought>"}}},
			})
		case message.ToolCallFinished:
			protocol.WriteSSEData(ctx, rw, ChatCompletionChunk{
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
			protocol.WriteSSEData(ctx, rw, ChatCompletionChunk{
				ID:      "mclone",
				Object:  "chat.completion.chunk",
				Model:   model,
				Choices: []ChunkChoice{{Delta: ChunkDelta{}, FinishReason: &reason}},
			})
		case message.ResponseError:
			slog.Error("openai_stream_error", "error", ev.Err)
			// Generic message only — never leak backend err.Error() to clients.
			protocol.WriteSSEData(ctx, rw, map[string]any{
				"error": map[string]any{
					"message": "internal server error",
					"type":    "server_error",
				},
			})
			protocol.WriteSSERaw(ctx, rw, "[DONE]")
			return
		}
	}
	protocol.WriteSSERaw(ctx, rw, "[DONE]")
}

func (w *Writer) serveJSON(ctx context.Context, rw http.ResponseWriter, ch <-chan message.Event, model string) {
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
			slog.Error("openai_json_error", "error", ev.Err)
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusInternalServerError)
			protocol.WriteJSON(ctx, rw, map[string]any{
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
	protocol.WriteJSON(ctx, rw, resp)
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
