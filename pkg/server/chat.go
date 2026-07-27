package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/monitor"
	"github.com/lucasew/mclone/pkg/protocol"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

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
	bodyReader, ok := s.bodyReader(r)
	if !ok {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := json.NewDecoder(bodyReader).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
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

func (s *Server) bodyReader(r *http.Request) (io.Reader, bool) {
	var bodyReader io.Reader = r.Body
	if s.cfg.SaveRawRequestPath != "" {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			monitor.ReportError(r.Context(), err, "action", "read_raw_request")
			return r.Body, true
		}
		if err := os.WriteFile(s.cfg.SaveRawRequestPath, body, 0644); err != nil {
			monitor.ReportError(r.Context(), err, "action", "save_raw_request", "path", s.cfg.SaveRawRequestPath)
		} else {
			slog.Debug("saved raw request", "path", s.cfg.SaveRawRequestPath)
		}
		bodyReader = strings.NewReader(string(body))
	}
	return bodyReader, true
}

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
