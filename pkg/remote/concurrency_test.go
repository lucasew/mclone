package remote

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lucasew/mclone/pkg/config"
	"github.com/lucasew/mclone/pkg/message"
)

type blockingProvider struct {
	current int32
	maxSeen int32
	release chan struct{}
}

func (p *blockingProvider) Name() string { return "blockingtest" }

func (p *blockingProvider) List(ctx context.Context) ([]Model, error) {
	return []Model{{Slug: "demo", Name: "demo"}}, nil
}

func (p *blockingProvider) Chat(ctx context.Context, req message.Request) (<-chan message.Event, error) {
	now := atomic.AddInt32(&p.current, 1)
	for {
		max := atomic.LoadInt32(&p.maxSeen)
		if now <= max || atomic.CompareAndSwapInt32(&p.maxSeen, max, now) {
			break
		}
	}

	ch := make(chan message.Event)
	go func() {
		defer close(ch)
		defer atomic.AddInt32(&p.current, -1)
		select {
		case <-p.release:
		case <-ctx.Done():
			ch <- message.ResponseError{Err: ctx.Err()}
			return
		}
		ch <- message.TextDelta{Text: "ok"}
		ch <- message.ResponseCompleted{Reason: message.StopReasonEndTurn}
	}()
	return ch, nil
}

func TestMaxConcurrentSerializesChat(t *testing.T) {
	t.Parallel()

	Register("blockingtest", func(name string, options map[string]any, resolve Resolver) (Provider, error) {
		return &blockingProvider{release: make(chan struct{})}, nil
	})

	dir := t.TempDir()
	confPath := filepath.Join(dir, "mclone.conf")
	if err := os.WriteFile(confPath, []byte(`
[remotes.demo]
type = "blockingtest"
max_concurrent = 1
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loader := &config.ConfigLoader{Location: confPath}
	resolve := NewResolver(loader)
	prov, err := resolve.Provider("demo")
	if err != nil {
		t.Fatalf("resolve provider: %v", err)
	}

	cp, ok := prov.(*concurrencyProvider)
	if !ok {
		t.Fatalf("expected concurrencyProvider, got %T", prov)
	}
	inner, ok := cp.inner.(*blockingProvider)
	if !ok {
		t.Fatalf("expected blockingProvider, got %T", cp.inner)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{}, 2)
	done := make(chan struct{}, 2)
	run := func() {
		ch, err := prov.Chat(ctx, message.Request{Model: "demo"})
		if err != nil {
			t.Errorf("chat error: %v", err)
			done <- struct{}{}
			return
		}
		started <- struct{}{}
		for range ch {
		}
		done <- struct{}{}
	}

	go run()
	<-started

	go run()

	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&inner.maxSeen); got != 1 {
		t.Fatalf("expected max concurrent chat 1, got %d", got)
	}

	close(inner.release)
	<-done
	<-started
	<-done
}
