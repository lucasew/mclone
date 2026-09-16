package monitor

import (
	"context"
	"log/slog"
)

// ReportError captures an error and reports it to the centralized logging/monitoring system.
// This function should be used for all unexpected errors that need to be tracked.
func ReportError(ctx context.Context, err error, args ...any) {
	if err == nil {
		return
	}
	// For now, just log using slog. In the future, this can be hooked to Sentry or other APM.
	logArgs := append([]any{"error", err}, args...)
	slog.ErrorContext(ctx, "unexpected error", logArgs...)
}
