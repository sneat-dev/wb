package main

import "testing"

// TestLifecycleShortSHATruncatesTo12Chars covers the truncation branch
// of lifecycleShortSHA (cmd/wb/hooks_lifecycle.go): a SHA longer than 12
// characters must be cut to exactly the first 12, not returned in full.
func TestLifecycleShortSHATruncatesTo12Chars(t *testing.T) {
	t.Parallel()
	full := "abcdef0123456789"
	got := lifecycleShortSHA(full)
	if got != full[:12] {
		t.Fatalf("lifecycleShortSHA(%q) = %q, want %q", full, got, full[:12])
	}
	if len(got) != 12 {
		t.Fatalf("lifecycleShortSHA(%q) returned length %d, want 12", full, len(got))
	}
}
