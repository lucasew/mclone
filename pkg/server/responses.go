package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/protocol"
)

type responsesRequest struct {
	Model              string          `json:"model"`
	Input              json.RawMessage `json:"input"`
	Instructions       json.RawMessage `json:"instructions,omitempty"`
	Tools              []protocol.Tool `json:"tools,omitempty"`
	PreviousResponseID string          `json:"previous_response_id,omitempty"`
	Stream             bool            `json:"stream"`
	Temperature        *float64        `json:"temperature,omitempty"`
	TopP               *float64        `json:"top_p,omitempty"`
	MaxOutputTokens    *int            `json:"max_output_tokens,omitempty"`
	Text               *struct {
		Format *struct {
			Type string `json:"type"`
		} `json:"format,omitempty"`
	} `json:"text,omitempty"`
}

type responsesOutputText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesOutputMessage struct {
	ID      string                `json:"id"`
	Type    string                `json:"type"`
	Role    string                `json:"role"`
	Status  string                `json:"status"`
	Content []responsesOutputText `json:"content"`
}

type responsesFunctionCall struct {
	ID        string `json:"id,omitempty"`
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Status    string `json:"status"`
}

type responsesResponse struct {
	ID         string `json:"id"`
	Object     string `json:"object"`
	CreatedAt  int64  `json:"created_at"`
	Model      string `json:"model,omitempty"`
	Status     string `json:"status"`
	OutputText string `json:"output_text,omitempty"`
	Output     []any  `json:"output"`
}

