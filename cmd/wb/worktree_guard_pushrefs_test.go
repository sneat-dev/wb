package main

import (
	"path/filepath"
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

// The hook-only flag answers before any checkout is inspected: a delete-only
// push exits 0 even for a path the guard could not otherwise resolve, while a
// push that updates a ref still reaches the guard.
func TestGuardPrePushStdinPassesADeleteOnlyPushWithoutInspectingTheCheckout(t *testing.T) {
	t.Parallel()
	zero, sha := strings.Repeat("0", 40), strings.Repeat("a", 40)
	missing := filepath.Join(t.TempDir(), "not-a-checkout")
	for _, test := range []struct {
		name  string
		input string
		want  int
	}{
		{"delete only", "(delete) " + zero + " refs/heads/x " + sha + "\n", exitOK},
		{"an update still reaches the guard", "refs/heads/x " + sha + " refs/heads/x " + zero + "\n", exitFindings},
	} {
		var stdout, stderr strings.Builder
		code := runWithStdin([]string{"--projects-root", t.TempDir(), "worktree", "guard", "--pre-push-stdin", missing}, strings.NewReader(test.input), &stdout, &stderr)
		if code == exitUsage || (test.want == exitOK && code != exitOK) || (test.want != exitOK && code == exitOK) {
			t.Errorf("%s: exit = %d (want %d)\nstdout=%s\nstderr=%s", test.name, code, test.want, stdout.String(), stderr.String())
		}
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
