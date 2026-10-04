package balance

import (
	"context"
	"errors"
	"testing"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
)

var (
	ErrFirstFailed  = errors.New("first failed")
	ErrSecondFailed = errors.New("second failed")
	ErrOneDown      = errors.New("one down")
	ErrTwoDown      = errors.New("two down")
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
				provider:          &stubProvider{name: "one", events: []message.Event{message.ResponseError{Err: ErrFirstFailed}}},
				failoverThreshold: defaultFailoverThreshold,
			},
			{
				name:              "two",
				provider:          &stubProvider{name: "two", events: []message.Event{message.ResponseError{Err: ErrSecondFailed}}},
				failoverThreshold: defaultFailoverThreshold,
			},
			{
				name:              "three",
				provider:          &stubProvider{name: "three", events: []message.Event{message.TextDelta{Text: "ok"}, message.ResponseCompleted{Reason: message.StopReasonEndTurn}}},
				failoverThreshold: defaultFailoverThreshold,
			},
		},
	}

	ch, err := provider.Chat(t.Context(), message.Request{
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

	provider := &BalanceProvider{
		backends: []*backend{
			{name: "one", provider: &stubProvider{name: "one", listErr: ErrOneDown}},
			{name: "two", provider: &stubProvider{name: "two", listErr: ErrTwoDown}},
		},
	}

	models, err := provider.List(t.Context())
	if err == nil {
		t.Fatalf("List() error = nil, want all-backends failure; models=%v", models)
	}
	if !errors.Is(err, ErrOneDown) {
		t.Fatalf("List() error = %v, want wrapped first backend error", err)
	}
	if !errors.Is(err, ErrAllBackendsFailed) {
		t.Fatalf("List() error = %v, want %v", err, ErrAllBackendsFailed)
	}
	if models != nil {
		t.Fatalf("List() models = %v, want nil on total failure", models)
	}
}

func TestListPartialBackendFailure(t *testing.T) {
	t.Parallel()

	provider := &BalanceProvider{
		backends: []*backend{
			{name: "one", provider: &stubProvider{name: "one", listErr: ErrOneDown}},
			{name: "two", provider: &stubProvider{name: "two", models: []remote.Model{{Slug: "m1", Name: "Model 1"}}}},
		},
	}

	models, err := provider.List(t.Context())
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
