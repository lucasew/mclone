package main

import (
	"bytes"
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
	"time"

	"github.com/goccy/go-json"
	"github.com/lucasew/mclone/pkg/config"
	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/monitor"
	"github.com/lucasew/mclone/pkg/protocol"
	"github.com/lucasew/mclone/pkg/protocol/anthropic"
	"github.com/lucasew/mclone/pkg/protocol/openai"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
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

var serveCmd = &cobra.Command{
	Use:   "serve [remote]",
	Short: "Serve a remote via OpenAI or Anthropic compatible API",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		remoteName := strings.TrimSuffix(args[0], ":")
		port, _ := cmd.Flags().GetInt("port")
		overrideModel, _ := cmd.Flags().GetString("model")
		verbose, _ := cmd.Flags().GetBool("verbose")

		level := slog.LevelInfo
		if verbose {
			level = slog.LevelDebug
		}
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
		shutdownTracing := setupTracing(verbose)
		defer func() {
			if err := shutdownTracing(cmd.Context()); err != nil {
				monitor.ReportError(cmd.Context(), err, "action", "shutdown_tracing")
			}
		}()

		loader := config.LoaderFrom(cmd.Context())
		conf, err := loader.Load()
		if err != nil {
			monitor.ReportError(cmd.Context(), err, "action", "load_config")
			return
		}

		resolve := remote.NewResolver(loader)
		p, err := resolve.Provider(remoteName)
		if err != nil {
			monitor.ReportError(cmd.Context(), err, "action", "create_provider")
			return
		}

		// Parse generation defaults from config options
		var defaultOpts message.ChatOptions
		if rc, ok := conf.Remotes[remoteName]; ok {
			defaultOpts = parseGenerationDefaults(rc.Options)
		}

		anthropicWriter := anthropic.NewWriter()
		openaiWriter := openai.NewWriter()

		mux := http.NewServeMux()
		mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
			serveChatRequest(w, r, p, overrideModel, anthropicWriter, cmd, defaultOpts)
		})
		mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
			serveChatRequest(w, r, p, overrideModel, openaiWriter, cmd, defaultOpts)
		})
		mux.HandleFunc("/v1/responses", func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPost:
				serveResponsesRequest(w, r, p, overrideModel, cmd, defaultOpts)
			default:
				http.NotFound(w, r)
			}
		})
		mux.HandleFunc("/v1/responses/", func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				serveResponsesRetrieve(w, r)
			default:
				http.NotFound(w, r)
			}
		})
		mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
			serveModels(w, r, p)
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
		finalHandler := requestDecompressionMiddleware(handler)

		slog.Info("starting server", "remote", remoteName, "port", port)
		if err := http.ListenAndServe(fmt.Sprintf(":%d", port), finalHandler); err != nil {
			monitor.ReportError(cmd.Context(), err, "action", "server_listen")
		}
	},
}

