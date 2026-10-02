package main

import (
	"os"
	"strings"
	"testing"
)

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
		if got := pushOnlyDeletesRemoteRefs(strings.NewReader(test.input)); got != test.want {
			t.Errorf("%s: pushOnlyDeletesRemoteRefs = %t, want %t", test.name, got, test.want)
		}
	}
}

func TestPushOnlyDeletesRemoteRefsNeverReadsATerminal(t *testing.T) {
	t.Parallel()
	tty, err := os.Open("/dev/tty")
	if err != nil {
		t.Skipf("no controlling terminal: %v", err)
	}
	defer func() { _ = tty.Close() }()
	if pushOnlyDeletesRemoteRefs(tty) {
		t.Fatal("a terminal was treated as a pushed-ref list")
	}
}

func TestGuardPrePushStdinFlagIsHiddenFromHelp(t *testing.T) {
	t.Parallel()
	var stdout, stderr strings.Builder
	if code := run([]string{"worktree", "guard", "--help"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "pre-push-stdin") {
		t.Fatalf("the hook-only flag is advertised:\n%s", stdout.String())
	}
}
