package balance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
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

func (p *BalanceProvider) Chat(ctx context.Context, modelName string, messages []message.Message, options message.ChatOptions) (<-chan message.ChatResponse, error) {
	affinityKey := systemHash(messages)
	slog.Info("balance_system_prompt_hash", "hash", affinityKey)
	b := p.pickBackend(affinityKey)
	if b == nil {
		out := make(chan message.ChatResponse)
		go func() {
			out <- message.ChatResponse{Error: fmt.Errorf("all backends unavailable")}
			close(out)
		}()
		return out, nil
	}

	slog.Info("balance_routing", "backend", b.name, "model", modelName, "affinity", affinityKey[:8])

	ch, err := p.tryBackend(ctx, b, modelName, messages, options)
	if err != nil {
		// Initial call failed, try failover
		return p.failoverFrom(ctx, b, modelName, messages, options, affinityKey)
	}
	return p.wrapChannel(ctx, ch, b, modelName, messages, options, affinityKey), nil
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

func (p *BalanceProvider) tryBackend(ctx context.Context, b *backend, modelName string, messages []message.Message, options message.ChatOptions) (<-chan message.ChatResponse, error) {
	return b.provider.Chat(ctx, modelName, messages, options)
}

// wrapChannel forwards responses but detects errors for failover.
func (p *BalanceProvider) wrapChannel(ctx context.Context, ch <-chan message.ChatResponse, b *backend, modelName string, messages []message.Message, options message.ChatOptions, affinityKey string) <-chan message.ChatResponse {
	out := make(chan message.ChatResponse)
	go func() {
		defer close(out)
		var gotContent bool
		for resp := range ch {
			if resp.Error != nil && !gotContent {
				slog.Warn("balance_stream_error", "backend", b.name, "error", resp.Error)
				b.markUnavailable(30 * time.Second) // Default cooldown on error
				failCh, err := p.failoverFrom(ctx, b, modelName, messages, options, affinityKey)
				if err != nil {
					out <- message.ChatResponse{Error: err}
					return
				}
				for r := range failCh {
					out <- r
				}
				return
			}
			if resp.Content != "" {
				gotContent = true
			}
			out <- resp
		}
	}()
	return out
}

func (p *BalanceProvider) failoverFrom(ctx context.Context, failed *backend, modelName string, messages []message.Message, options message.ChatOptions, affinityKey string) (<-chan message.ChatResponse, error) {
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

		slog.Info("balance_failover", "backend", b.name, "model", modelName)
		ch, err := b.provider.Chat(ctx, modelName, messages, options)
		if err != nil {
			slog.Warn("balance_failover_error", "backend", b.name, "error", err)
			continue
		}

		p.affinity.Store(affinityKey, idx)
		return ch, nil
	}
	return nil, fmt.Errorf("all backends exhausted")
}

func systemHash(messages []message.Message) string {
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
