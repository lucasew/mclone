package server

import (
	"testing"
)

func TestExtractResponseArguments(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{name: "nil", in: nil, want: "{}"},
		{name: "empty string", in: "", want: "{}"},
		{name: "blank string", in: "   ", want: "{}"},
		{name: "object string", in: `{"query":"x"}`, want: `{"query":"x"}`},
		{name: "array string", in: `[1,2]`, want: `[1,2]`},
		{name: "plain string", in: "hello", want: `{"input":"hello"}`},
		{name: "map", in: map[string]any{"query": "x"}, want: `{"query":"x"}`},
		{name: "array", in: []any{"a", "b"}, want: `["a","b"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(extractResponseArguments(tt.in))
			if got != tt.want {
				t.Fatalf("got %s want %s", got, tt.want)
			}
		})
	}
}
