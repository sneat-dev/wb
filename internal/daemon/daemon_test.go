package daemon

// Pack unit p02 coverage: unifiedCgroupLine and lastPathComponent, pure
// string helpers.

import "testing"

func TestUnifiedCgroupLine(t *testing.T) {
	t.Parallel()
	if line, ok := unifiedCgroupLine(""); ok || line != "" {
		t.Fatalf("expected not-found for empty contents, got %q ok=%v", line, ok)
	}
	line, ok := unifiedCgroupLine("0::/user.slice/foo.scope\n")
	if !ok || line != "/user.slice/foo.scope" {
		t.Fatalf("expected match, got %q ok=%v", line, ok)
	}
	if line, ok := unifiedCgroupLine("1:cpu:/legacy.slice\n2:memory:/legacy.slice\n"); ok || line != "" {
		t.Fatalf("expected not-found when no 0:: line is present, got %q ok=%v", line, ok)
	}
}

func TestLastPathComponent(t *testing.T) {
	t.Parallel()
	if got := lastPathComponent(""); got != "" {
		t.Fatalf("expected empty for an all-slash/empty path, got %q", got)
	}
	if got := lastPathComponent("/a/b/c/"); got != "c" {
		t.Fatalf("expected c, got %q", got)
	}
	if got := lastPathComponent("solo"); got != "solo" {
		t.Fatalf("expected solo, got %q", got)
	}
}
