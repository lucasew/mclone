package server

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/goccy/go-json"
	"github.com/klauspost/compress/zstd"
	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/monitor"
	"github.com/lucasew/mclone/pkg/protocol"
	anthropicprotocol "github.com/lucasew/mclone/pkg/protocol/anthropic"
	openaiprotocol "github.com/lucasew/mclone/pkg/protocol/openai"
	"github.com/lucasew/mclone/pkg/remote"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
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

func New(cfg Config) *Server {
	return &Server{
		cfg:             cfg,
		anthropicWriter: anthropicprotocol.NewWriter(),
		openaiWriter:    openaiprotocol.NewWriter(),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		s.serveChatRequest(w, r, s.anthropicWriter)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		s.serveChatRequest(w, r, s.openaiWriter)
	})
	mux.HandleFunc("/v1/responses", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			s.serveResponsesRequest(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/v1/responses/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.serveResponsesRetrieve(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		s.serveModels(w, r)
	})

	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("request", "method", r.Method, "path", r.URL.Path)
		mux.ServeHTTP(w, r)
	})
	return requestDecompressionMiddleware(handler)
}

func (s *Server) ListenAndServe(addr string) error {
	return http.ListenAndServe(addr, s.Handler())
}

func SetupTracing(verbose bool) func(context.Context) error {
	if !verbose {
		return func(context.Context) error { return nil }
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(stderrSpanExporter{})),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown
}

type chatRequest struct {
	Model        string                     `json:"model"`
	Messages     []protocol.IncomingMessage `json:"messages"`
	Tools        []protocol.Tool            `json:"tools,omitempty"`
	System       any                        `json:"system,omitempty"`
	Stream       bool                       `json:"stream"`
	OutputConfig *struct {
		Format struct {
			Type string `json:"type"`
		} `json:"format"`
	} `json:"output_config,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"top_p,omitempty"`
	MaxTokens     *int     `json:"max_tokens,omitempty"`
	Stop          any      `json:"stop,omitempty"`
	StopSequences []string `json:"stop_sequences,omitempty"`
}

