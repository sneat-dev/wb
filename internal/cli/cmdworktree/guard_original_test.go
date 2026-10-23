package cmdworktree

import (
	"github.com/sneat-dev/wb/internal/worktrees"
	"strings"
	"testing"
)

func TestCwWtFormatCanonicalFreshness(t *testing.T) {
	t.Parallel()
	if got := formatCanonicalFreshness(nil); got != "not checked" {
		t.Fatalf("nil freshness = %q", got)
	}
	withError := &worktrees.CanonicalFreshness{Status: worktrees.CanonicalFreshnessOffline, RemoteRef: "origin/main", Error: "network down"}
	if got := formatCanonicalFreshness(withError); !strings.Contains(got, "status=") || !strings.Contains(got, "network down") {
		t.Fatalf("error freshness = %q", got)
	}
	fresh := &worktrees.CanonicalFreshness{
		Status: worktrees.CanonicalFreshnessCurrent, RemoteRef: "origin/main",
		LocalSHA: "aaaa", RemoteSHA: "bbbb", Ahead: 1, Behind: 2,
	}
	got := formatCanonicalFreshness(fresh)
	for _, want := range []string{"status=current", "target=origin/main", "local=aaaa", "remote=bbbb", "(1 ahead, 2 behind)"} {
		if !strings.Contains(got, want) {
			t.Errorf("freshness text missing %q: %s", want, got)
		}
	}
}

func TestPushOnlyDeletesRemoteRefsReadsGitsPrePushList(t *testing.T) {
	t.Parallel()
	zero, sha := strings.Repeat("0", 40), strings.Repeat("a", 40)
	for _, test := range []struct {
		name  string
		input string
		want  bool
	}{
		{"a deletion", "(delete) " + zero + " refs/heads/x " + sha + "\n", true},
		{"an update", "refs/heads/x " + sha + " refs/heads/x " + zero + "\n", false},
		{"nothing", "", false},
		{"a malformed list is never trusted", "garbage\n", false},
	} {
		if got := pushOnlyDeletesRemoteRefs(strings.NewReader(test.input), func(any) bool { return false }); got != test.want {
			t.Errorf("%s: pushOnlyDeletesRemoteRefs = %t, want %t", test.name, got, test.want)
		}
	}
}

func TestPushOnlyDeletesRemoteRefsNeverReadsATerminal(t *testing.T) {
	t.Parallel()
	zero, sha := strings.Repeat("0", 40), strings.Repeat("a", 40)
	deletion := strings.NewReader("(delete) " + zero + " refs/heads/x " + sha + "\n")
	if pushOnlyDeletesRemoteRefs(deletion, func(any) bool { return true }) {
		t.Fatal("a terminal was treated as a pushed-ref list")
	}
	if deletion.Len() == 0 {
		t.Fatal("a terminal's input was read")
	}
}
