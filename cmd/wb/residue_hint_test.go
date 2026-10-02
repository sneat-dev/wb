package main

import (
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktreelanding"
)

// The command a landed-with-residue finding tells the operator to run must
// parse against the real command tree: sneat-dev/wb#814 was a hint naming a
// flag the printing verb rejected.
func TestResidueHintNamesACommandThatParses(t *testing.T) {
	t.Parallel()
	reason := worktreelanding.ResidueReason(&worktreelanding.LandingEvidence{
		LandedSHA: "a", LandingSHA: "b", Residue: []worktreelanding.ResidualCommit{{SHA: "c", Subject: "s"}},
	}, "founder-rulings-1002b")
	_, hint, found := strings.Cut(reason, "with: ")
	if !found {
		t.Fatalf("reason names no command to run:\n%s", reason)
	}
	words := strings.Fields(hint)
	if len(words) < 3 || words[0] != "wb" {
		t.Fatalf("hint %q is not a wb command", hint)
	}
	root := newRootCmd()
	command, args, err := root.Find(words[1:])
	if err != nil {
		t.Fatalf("hint %q names no command: %v", hint, err)
	}
	if err := command.ParseFlags(args); err != nil {
		t.Fatalf("hint %q names a flag the verb rejects: %v", hint, err)
	}
	if got := command.CommandPath(); got != "wb worktree gc" {
		t.Fatalf("hint resolves to %q, want wb worktree gc", got)
	}
}
