package server

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

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

func (stderrSpanExporter) Shutdown(context.Context) error { return nil }

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
