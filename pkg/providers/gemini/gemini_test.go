package gemini

import (
	"strings"
	"testing"

	"github.com/lucasew/mclone/pkg/httpclient"
)

func TestHTTPClientsUseSharedPackage(t *testing.T) {
	t.Parallel()
	if listHTTPClient != httpclient.List {
		t.Fatal("listHTTPClient should be httpclient.List")
	}
	if streamHTTPClient != httpclient.Stream {
		t.Fatal("streamHTTPClient should be httpclient.Stream")
	}
}

func TestSanitizeToolName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		fallback string
		want     string
	}{
		{name: "", fallback: "tool_2", want: "tool_2"},
		{name: "", fallback: "tool", want: "tool"},
		{name: "bad name!", fallback: "tool", want: "bad_name_"},
		{name: "A.b:c-d_e", fallback: "tool", want: "A.b:c-d_e"},
		{name: "1abc", fallback: "tool", want: "_1abc"},
		{name: "café", fallback: "tool", want: "caf_"},
	}
	for _, tc := range cases {
		got := SanitizeToolName(tc.name, tc.fallback)
		if got != tc.want {
			t.Errorf("SanitizeToolName(%q, %q) = %q, want %q", tc.name, tc.fallback, got, tc.want)
		}
	}

	long := strings.Repeat("a", 200)
	got := SanitizeToolName(long, "tool")
	if len(got) != 128 || strings.Trim(got, "a") != "" {
		t.Errorf("long letter name = %q (len %d)", got, len(got))
	}

	digits := strings.Repeat("9", 128)
	got = SanitizeToolName(digits, "tool")
	if len(got) != 128 || !strings.HasPrefix(got, "_") {
		t.Errorf("leading digit cap = %q (len %d)", got, len(got))
	}

	if got := sanitizeGeminiToolName("", 3); got != "tool_3" {
		t.Errorf("empty gemini name = %q, want tool_3", got)
	}
	if got := sanitizeGeminiToolName("bad name", 0); got != "bad_name" {
		t.Errorf("gemini sanitize = %q", got)
	}
}
