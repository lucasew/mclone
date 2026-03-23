package openai

import (
	"errors"
	"testing"
)

func TestShouldIgnoreStreamError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		err         error
		sawContent  bool
		sawToolCall bool
		want        bool
	}{
		{
			name:       "no error",
			err:        nil,
			sawContent: true,
			want:       false,
		},
		{
			name: "no output yet",
			err:  errors.New("stream error: stream ID 41; NO_ERROR; received from peer"),
			want: false,
		},
		{
			name:       "ignore no_error after content",
			err:        errors.New("stream error: stream ID 41; NO_ERROR; received from peer"),
			sawContent: true,
			want:       true,
		},
		{
			name:        "ignore context canceled after tool call",
			err:         errors.New("context canceled"),
			sawToolCall: true,
			want:        true,
		},
		{
			name:       "other stream errors still fail",
			err:        errors.New("stream error: unexpected EOF"),
			sawContent: true,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shouldIgnoreStreamError(tt.err, tt.sawContent, tt.sawToolCall)
			if got != tt.want {
				t.Fatalf("shouldIgnoreStreamError(%v, %v, %v) = %v, want %v", tt.err, tt.sawContent, tt.sawToolCall, got, tt.want)
			}
		})
	}
}
