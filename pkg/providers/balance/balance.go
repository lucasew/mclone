package balance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
)

const defaultFailoverThreshold = 60 * time.Second

type backend struct {
	provider          remote.Provider
	name              string
	availableAt       time.Time
	failoverThreshold time.Duration
	consecutive       int // consecutive rate limit errors (for backoff when RetryAfter==0)
	mu                sync.Mutex
}

func (b *backend) getAvailableAt() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.availableAt
}

func (b *backend) markUnavailable(retryAfter time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.availableAt = time.Now().Add(retryAfter)
	slog.Info("balance_backend_cooldown", "backend", b.name, "until", b.availableAt.Format(time.RFC3339))
}

func (b *backend) resetConsecutive() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consecutive = 0
}

type BalanceProvider struct {
	backends []*backend
	// affinity maps system prompt hash → backend index
	affinity sync.Map
}

func (p *BalanceProvider) Name() string { return "balance" }

func (p *BalanceProvider) List(ctx context.Context) ([]remote.Model, error) {
	seen := make(map[string]bool)
	var all []remote.Model
	for _, b := range p.backends {
		models, err := b.provider.List(ctx)
		if err != nil {
			slog.Warn("balance_list_error", "backend", b.name, "error", err)
			continue
		}
		for _, m := range models {
			if !seen[m.Slug] {
				seen[m.Slug] = true
				all = append(all, m)
			}
		}
	}
	return all, nil
}

func (p *BalanceProvider) Chat(ctx context.Context, req message.Request) (<-chan message.Event, error) {
	affinityKey := systemHash(req.Turns)
	slog.Info("balance_system_prompt_hash", "hash", affinityKey)
	b := p.pickBackend(affinityKey)
	if b == nil {
		out := make(chan message.Event)
		go func() {
			out <- message.ResponseError{Err: fmt.Errorf("all backends unavailable")}
			close(out)
		}()
		return out, nil
	}

	slog.Info("balance_routing", "backend", b.name, "model", req.Model, "affinity", affinityKey[:8])

	ch, err := p.tryBackend(ctx, b, req)
	if err != nil {
		// Initial call failed, try failover
		return p.failoverFrom(ctx, b, req, affinityKey)
	}
	return p.wrapChannel(ctx, ch, b, req, affinityKey), nil
}

func (p *BalanceProvider) pickBackend(affinityKey string) *backend {
	// Check affinity first
	if idx, ok := p.affinity.Load(affinityKey); ok {
		b := p.backends[idx.(int)]
		wait := time.Until(b.getAvailableAt())
		if wait <= 0 {
			return b
		}
		if wait <= b.failoverThreshold {
			slog.Debug("balance_waiting", "backend", b.name, "wait", wait)
			time.Sleep(wait)
			return b
		}
		// Affinity backend too slow, pick another
	}

	// Find first available backend
	for i, b := range p.backends {
		wait := time.Until(b.getAvailableAt())
		if wait <= 0 {
			p.affinity.Store(affinityKey, i)
			return b
		}
		if wait <= b.failoverThreshold {
			slog.Debug("balance_waiting", "backend", b.name, "wait", wait)
			time.Sleep(wait)
			p.affinity.Store(affinityKey, i)
			return b
		}
	}
	// All backends busy/cooling down. Pick the one with shortest wait?
	// For now return nil, trigger failover logic (which also checks availability)
	return nil
}

func (p *BalanceProvider) tryBackend(ctx context.Context, b *backend, req message.Request) (<-chan message.Event, error) {
	return b.provider.Chat(ctx, req)
}

// wrapChannel forwards responses but detects errors for failover.
func (p *BalanceProvider) wrapChannel(ctx context.Context, ch <-chan message.Event, b *backend, req message.Request, affinityKey string) <-chan message.Event {
	out := make(chan message.Event)
	go func() {
		defer close(out)
		var gotContent bool
		for resp := range ch {
			if errEvent, ok := resp.(message.ResponseError); ok && !gotContent {
				cooldown := p.handleBackendError(b, errEvent.Err)
				b.markUnavailable(cooldown)

				failCh, err := p.failoverFrom(ctx, b, req, affinityKey)
				if err != nil {
					out <- message.ResponseError{Err: err}
					return
				}
				for r := range failCh {
					out <- r
				}
				return
			}
			switch resp.(type) {
			case message.TextDelta, message.ReasoningDelta, message.ToolCallDelta, message.ToolCallFinished:
				gotContent = true
				b.resetConsecutive()
			}
			out <- resp
		}
	}()
	return out
}

