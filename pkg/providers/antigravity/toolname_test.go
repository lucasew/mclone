package antigravity

import "testing"

func TestSanitizeToolNameUsesSharedFilter(t *testing.T) {
	t.Parallel()
	if got := sanitizeToolName(""); got != "tool" {
		t.Fatalf("empty = %q, want tool", got)
	}
	if got := sanitizeToolName("bad name!"); got != "bad_name_" {
		t.Fatalf("sanitize = %q, want bad_name_", got)
	}
	if got := sanitizeToolName("1abc"); got != "_1abc" {
		t.Fatalf("leading digit = %q, want _1abc", got)
	}
}