func (s *Server) serveResponsesRequest(w http.ResponseWriter, r *http.Request) {
	var req responsesRequest
	bodyReader, ok := s.bodyReader(r)
	if !ok {
		serveResponsesError(r.Context(), w, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	if err := json.NewDecoder(bodyReader).Decode(&req); err != nil {
		serveResponsesError(r.Context(), w, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}

	chatModel := req.Model
	if s.cfg.OverrideModel != "" {
		chatModel = s.cfg.OverrideModel
	}
	turns := parseResponsesTurns(req.Instructions, req.Input)
	opts := message.ChatOptions{Temperature: req.Temperature, TopP: req.TopP, MaxTokens: req.MaxOutputTokens}
	if len(req.Tools) > 0 {
		for _, t := range req.Tools {
			opts.Tools = append(opts.Tools, t.ToDefinition())
		}
	}
	if req.Text != nil && req.Text.Format != nil && req.Text.Format.Type == "json_schema" {
		opts.JSONMode = true
	}
	opts = opts.WithDefaults(s.cfg.DefaultChatOptions)

	respChan, err := s.cfg.Provider.Chat(r.Context(), message.Request{Model: chatModel, Turns: turns, Options: opts})
	if err != nil {
		if serveRateLimitError(r.Context(), w, responsesAPIWriter{}, err) {
			return
		}
		slog.Error("responses provider error", "err", err)
		serveResponsesError(r.Context(), w, http.StatusInternalServerError, "server_error", "internal server error")
		return
	}

	firstEvent, okFirst := <-respChan
	if okFirst {
		if ev, isErr := firstEvent.(message.ResponseError); isErr && serveRateLimitError(r.Context(), w, responsesAPIWriter{}, ev.Err) {
			return
		}
	}
	instrumented := make(chan message.Event)
	go func() {
		defer close(instrumented)
		if okFirst {
			instrumented <- firstEvent
		}
		for ev := range respChan {
			instrumented <- ev
		}
	}()
	if req.Stream {
		s.serveResponsesStream(r.Context(), w, instrumented, req.Model)
		return
	}
	s.serveResponsesJSON(r.Context(), w, instrumented, req.Model)
}

func (s *Server) serveResponsesJSON(ctx context.Context, w http.ResponseWriter, ch <-chan message.Event, model string) {
	var text strings.Builder
	var output []any
	for ev := range ch {
		switch v := ev.(type) {
		case message.TextDelta:
			text.WriteString(v.Text)
		case message.ToolCallFinished:
			callID := v.Call.ID
			if callID == "" {
				callID = fmt.Sprintf("call_%d", time.Now().UnixNano())
			}
			output = append(output, responsesFunctionCall{
				ID:        callID,
				Type:      "function_call",
				CallID:    callID,
				Name:      v.Call.Name,
				Arguments: string(v.Call.Arguments),
				Status:    "completed",
			})
		case message.ResponseError:
			slog.Error("responses stream error", "err", v.Err)
			serveResponsesError(ctx, w, http.StatusInternalServerError, "server_error", "internal server error")
			return
		}
	}
	if text.Len() > 0 {
		output = append([]any{responsesOutputMessage{
			ID:     fmt.Sprintf("msg_%d", time.Now().UnixNano()),
			Type:   "message",
			Role:   "assistant",
			Status: "completed",
			Content: []responsesOutputText{{
				Type: "output_text",
				Text: text.String(),
			}},
		}}, output...)
	}
	w.Header().Set("Content-Type", "application/json")
	resp := responsesResponse{
		ID:         fmt.Sprintf("resp_%d", time.Now().UnixNano()),
		Object:     "response",
		CreatedAt:  time.Now().Unix(),
		Model:      model,
		Status:     "completed",
		OutputText: text.String(),
		Output:     output,
	}
	s.responsesStore.Store(resp.ID, resp)
	protocol.WriteJSON(ctx, w, resp)
}

func (s *Server) serveResponsesStream(ctx context.Context, w http.ResponseWriter, ch <-chan message.Event, model string) {
	protocol.SetStreamHeaders(w)
	responseID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	messageID := fmt.Sprintf("msg_%d", time.Now().UnixNano())
	seq := 0
	messageOpened := false
	var text strings.Builder

	protocol.WriteSSE(ctx, w, "response.created", map[string]any{
		"type":            "response.created",
		"sequence_number": seq,
		"response": map[string]any{
			"id":         responseID,
			"object":     "response",
			"created_at": time.Now().Unix(),
			"model":      model,
			"status":     "in_progress",
			"output":     []any{},
		},
	})
	seq++

	for ev := range ch {
		switch v := ev.(type) {
		case message.TextDelta:
			if !messageOpened {
				messageOpened = true
				protocol.WriteSSE(ctx, w, "response.output_item.added", map[string]any{
					"type":            "response.output_item.added",
					"sequence_number": seq,
					"output_index":    0,
					"item": map[string]any{
						"id":      messageID,
						"type":    "message",
						"role":    "assistant",
						"status":  "in_progress",
						"content": []any{map[string]any{"type": "output_text", "text": ""}},
					},
				})
				seq++
			}
			text.WriteString(v.Text)
			protocol.WriteSSE(ctx, w, "response.output_text.delta", map[string]any{
				"type":            "response.output_text.delta",
				"sequence_number": seq,
				"output_index":    0,
				"content_index":   0,
				"item_id":         messageID,
				"delta":           v.Text,
				"logprobs":        []any{},
			})
			seq++
		case message.ToolCallFinished:
			callID := v.Call.ID
			if callID == "" {
				callID = fmt.Sprintf("call_%d", time.Now().UnixNano())
			}
			protocol.WriteSSE(ctx, w, "response.output_item.added", map[string]any{
				"type":            "response.output_item.added",
				"sequence_number": seq,
				"output_index":    len(text.String()),
				"item": map[string]any{
					"id":        callID,
					"type":      "function_call",
					"call_id":   callID,
					"name":      v.Call.Name,
					"arguments": "",
					"status":    "in_progress",
				},
			})
			seq++
			protocol.WriteSSE(ctx, w, "response.function_call_arguments.done", map[string]any{
				"type":            "response.function_call_arguments.done",
				"sequence_number": seq,
				"output_index":    len(text.String()),
				"item_id":         callID,
				"name":            v.Call.Name,
				"arguments":       string(v.Call.Arguments),
			})
			seq++
			protocol.WriteSSE(ctx, w, "response.output_item.done", map[string]any{
				"type":            "response.output_item.done",
				"sequence_number": seq,
				"output_index":    len(text.String()),
				"item": map[string]any{
					"id":        callID,
					"type":      "function_call",
					"call_id":   callID,
					"name":      v.Call.Name,
					"arguments": string(v.Call.Arguments),
					"status":    "completed",
				},
			})
			seq++
		case message.ResponseError:
			protocol.WriteSSE(ctx, w, "error", map[string]any{
				"type":            "error",
				"sequence_number": seq,
				"message":         v.Err.Error(),
				"code":            "server_error",
				"param":           nil,
			})
			return
		}
	}
	if messageOpened {
		protocol.WriteSSE(ctx, w, "response.output_text.done", map[string]any{
			"type":            "response.output_text.done",
			"sequence_number": seq,
			"output_index":    0,
			"content_index":   0,
			"item_id":         messageID,
			"text":            text.String(),
			"logprobs":        []any{},
		})
		seq++
		protocol.WriteSSE(ctx, w, "response.output_item.done", map[string]any{
			"type":            "response.output_item.done",
			"sequence_number": seq,
			"output_index":    0,
			"item": map[string]any{
				"id":      messageID,
				"type":    "message",
				"role":    "assistant",
				"status":  "completed",
				"content": []any{map[string]any{"type": "output_text", "text": text.String()}},
			},
		})
		seq++
	}
	protocol.WriteSSE(ctx, w, "response.completed", map[string]any{
		"type":            "response.completed",
		"sequence_number": seq,
		"response": map[string]any{
			"id":          responseID,
			"object":      "response",
			"created_at":  time.Now().Unix(),
			"model":       model,
			"status":      "completed",
			"output_text": text.String(),
		},
	})
	s.responsesStore.Store(responseID, responsesResponse{
		ID:         responseID,
		Object:     "response",
		CreatedAt:  time.Now().Unix(),
		Model:      model,
		Status:     "completed",
		OutputText: text.String(),
	})
}

func (s *Server) serveResponsesRetrieve(w http.ResponseWriter, r *http.Request) {
	responseID := strings.TrimPrefix(r.URL.Path, "/v1/responses/")
	if responseID == "" || responseID == r.URL.Path {
		serveResponsesError(r.Context(), w, http.StatusNotFound, "not_found_error", "response not found")
		return
	}
	value, ok := s.responsesStore.Load(responseID)
	if !ok {
		serveResponsesError(r.Context(), w, http.StatusNotFound, "not_found_error", "response not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	protocol.WriteJSON(r.Context(), w, value)
}

func parseResponsesTurns(instructions, input json.RawMessage) []message.Turn {
	var turns []message.Turn
	if sys := parseResponsesInstructionText(instructions); sys != "" {
		turns = append(turns, message.TextTurn(message.RoleSystem, sys))
	}
	if len(input) == 0 {
		return turns
	}
	switch protocol.FirstNonSpaceByte(input) {
	case '"':
		var s string
		if err := json.Unmarshal(input, &s); err == nil && s != "" {
			turns = append(turns, message.TextTurn(message.RoleUser, s))
		}
	case '[':
		var items []map[string]any
		if err := json.Unmarshal(input, &items); err == nil {
			for _, item := range items {
				if turn, ok := responseItemToTurn(item); ok {
					turns = append(turns, turn)
				}
			}
		}
	case '{':
		var item map[string]any
		if err := json.Unmarshal(input, &item); err == nil {
			if turn, ok := responseItemToTurn(item); ok {
				turns = append(turns, turn)
			}
		}
	}
	return turns
}

func parseResponsesInstructionText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}

func responseItemToTurn(item map[string]any) (message.Turn, bool) {
	itemType, _ := item["type"].(string)
	switch itemType {
	case "function_call_output":
		callID, _ := item["call_id"].(string)
		return message.Turn{Role: message.RoleTool, Parts: []message.Part{message.ToolResultPart{
			ToolCallID: callID,
			Content:    stringifyResponseContent(item["output"]),
		}}}, true
	case "function_call":
		name, _ := item["name"].(string)
		callID, _ := item["call_id"].(string)
		args := extractResponseArguments(item["arguments"])
		return message.Turn{Role: message.RoleAssistant, Parts: []message.Part{message.ToolCallPart{
			ID:        callID,
			Name:      name,
			Arguments: args,
		}}}, true
	default:
		role, _ := item["role"].(string)
		if role == "" {
			role = "user"
		}
		text := stringifyResponseContent(item["content"])
		if text == "" {
			return message.Turn{}, false
		}
		return message.TextTurn(normalizeResponseRole(role), text), true
	}
}

func stringifyResponseContent(v any) string {
	switch content := v.(type) {
	case string:
		return content
	case []any:
		var parts []string
		for _, item := range content {
			if m, ok := item.(map[string]any); ok {
				if text, _ := m["text"].(string); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "")
	default:
		return ""
	}
}

func extractResponseArguments(v any) json.RawMessage {
	switch args := v.(type) {
	case string:
		trimmed := strings.TrimSpace(args)
		if trimmed == "" {
			return json.RawMessage("{}")
		}
		if first := protocol.FirstNonSpaceByte([]byte(trimmed)); first == '{' || first == '[' {
			return json.RawMessage(trimmed)
		}
		b, err := json.Marshal(map[string]string{"input": args})
		if err == nil {
			return json.RawMessage(b)
		}
	case map[string]any, []any:
		b, err := json.Marshal(args)
		if err == nil {
			return json.RawMessage(b)
		}
	}
	return json.RawMessage("{}")
}

type responsesAPIWriter struct{}

func (responsesAPIWriter) ServeResponse(context.Context, http.ResponseWriter, <-chan message.Event, string, bool) {
}

func serveResponsesError(ctx context.Context, w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	protocol.WriteJSON(ctx, w, map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    code,
			"message": msg,
		},
	})
}

func normalizeResponseRole(role string) message.Role {
	switch role {
	case "assistant":
		return message.RoleAssistant
	case "system", "developer":
		return message.RoleSystem
	default:
		return message.RoleUser
	}
}
