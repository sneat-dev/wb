package streambranch

import "testing"

func TestIsRecognizesBothSpellingsAndNothingElse(t *testing.T) {
	for _, ref := range []string{"stream/checkout", "refs/heads/stream/checkout", "stream/a/b"} {
		if !Is(ref) {
			t.Errorf("Is(%q) = false, want true", ref)
		}
	}
	for _, ref := range []string{
		"", "main", "feature/stream-thing", "streams/checkout",
		"refs/tags/stream/v1", "refs/wb/checkpoints/stream/x",
	} {
		if Is(ref) {
			t.Errorf("Is(%q) = true; the namespace is a path prefix on a branch ref, not a substring", ref)
		}
	}
}

func TestNameRendersTheBranch(t *testing.T) {
	if got := Name("checkout-rewrite"); got != "stream/checkout-rewrite" {
		t.Fatalf("Name = %q", got)
	}
}

func TestStreamNameExtractsOrRefuses(t *testing.T) {
	name, ok := StreamName("refs/heads/stream/checkout-rewrite")
	if !ok || name != "checkout-rewrite" {
		t.Fatalf("StreamName = %q, %t", name, ok)
	}
	if _, ok := StreamName("main"); ok {
		t.Error("StreamName accepted a non-stream branch")
	}
	if _, ok := StreamName("stream/"); ok {
		t.Error("StreamName accepted an empty stream name")
	}
}
