package balance

import (
	"context"
	"errors"
	"testing"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
)

type stubProvider struct {
	name   string
	events []message.Event
}

func (p *stubProvider) Name() string { return p.name }

func (p *stubProvider) List(context.Context) ([]remote.Model, error) { return nil, nil }

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
