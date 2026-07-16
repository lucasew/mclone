package balance

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
)

type stubProvider struct {
	name    string
	events  []message.Event
	models  []remote.Model
	listErr error
}

func (p *stubProvider) Name() string { return p.name }

func (p *stubProvider) List(context.Context) ([]remote.Model, error) {
	if p.listErr != nil {
		return nil, p.listErr
	}
	return p.models, nil
}

func (p *stubProvider) Chat(context.Context, message.Request) (<-chan message.Event, error) {
	ch := make(chan message.Event, len(p.events))
	for _, event := range p.events {
		ch <- event
	}
	close(ch)
	return ch, nil
}

func TestBalanceProviderFailoverChainsAcrossMultipleBackends(t *testing.T) {
	provider := &BalanceProvider{
		backends: []*backend{
			{
				name:              "one",
				provider:          &stubProvider{name: "one", events: []message.Event{message.ResponseError{Err: errors.New("first failed")}}},
				failoverThreshold: defaultFailoverThreshold,
			},
			{
				name:              "two",
				provider:          &stubProvider{name: "two", events: []message.Event{message.ResponseError{Err: errors.New("second failed")}}},
				failoverThreshold: defaultFailoverThreshold,
			},
			{
				name:              "three",
				provider:          &stubProvider{name: "three", events: []message.Event{message.TextDelta{Text: "ok"}, message.ResponseCompleted{Reason: message.StopReasonEndTurn}}},
				failoverThreshold: defaultFailoverThreshold,
			},
		},
	}

	ch, err := provider.Chat(context.Background(), message.Request{
		Model: "demo",
		Turns: []message.Turn{message.TextTurn(message.RoleSystem, "same system")},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	var gotText bool
	for event := range ch {
		switch v := event.(type) {
		case message.TextDelta:
			if v.Text == "ok" {
				gotText = true
			}
		case message.ResponseError:
			t.Fatalf("unexpected response error: %v", v.Err)
		}
	}

	if !gotText {
		t.Fatal("expected text from third backend after chained failover")
	}
}

func TestListAllBackendsFail(t *testing.T) {
	t.Parallel()

	first := errors.New("one down")
	provider := &BalanceProvider{
		backends: []*backend{
			{name: "one", provider: &stubProvider{name: "one", listErr: first}},
			{name: "two", provider: &stubProvider{name: "two", listErr: errors.New("two down")}},
		},
	}

	models, err := provider.List(context.Background())
	if err == nil {
		t.Fatalf("List() error = nil, want all-backends failure; models=%v", models)
	}
	if !errors.Is(err, first) {
		t.Fatalf("List() error = %v, want wrapped first backend error", err)
	}
	if !strings.Contains(err.Error(), "all backends failed") {
		t.Fatalf("List() error = %v, want all-backends message", err)
	}
	if models != nil {
		t.Fatalf("List() models = %v, want nil on total failure", models)
	}
}

func TestListPartialBackendFailure(t *testing.T) {
	t.Parallel()

	provider := &BalanceProvider{
		backends: []*backend{
			{name: "one", provider: &stubProvider{name: "one", listErr: errors.New("one down")}},
			{name: "two", provider: &stubProvider{name: "two", models: []remote.Model{{Slug: "m1", Name: "Model 1"}}}},
		},
	}

	models, err := provider.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v, want nil when any backend succeeds", err)
	}
	if len(models) != 1 || models[0].Slug != "m1" {
		t.Fatalf("List() models = %+v, want single m1 from healthy backend", models)
	}
	if len(models[0].OwnedBy) != 1 || models[0].OwnedBy[0] != "two" {
		t.Fatalf("List() OwnedBy = %v, want [two]", models[0].OwnedBy)
	}
}

func TestSleepOrDoneRespectsCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	err := sleepOrDone(ctx, 5*time.Second)
	elapsed := time.Since(started)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("sleepOrDone() error = %v, want context.Canceled", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("sleepOrDone() blocked for %v after cancel, want immediate return", elapsed)
	}
}

func TestSleepOrDoneZeroIsNoop(t *testing.T) {
	t.Parallel()

	if err := sleepOrDone(context.Background(), 0); err != nil {
		t.Fatalf("sleepOrDone(0) error = %v, want nil", err)
	}
}

func TestChatCooldownWaitCancelled(t *testing.T) {
	t.Parallel()

	// Only backend is cooling down within failover threshold; wait must honor ctx.
	provider := &BalanceProvider{
		backends: []*backend{
			{
				name:              "one",
				provider:          &stubProvider{name: "one", events: []message.Event{message.TextDelta{Text: "should-not-run"}}},
				availableAt:       time.Now().Add(5 * time.Second),
				failoverThreshold: 30 * time.Second,
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel almost immediately so we do not sleep the full cooldown.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	_, err := provider.Chat(ctx, message.Request{
		Model: "demo",
		Turns: []message.Turn{message.TextTurn(message.RoleSystem, "cancel-wait")},
	})
	elapsed := time.Since(started)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Chat() error = %v, want context.Canceled", err)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("Chat() blocked for %v during cooldown wait after cancel", elapsed)
	}
}

func TestFailoverCooldownWaitCancelled(t *testing.T) {
	t.Parallel()

	// First backend fails immediately; second is cooling down within threshold.
	// Failover wait must return when ctx is cancelled.
	provider := &BalanceProvider{
		backends: []*backend{
			{
				name:              "one",
				provider:          &stubProvider{name: "one", events: []message.Event{message.ResponseError{Err: errors.New("first failed")}}},
				failoverThreshold: 30 * time.Second,
			},
			{
				name:              "two",
				provider:          &stubProvider{name: "two", events: []message.Event{message.TextDelta{Text: "ok"}}},
				availableAt:       time.Now().Add(5 * time.Second),
				failoverThreshold: 30 * time.Second,
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	ch, err := provider.Chat(ctx, message.Request{
		Model: "demo",
		Turns: []message.Turn{message.TextTurn(message.RoleSystem, "failover-cancel")},
	})
	if err != nil {
		// startBackend may surface cancel before returning a channel
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Chat() error = %v, want context.Canceled", err)
		}
		return
	}

	var gotErr error
	for event := range ch {
		if re, ok := event.(message.ResponseError); ok {
			gotErr = re.Err
		}
	}
	elapsed := time.Since(started)
	if !errors.Is(gotErr, context.Canceled) {
		t.Fatalf("stream error = %v, want context.Canceled", gotErr)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("failover wait blocked for %v after cancel", elapsed)
	}
}
