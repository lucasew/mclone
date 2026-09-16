package message

import (
	"fmt"
	"time"
)

// ErrRateLimit indicates the backend returned a rate limit error (e.g. HTTP 429).
// RetryAfter holds the backend-suggested wait duration, or 0 if unknown.
type ErrRateLimit struct {
	RetryAfter time.Duration
}

func (e *ErrRateLimit) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("rate limited, retry after %s", e.RetryAfter)
	}
	return "rate limited"
}