// handleBackendError determines cooldown duration from an error.
// For ErrRateLimit with RetryAfter > 0, uses that value and resets consecutive.
// For ErrRateLimit with RetryAfter == 0, uses exponential backoff (1s * 2^consecutive).
// For other errors, uses a fixed 30s cooldown.
func (p *BalanceProvider) handleBackendError(b *backend, err error) time.Duration {
	var rl *message.ErrRateLimit
	if errors.As(err, &rl) {
		if rl.RetryAfter > 0 {
			b.resetConsecutive()
			slog.Warn("balance_rate_limit", "backend", b.name, "retry_after", rl.RetryAfter)
			return rl.RetryAfter
		}
		b.mu.Lock()
		b.consecutive++
		n := b.consecutive
		b.mu.Unlock()
		cooldown := time.Second * time.Duration(math.Pow(2, float64(n)))
		slog.Warn("balance_rate_limit_backoff", "backend", b.name, "consecutive", n, "cooldown", cooldown)
		return cooldown
	}
	slog.Warn("balance_stream_error", "backend", b.name, "error", err)
	return 30 * time.Second
}

func (p *BalanceProvider) failoverFrom(ctx context.Context, failed *backend, req message.Request, affinityKey string) (<-chan message.Event, error) {
	startIdx := 0
	for i, b := range p.backends {
		if b == failed {
			startIdx = i + 1
			break
		}
	}

	// Try round-robin from failed backend onwards
	count := len(p.backends)
	for i := 0; i < count; i++ {
		idx := (startIdx + i) % count
		b := p.backends[idx]
		if b == failed {
			continue
		}

		wait := time.Until(b.getAvailableAt())
		if wait > b.failoverThreshold {
			continue
		}
		if wait > 0 {
			slog.Debug("balance_failover_waiting", "backend", b.name, "wait", wait)
			time.Sleep(wait)
		}

		slog.Info("balance_failover", "backend", b.name, "model", req.Model)
		ch, err := b.provider.Chat(ctx, req)
		if err != nil {
			slog.Warn("balance_failover_error", "backend", b.name, "error", err)
			continue
		}

		p.affinity.Store(affinityKey, idx)
		return ch, nil
	}
	return nil, fmt.Errorf("all backends exhausted")
}

func systemHash(messages []message.Turn) string {
	h := sha256.New()
	for _, m := range messages {
		if m.Role == message.RoleSystem {
			for _, p := range m.Parts {
				if tp, ok := p.(message.TextPart); ok {
					h.Write([]byte(tp.Text))
				}
			}
			break
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func parseThreshold(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func init() {
	remote.Register("balance", func(name string, options map[string]any, resolve remote.Resolver) (remote.Provider, error) {
		if resolve.Provider == nil {
			return nil, fmt.Errorf("balance provider requires a resolver")
		}

		remoteList := getString(options, "remotes")
		if remoteList == "" {
			return nil, fmt.Errorf("balance provider requires 'remotes' option (comma-separated)")
		}

		groupThreshold := parseThreshold(getString(options, "failover_threshold"), defaultFailoverThreshold)

		names := strings.Split(remoteList, ",")
		backends := make([]*backend, 0, len(names))
		for _, rn := range names {
			rn = strings.TrimSpace(rn)
			prov, err := resolve.Provider(rn)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve remote %q: %w", rn, err)
			}

			// Per-backend threshold: specific > group > default
			threshold := parseThreshold(getString(options, "failover_threshold:"+rn), groupThreshold)

			backends = append(backends, &backend{
				provider:          prov,
				name:              rn,
				failoverThreshold: threshold,
			})
		}

		return &BalanceProvider{backends: backends}, nil
	})
}
