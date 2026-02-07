package balance

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
)

type backend struct {
	provider remote.Provider
	model    string
}

type BalanceProvider struct {
	backends []backend
}

func (p *BalanceProvider) Name() string { return "balance" }

func (p *BalanceProvider) List(ctx context.Context) ([]remote.Model, error) {
	// Expose each backend as a model entry
	var models []remote.Model
	for _, b := range p.backends {
		models = append(models, remote.Model{
			Slug: b.model,
			Name: fmt.Sprintf("%s (%s)", b.model, b.provider.Name()),
		})
	}
	return models, nil
}

func (p *BalanceProvider) Chat(ctx context.Context, modelName string, messages []message.Message, options message.ChatOptions) (<-chan message.ChatResponse, error) {
	var lastErr error
	for _, b := range p.backends {
		slog.Info("balance_trying", "provider", b.provider.Name(), "model", b.model)
		ch, err := b.provider.Chat(ctx, b.model, messages, options)
		if err != nil {
			slog.Warn("balance_error", "provider", b.provider.Name(), "error", err)
			lastErr = err
			continue
		}
		// Wrap channel to detect streaming errors and failover
		return p.wrapWithFailover(ctx, ch, b, messages, options)
	}
	return nil, fmt.Errorf("all backends failed, last error: %w", lastErr)
}

// wrapWithFailover drains the channel. If it gets an error, tries the next backends.
func (p *BalanceProvider) wrapWithFailover(ctx context.Context, ch <-chan message.ChatResponse, current backend, messages []message.Message, options message.ChatOptions) (<-chan message.ChatResponse, error) {
	out := make(chan message.ChatResponse)
	go func() {
		defer close(out)

		var gotContent bool
		for resp := range ch {
			if resp.Error != nil && !gotContent {
				// No content sent yet, we can failover
				slog.Warn("balance_stream_error", "provider", current.provider.Name(), "error", resp.Error)
				p.failover(ctx, out, current, messages, options)
				return
			}
			if resp.Content != "" {
				gotContent = true
			}
			out <- resp
		}
	}()
	return out, nil
}

func (p *BalanceProvider) failover(ctx context.Context, out chan<- message.ChatResponse, failed backend, messages []message.Message, options message.ChatOptions) {
	// Find backends after the failed one
	started := false
	for _, b := range p.backends {
		if b.provider.Name() == failed.provider.Name() && b.model == failed.model {
			started = true
			continue
		}
		if !started {
			continue
		}

		slog.Info("balance_failover", "provider", b.provider.Name(), "model", b.model)
		ch, err := b.provider.Chat(ctx, b.model, messages, options)
		if err != nil {
			slog.Warn("balance_failover_error", "provider", b.provider.Name(), "error", err)
			continue
		}

		for resp := range ch {
			if resp.Error != nil {
				slog.Warn("balance_failover_stream_error", "provider", b.provider.Name(), "error", resp.Error)
				break
			}
			out <- resp
		}
		return
	}
	out <- message.ChatResponse{Error: fmt.Errorf("all backends exhausted")}
}

func init() {
	remote.Register("balance", func(name string, options map[string]string, resolve remote.Resolver) (remote.Provider, error) {
		if resolve == nil {
			return nil, fmt.Errorf("balance provider requires a resolver")
		}

		// Format: "remote1:model1,remote2:model2"
		backendsStr := options["backends"]
		if backendsStr == "" {
			return nil, fmt.Errorf("balance provider requires 'backends' option (format: remote:model,remote:model)")
		}

		entries := strings.Split(backendsStr, ",")
		backends := make([]backend, 0, len(entries))
		for _, entry := range entries {
			entry = strings.TrimSpace(entry)
			parts := strings.SplitN(entry, ":", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid backend %q (expected remote:model)", entry)
			}
			p, err := resolve(parts[0])
			if err != nil {
				return nil, fmt.Errorf("failed to resolve remote %q: %w", parts[0], err)
			}
			backends = append(backends, backend{provider: p, model: parts[1]})
		}

		return &BalanceProvider{backends: backends}, nil
	})
}