func serveChatRequest(w http.ResponseWriter, r *http.Request, p remote.Provider, overrideModel string, writer protocol.Writer, cmd *cobra.Command, defaultOpts message.ChatOptions) {
	ctx, reqSpan := otel.Tracer("mclone/serve").Start(r.Context(), "serve.chat_request",
		trace.WithAttributes(
			attribute.String("http.method", r.Method),
			attribute.String("http.route", r.URL.Path),
			attribute.String("provider.name", p.Name()),
		),
	)
	defer reqSpan.End()
	r = r.WithContext(ctx)

	var req chatRequest
	var bodyReader io.Reader = r.Body

	if path, _ := cmd.Flags().GetString("save-raw-request"); path != "" {
		body, _ := io.ReadAll(r.Body)
		err := os.WriteFile(path, body, 0644)
		if err != nil {
			monitor.ReportError(r.Context(), err, "action", "save_raw_request", "path", path)
		} else {
			slog.Debug("saved raw request", "path", path)
		}
		bodyReader = bytes.NewReader(body)
	}

	if err := json.NewDecoder(bodyReader).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	responseModel := req.Model
	chatModel := req.Model
	if overrideModel != "" {
		chatModel = overrideModel
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
	opts = opts.WithDefaults(defaultOpts)

	providerCtx, providerSpan := otel.Tracer("mclone/provider").Start(r.Context(), "provider.chat",
		trace.WithAttributes(
			attribute.String("provider.name", p.Name()),
			attribute.String("model.requested", req.Model),
			attribute.String("model.actual", chatModel),
		),
	)
	respChan, err := p.Chat(providerCtx, message.Request{
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
	firstEvent, ok := <-respChan
	if ok {
		elapsed := time.Since(firstEventStartedAt)
		providerSpan.SetAttributes(attribute.Int64("stream.first_event_ms", elapsed.Milliseconds()))
		providerSpan.AddEvent("first_event")
		slog.Debug("provider_first_event",
			"provider", p.Name(),
			"model", chatModel,
			"elapsed", elapsed,
		)
		if ev, isErr := firstEvent.(message.ResponseError); isErr && serveRateLimitError(w, writer, ev.Err) {
			providerSpan.RecordError(ev.Err)
			providerSpan.SetStatus(codes.Error, ev.Err.Error())
			providerSpan.SetAttributes(
				attribute.Int("stream.event_count", 1),
				attribute.Int64("stream.duration_ms", elapsed.Milliseconds()),
			)
			providerSpan.End()
			reqSpan.RecordError(ev.Err)
			reqSpan.SetStatus(codes.Error, ev.Err.Error())
			slog.Debug("provider_chat_finished",
				"provider", p.Name(),
				"model", chatModel,
				"elapsed", elapsed,
				"events", 1,
			)
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
		if ok {
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
				slog.Debug("provider_first_event",
					"provider", p.Name(),
					"model", chatModel,
					"elapsed", elapsed,
				)
			}
			switch tev := ev.(type) {
			case message.ResponseCompleted:
				finishReason = string(tev.Reason)
			case message.ResponseError:
				finalErr = tev.Err
			}
			instrumented <- ev
		}
		providerSpan.SetAttributes(
			attribute.Int("stream.event_count", eventCount),
			attribute.Int64("stream.duration_ms", time.Since(startedAt).Milliseconds()),
		)
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
		slog.Debug("provider_chat_finished",
			"provider", p.Name(),
			"model", chatModel,
			"elapsed", time.Since(startedAt),
			"events", eventCount,
		)
	}()

	writer.ServeResponse(w, instrumented, responseModel, req.Stream)
}

func serveRateLimitError(w http.ResponseWriter, writer protocol.Writer, err error) bool {
	var rl *message.ErrRateLimit
	if !errors.As(err, &rl) {
		return false
	}

	setRateLimitHeaders(w, writer, rl.RetryAfter)
	w.WriteHeader(http.StatusTooManyRequests)

	switch writer.(type) {
	case *openai.Writer:
		protocol.WriteJSON(w, map[string]any{
			"error": map[string]any{
				"message": err.Error(),
				"type":    "rate_limit_error",
				"code":    "rate_limit_exceeded",
			},
		})
	case *anthropic.Writer:
		protocol.WriteJSON(w, map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "rate_limit_error",
				"message": err.Error(),
			},
		})
	case responsesAPIWriter:
		protocol.WriteJSON(w, map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "rate_limit_error",
				"message": err.Error(),
			},
		})
	default:
		protocol.WriteJSON(w, map[string]any{
			"error": err.Error(),
		})
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
	case *openai.Writer:
		reset := (time.Duration(seconds) * time.Second).String()
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		w.Header().Set("x-ratelimit-reset-requests", reset)
		w.Header().Set("x-ratelimit-reset-tokens", reset)
	case responsesAPIWriter:
		reset := (time.Duration(seconds) * time.Second).String()
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		w.Header().Set("x-ratelimit-reset-requests", reset)
		w.Header().Set("x-ratelimit-reset-tokens", reset)
	case *anthropic.Writer:
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
		args := []any{
			"span", span.Name(),
			"trace_id", span.SpanContext().TraceID().String(),
			"span_id", span.SpanContext().SpanID().String(),
			"parent_span_id", span.Parent().SpanID().String(),
			"duration_ms", span.EndTime().Sub(span.StartTime()).Milliseconds(),
			"status", span.Status().Code.String(),
		}
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

func (stderrSpanExporter) Shutdown(context.Context) error {
	return nil
}

func setupTracing(verbose bool) func(context.Context) error {
	if !verbose {
		return func(context.Context) error { return nil }
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(stderrSpanExporter{})),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown
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

func serveModels(w http.ResponseWriter, r *http.Request, p remote.Provider) {
	models, err := p.List(r.Context())
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
		resp.Data = append(resp.Data, modelEntry{
			ID: m.Slug, Object: "model", Created: 1677610602, OwnedBy: "mclone",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		monitor.ReportError(r.Context(), err, "action", "serve_models_encode_error")
	}
}

func parseGenerationDefaults(opts map[string]any) message.ChatOptions {
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

func init() {
	serveCmd.Flags().Int("port", 8080, "Port to listen on")
	serveCmd.Flags().String("model", "", "Force a specific model name (useful for Claude Code)")
	serveCmd.Flags().BoolP("verbose", "v", false, "Enable debug logs")
	serveCmd.Flags().String("save-raw-request", "", "Path to save the raw incoming request body (overwrites)")
	rootCmd.AddCommand(serveCmd)
}