func (s *Server) serveChatRequest(w http.ResponseWriter, r *http.Request, writer protocol.Writer) {
	ctx, reqSpan := otel.Tracer("mclone/serve").Start(r.Context(), "serve.chat_request",
		trace.WithAttributes(
			attribute.String("http.method", r.Method),
			attribute.String("http.route", r.URL.Path),
			attribute.String("provider.name", s.cfg.Provider.Name()),
		),
	)
	defer reqSpan.End()
	r = r.WithContext(ctx)

	var req chatRequest
	bodyReader, ok := s.bodyReader(w, r)
	if !ok {
		return
	}
	if err := json.NewDecoder(bodyReader).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	responseModel := req.Model
	chatModel := req.Model
	if s.cfg.OverrideModel != "" {
		chatModel = s.cfg.OverrideModel
	}
	reqSpan.SetAttributes(
		attribute.String("model.requested", req.Model),
		attribute.String("model.actual", chatModel),
		attribute.Bool("response.stream", req.Stream),
	)

	slog.Info("incoming request", "req_model", req.Model, "chat_model", chatModel)
	turns := parseMessages(r.Context(), req)

	opts := message.ChatOptions{}
	if len(req.Tools) > 0 {
		for _, t := range req.Tools {
			opts.Tools = append(opts.Tools, t.ToDefinition())
		}
		slog.Info("tools_configured", "count", len(opts.Tools))
	}
	if req.OutputConfig != nil && req.OutputConfig.Format.Type == "json_schema" {
		opts.JSONMode = true
	}
	opts.Temperature = req.Temperature
	opts.TopP = req.TopP
	opts.MaxTokens = req.MaxTokens
	opts.Stop = mergeStop(req.Stop, req.StopSequences)
	opts = opts.WithDefaults(s.cfg.DefaultChatOptions)

	providerCtx, providerSpan := otel.Tracer("mclone/provider").Start(r.Context(), "provider.chat",
		trace.WithAttributes(
			attribute.String("provider.name", s.cfg.Provider.Name()),
			attribute.String("model.requested", req.Model),
			attribute.String("model.actual", chatModel),
		),
	)
	respChan, err := s.cfg.Provider.Chat(providerCtx, message.Request{
		Model:   chatModel,
		Turns:   turns,
		Options: opts,
	})
	if err != nil {
		providerSpan.RecordError(err)
		providerSpan.SetStatus(codes.Error, err.Error())
		providerSpan.End()
		reqSpan.RecordError(err)
		reqSpan.SetStatus(codes.Error, err.Error())
		monitor.ReportError(r.Context(), err, "action", "chat_failed")
		if serveRateLimitError(w, writer, err) {
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	startedAt := time.Now()
	firstEventStartedAt := startedAt
	firstEvent, okFirst := <-respChan
	if okFirst {
		elapsed := time.Since(firstEventStartedAt)
		providerSpan.SetAttributes(attribute.Int64("stream.first_event_ms", elapsed.Milliseconds()))
		providerSpan.AddEvent("first_event")
		slog.Debug("provider_first_event", "provider", s.cfg.Provider.Name(), "model", chatModel, "elapsed", elapsed)
		if ev, isErr := firstEvent.(message.ResponseError); isErr && serveRateLimitError(w, writer, ev.Err) {
			providerSpan.RecordError(ev.Err)
			providerSpan.SetStatus(codes.Error, ev.Err.Error())
			providerSpan.SetAttributes(attribute.Int("stream.event_count", 1), attribute.Int64("stream.duration_ms", elapsed.Milliseconds()))
			providerSpan.End()
			reqSpan.RecordError(ev.Err)
			reqSpan.SetStatus(codes.Error, ev.Err.Error())
			slog.Debug("provider_chat_finished", "provider", s.cfg.Provider.Name(), "model", chatModel, "elapsed", elapsed, "events", 1)
			return
		}
	}

	loggedFirstEvent := false
	instrumented := make(chan message.Event)
	go func() {
		defer close(instrumented)
		eventCount := 0
		var finalErr error
		var finishReason string
		if okFirst {
			eventCount++
			loggedFirstEvent = true
			switch tev := firstEvent.(type) {
			case message.ResponseCompleted:
				finishReason = string(tev.Reason)
			case message.ResponseError:
				finalErr = tev.Err
			}
			instrumented <- firstEvent
		}
		for ev := range respChan {
			eventCount++
			if !loggedFirstEvent {
				loggedFirstEvent = true
				elapsed := time.Since(startedAt)
				providerSpan.SetAttributes(attribute.Int64("stream.first_event_ms", elapsed.Milliseconds()))
				providerSpan.AddEvent("first_event")
				slog.Debug("provider_first_event", "provider", s.cfg.Provider.Name(), "model", chatModel, "elapsed", elapsed)
			}
			switch tev := ev.(type) {
			case message.ResponseCompleted:
				finishReason = string(tev.Reason)
			case message.ResponseError:
				finalErr = tev.Err
			}
			instrumented <- ev
		}
		providerSpan.SetAttributes(attribute.Int("stream.event_count", eventCount), attribute.Int64("stream.duration_ms", time.Since(startedAt).Milliseconds()))
		if finishReason != "" {
			providerSpan.SetAttributes(attribute.String("stream.finish_reason", finishReason))
		}
		if finalErr != nil {
			providerSpan.RecordError(finalErr)
			providerSpan.SetStatus(codes.Error, finalErr.Error())
			reqSpan.RecordError(finalErr)
			reqSpan.SetStatus(codes.Error, finalErr.Error())
		} else {
			providerSpan.SetStatus(codes.Ok, "")
		}
		providerSpan.End()
		slog.Debug("provider_chat_finished", "provider", s.cfg.Provider.Name(), "model", chatModel, "elapsed", time.Since(startedAt), "events", eventCount)
	}()

	writer.ServeResponse(w, instrumented, responseModel, req.Stream)
}

func (s *Server) bodyReader(w http.ResponseWriter, r *http.Request) (io.Reader, bool) {
	var bodyReader io.Reader = r.Body
	if s.cfg.SaveRawRequestPath != "" {
		body, _ := io.ReadAll(r.Body)
		err := os.WriteFile(s.cfg.SaveRawRequestPath, body, 0644)
		if err != nil {
			monitor.ReportError(r.Context(), err, "action", "save_raw_request", "path", s.cfg.SaveRawRequestPath)
		} else {
			slog.Debug("saved raw request", "path", s.cfg.SaveRawRequestPath)
		}
		bodyReader = bytes.NewReader(body)
	}
	return bodyReader, true
}

func serveRateLimitError(w http.ResponseWriter, writer protocol.Writer, err error) bool {
	var rl *message.ErrRateLimit
	if !errors.As(err, &rl) {
		return false
	}
	setRateLimitHeaders(w, writer, rl.RetryAfter)
	w.WriteHeader(http.StatusTooManyRequests)
	switch writer.(type) {
	case *openaiprotocol.Writer:
		protocol.WriteJSON(w, map[string]any{"error": map[string]any{"message": err.Error(), "type": "rate_limit_error", "code": "rate_limit_exceeded"}})
	case *anthropicprotocol.Writer:
		protocol.WriteJSON(w, map[string]any{"type": "error", "error": map[string]any{"type": "rate_limit_error", "message": err.Error()}})
	case responsesAPIWriter:
		protocol.WriteJSON(w, map[string]any{"type": "error", "error": map[string]any{"type": "rate_limit_error", "message": err.Error()}})
	default:
		protocol.WriteJSON(w, map[string]any{"error": err.Error()})
	}
	return true
}

func setRateLimitHeaders(w http.ResponseWriter, writer protocol.Writer, retryAfter time.Duration) {
	if retryAfter <= 0 {
		return
	}
	seconds := int64((retryAfter + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	switch writer.(type) {
	case *openaiprotocol.Writer, responsesAPIWriter:
		reset := (time.Duration(seconds) * time.Second).String()
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		w.Header().Set("x-ratelimit-reset-requests", reset)
		w.Header().Set("x-ratelimit-reset-tokens", reset)
	case *anthropicprotocol.Writer:
		resetAt := time.Now().Add(time.Duration(seconds) * time.Second).UTC().Format(time.RFC3339)
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		w.Header().Set("anthropic-ratelimit-requests-reset", resetAt)
		w.Header().Set("anthropic-ratelimit-tokens-reset", resetAt)
	default:
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	}
}

type stderrSpanExporter struct{}

func (stderrSpanExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	for _, span := range spans {
		args := []any{"span", span.Name(), "trace_id", span.SpanContext().TraceID().String(), "span_id", span.SpanContext().SpanID().String(), "parent_span_id", span.Parent().SpanID().String(), "duration_ms", span.EndTime().Sub(span.StartTime()).Milliseconds(), "status", span.Status().Code.String()}
		if msg := span.Status().Description; msg != "" {
			args = append(args, "status_message", msg)
		}
		for _, attr := range span.Attributes() {
			args = append(args, string(attr.Key), attr.Value.Emit())
		}
		slog.Debug("otel_span", args...)
	}
	return nil
}

func (stderrSpanExporter) Shutdown(context.Context) error { return nil }

func parseMessages(ctx context.Context, req chatRequest) []message.Turn {
	var msgs []message.Turn
	if req.System != nil {
		systemText := ""
		switch v := req.System.(type) {
		case string:
			systemText = v
		case []any:
			var parts []string
			for _, p := range v {
				if pm, ok := p.(map[string]any); ok {
					if txt, ok := pm["text"].(string); ok {
						parts = append(parts, txt)
					}
				}
			}
			systemText = strings.Join(parts, "\n")
		}
		if systemText != "" {
			msgs = append(msgs, message.TextTurn(message.RoleSystem, systemText))
		}
	}
	for i, m := range req.Messages {
		msg, err := m.ToTurn()
		if err != nil {
			monitor.ReportError(ctx, err, "action", "convert_message", "index", i)
			continue
		}
		msgs = append(msgs, msg)
	}
	return msgs
}

func (s *Server) serveModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.cfg.Provider.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type modelEntry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	resp := struct {
		Object string       `json:"object"`
		Data   []modelEntry `json:"data"`
	}{Object: "list", Data: []modelEntry{}}
	for _, m := range models {
		resp.Data = append(resp.Data, modelEntry{ID: m.Slug, Object: "model", Created: 1677610602, OwnedBy: "mclone"})
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		monitor.ReportError(r.Context(), err, "action", "serve_models_encode_error")
	}
}

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
	bodyReader, ok := s.bodyReader(w, r)
	if !ok {
		return
	}
	if err := json.NewDecoder(bodyReader).Decode(&req); err != nil {
		serveResponsesError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
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
		if serveRateLimitError(w, responsesAPIWriter{}, err) {
			return
		}
		serveResponsesError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	firstEvent, okFirst := <-respChan
	if okFirst {
		if ev, isErr := firstEvent.(message.ResponseError); isErr && serveRateLimitError(w, responsesAPIWriter{}, ev.Err) {
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
		s.serveResponsesStream(w, instrumented, req.Model)
		return
	}
	s.serveResponsesJSON(w, instrumented, req.Model)
}

func (s *Server) serveResponsesJSON(w http.ResponseWriter, ch <-chan message.Event, model string) {
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
			output = append(output, responsesFunctionCall{ID: callID, Type: "function_call", CallID: callID, Name: v.Call.Name, Arguments: string(v.Call.Arguments), Status: "completed"})
		case message.ResponseError:
			serveResponsesError(w, http.StatusInternalServerError, "server_error", v.Err.Error())
			return
		}
	}
	if text.Len() > 0 {
		output = append([]any{responsesOutputMessage{ID: fmt.Sprintf("msg_%d", time.Now().UnixNano()), Type: "message", Role: "assistant", Status: "completed", Content: []responsesOutputText{{Type: "output_text", Text: text.String()}}}}, output...)
	}
	w.Header().Set("Content-Type", "application/json")
	resp := responsesResponse{ID: fmt.Sprintf("resp_%d", time.Now().UnixNano()), Object: "response", CreatedAt: time.Now().Unix(), Model: model, Status: "completed", OutputText: text.String(), Output: output}
	s.responsesStore.Store(resp.ID, resp)
	protocol.WriteJSON(w, resp)
}

func (s *Server) serveResponsesStream(w http.ResponseWriter, ch <-chan message.Event, model string) {
	protocol.SetStreamHeaders(w)
	responseID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	messageID := fmt.Sprintf("msg_%d", time.Now().UnixNano())
	seq := 0
	messageOpened := false
	var text strings.Builder
	protocol.WriteSSE(w, "response.created", map[string]any{"type": "response.created", "sequence_number": seq, "response": map[string]any{"id": responseID, "object": "response", "created_at": time.Now().Unix(), "model": model, "status": "in_progress", "output": []any{}}})
	seq++
	for ev := range ch {
		switch v := ev.(type) {
		case message.TextDelta:
			if !messageOpened {
				messageOpened = true
				protocol.WriteSSE(w, "response.output_item.added", map[string]any{"type": "response.output_item.added", "sequence_number": seq, "output_index": 0, "item": map[string]any{"id": messageID, "type": "message", "role": "assistant", "status": "in_progress", "content": []any{map[string]any{"type": "output_text", "text": ""}}}})
				seq++
			}
			text.WriteString(v.Text)
			protocol.WriteSSE(w, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "sequence_number": seq, "output_index": 0, "content_index": 0, "item_id": messageID, "delta": v.Text, "logprobs": []any{}})
			seq++
		case message.ToolCallFinished:
			callID := v.Call.ID
			if callID == "" {
				callID = fmt.Sprintf("call_%d", time.Now().UnixNano())
			}
			protocol.WriteSSE(w, "response.output_item.added", map[string]any{"type": "response.output_item.added", "sequence_number": seq, "output_index": len(text.String()), "item": map[string]any{"id": callID, "type": "function_call", "call_id": callID, "name": v.Call.Name, "arguments": "", "status": "in_progress"}})
			seq++
			protocol.WriteSSE(w, "response.function_call_arguments.done", map[string]any{"type": "response.function_call_arguments.done", "sequence_number": seq, "output_index": len(text.String()), "item_id": callID, "name": v.Call.Name, "arguments": string(v.Call.Arguments)})
			seq++
			protocol.WriteSSE(w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "sequence_number": seq, "output_index": len(text.String()), "item": map[string]any{"id": callID, "type": "function_call", "call_id": callID, "name": v.Call.Name, "arguments": string(v.Call.Arguments), "status": "completed"}})
			seq++
		case message.ResponseError:
			protocol.WriteSSE(w, "error", map[string]any{"type": "error", "sequence_number": seq, "message": v.Err.Error(), "code": "server_error", "param": nil})
			return
		}
	}
	if messageOpened {
		protocol.WriteSSE(w, "response.output_text.done", map[string]any{"type": "response.output_text.done", "sequence_number": seq, "output_index": 0, "content_index": 0, "item_id": messageID, "text": text.String(), "logprobs": []any{}})
		seq++
		protocol.WriteSSE(w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "sequence_number": seq, "output_index": 0, "item": map[string]any{"id": messageID, "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text.String()}}}})
		seq++
	}
	protocol.WriteSSE(w, "response.completed", map[string]any{"type": "response.completed", "sequence_number": seq, "response": map[string]any{"id": responseID, "object": "response", "created_at": time.Now().Unix(), "model": model, "status": "completed", "output_text": text.String()}})
	s.responsesStore.Store(responseID, responsesResponse{ID: responseID, Object: "response", CreatedAt: time.Now().Unix(), Model: model, Status: "completed", OutputText: text.String()})
}

func (s *Server) serveResponsesRetrieve(w http.ResponseWriter, r *http.Request) {
	responseID := strings.TrimPrefix(r.URL.Path, "/v1/responses/")
	if responseID == "" || responseID == r.URL.Path {
		serveResponsesError(w, http.StatusNotFound, "not_found_error", "response not found")
		return
	}
	value, ok := s.responsesStore.Load(responseID)
	if !ok {
		serveResponsesError(w, http.StatusNotFound, "not_found_error", "response not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	protocol.WriteJSON(w, value)
}

func parseResponsesTurns(instructions, input json.RawMessage) []message.Turn {
	var turns []message.Turn
	if sys := parseResponsesInstructionText(instructions); sys != "" {
		turns = append(turns, message.TextTurn(message.RoleSystem, sys))
	}
	if len(input) == 0 {
		return turns
	}
	switch firstNonSpaceByte(input) {
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
		return message.Turn{Role: message.RoleTool, Parts: []message.Part{message.ToolResultPart{ToolCallID: callID, Content: stringifyResponseContent(item["output"])}}}, true
	case "function_call":
		name, _ := item["name"].(string)
		callID, _ := item["call_id"].(string)
		args := extractResponseArguments(item["arguments"])
		return message.Turn{Role: message.RoleAssistant, Parts: []message.Part{message.ToolCallPart{ID: callID, Name: name, Arguments: args}}}, true
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
		if first := firstNonSpaceByte([]byte(trimmed)); first == '{' || first == '[' {
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

func (responsesAPIWriter) ServeResponse(http.ResponseWriter, <-chan message.Event, string, bool) {}

func serveResponsesError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	protocol.WriteJSON(w, map[string]any{"type": "error", "error": map[string]any{"type": code, "message": msg}})
}

func requestDecompressionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := decodedRequestBody(r)
		if err != nil {
			serveJSONHTTPError(w, http.StatusBadRequest, err.Error())
			return
		}
		if body != r.Body {
			r.Body = body
			r.Header.Del("Content-Encoding")
			r.ContentLength = -1
		}
		next.ServeHTTP(w, r)
	})
}

func decodedRequestBody(r *http.Request) (io.ReadCloser, error) {
	encoding := strings.TrimSpace(strings.ToLower(r.Header.Get("Content-Encoding")))
	switch encoding {
	case "", "identity":
		return r.Body, nil
	case "gzip", "x-gzip":
		reader, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, fmt.Errorf("invalid gzip request body: %w", err)
		}
		return &compositeReadCloser{Reader: reader, closers: []io.Closer{reader, r.Body}}, nil
	case "deflate":
		reader := flate.NewReader(r.Body)
		return &compositeReadCloser{Reader: reader, closers: []io.Closer{reader, r.Body}}, nil
	case "zstd":
		reader, err := zstd.NewReader(r.Body)
		if err != nil {
			return nil, fmt.Errorf("invalid zstd request body: %w", err)
		}
		return &compositeReadCloser{Reader: reader, closers: []io.Closer{zstdCloser{reader}, r.Body}}, nil
	case "br":
		return nil, fmt.Errorf("unsupported content-encoding: br")
	default:
		return nil, fmt.Errorf("unsupported content-encoding: %s", encoding)
	}
}

type compositeReadCloser struct {
	io.Reader
	closers []io.Closer
}
type zstdCloser struct{ *zstd.Decoder }
func (z zstdCloser) Close() error { z.Decoder.Close(); return nil }
func (c *compositeReadCloser) Close() error {
	var firstErr error
	for _, closer := range c.closers {
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func serveJSONHTTPError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, fmt.Sprintf("{\"error\":{\"message\":%q,\"type\":\"invalid_request_error\"}}\n", msg))
}

func firstNonSpaceByte(data []byte) byte {
	for _, b := range data {
		if !unicode.IsSpace(rune(b)) {
			return b
		}
	}
	return 0
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

func ParseGenerationDefaults(opts map[string]any) message.ChatOptions {
	var co message.ChatOptions
	if v, ok := opts["temperature"]; ok {
		switch val := v.(type) {
		case float64:
			co.Temperature = &val
		case string:
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				co.Temperature = &f
			}
		}
	}
	if v, ok := opts["top_p"]; ok {
		switch val := v.(type) {
		case float64:
			co.TopP = &val
		case string:
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				co.TopP = &f
			}
		}
	}
	if v, ok := opts["max_tokens"]; ok {
		switch val := v.(type) {
		case int64:
			n := int(val)
			co.MaxTokens = &n
		case float64:
			n := int(val)
			co.MaxTokens = &n
		case string:
			if n, err := strconv.Atoi(val); err == nil {
				co.MaxTokens = &n
			}
		}
	}
	if v, ok := opts["stop"]; ok {
		switch val := v.(type) {
		case string:
			co.Stop = strings.Split(val, ",")
		case []string:
			co.Stop = val
		case []any:
			for _, s := range val {
				if str, ok := s.(string); ok {
					co.Stop = append(co.Stop, str)
				}
			}
		}
	}
	return co
}

func mergeStop(stopField any, stopSequences []string) []string {
	var result []string
	switch v := stopField.(type) {
	case string:
		if v != "" {
			result = append(result, v)
		}
	case []any:
		for _, s := range v {
			if str, ok := s.(string); ok {
				result = append(result, str)
			}
		}
	}
	if len(result) == 0 {
		result = stopSequences
	}
	return result
}
