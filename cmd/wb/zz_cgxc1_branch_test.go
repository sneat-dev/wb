package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// cov-cgx-c1 trial lane: covers cmd/wb/branch.go seams named in
// scratchpad/seams/rwi/c1.txt: the print helpers directly, and the
// newBranchCountCmd/newBranchListCmd/newBranchCleanupCmd `switch format`
// RunE closures via a real (but empty) projects root so no network or real
// git history is needed. newBranchQuarantineCmd's loop closure is not
// exercised here: producing both a succeeding and a failing quarantine
// candidate needs a real git repository and branch — out of scope for this
// lane's budget (see final report).

func TestCgxc1RetiredRemoteTagCountReadsScopedTotal(t *testing.T) {
	t.Parallel()
	if got := retiredRemoteTagCount(worktrees.BranchListOutcome{RetiredTags: map[string]int{worktrees.BranchScopeRemote: 5}}); got != "5" {
		t.Fatalf("got %q", got)
	}
	if got := retiredRemoteTagCount(worktrees.BranchListOutcome{RetiredRemoteUnavailable: true}); got != "unavailable" {
		t.Fatalf("got %q", got)
	}
	if got := retiredRemoteTagCount(worktrees.BranchListOutcome{}); got != "0" {
		t.Fatalf("got %q", got)
	}
}

func TestCgxc1PrintRetiredBranchSummaryReportsCountsAndStaysSilentWhenZero(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	outcome := worktrees.BranchListOutcome{
		RetiredBranches: 2, RetiredTagNames: 1,
		RetiredRefs: map[string]int{"local": 2},
		RetiredTags: map[string]int{"local": 1, worktrees.BranchScopeRemote: 3},
	}
	if err := printRetiredBranchSummary(&out, outcome); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"retired branches 2", "tags 1", "remote=3"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}

	out.Reset()
	if err := printRetiredBranchSummary(&out, worktrees.BranchListOutcome{}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no output for a zero outcome, got %q", out.String())
	}
}

func TestCgxc1PrintBranchCountPropagatesWriteError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("stdout is closed")
	if err := printBranchCount(failingWriter{err: sentinel}, worktrees.BranchListOutcome{}); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
}

func TestCgxc1PrintBranchDiagnosticsPropagatesWriteError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("stdout is closed")
	if err := printBranchDiagnostics(failingWriter{err: sentinel}, []string{"one diagnostic"}); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
}

func TestCgxc1PrintDispositionTotalsPropagatesWriteError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("stdout is closed")
	if err := printDispositionTotals(failingWriter{err: sentinel}, map[string]int{"landed": 1}); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
}

func TestCgxc1PrintBranchListPropagatesWriteErrorsAtEveryStage(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("stdout is closed")
	outcome := worktrees.BranchListOutcome{
		Entries: []worktrees.BranchEntry{{Repository: "acme/app", Branch: "task/one", ShortSHA: "abc1234", Disposition: "unmerged"}},
	}
	// Write order for one entry, empty Totals, nothing retired: header line,
	// repository header, row, trailing blank line. Failing at each position
	// in turn covers every write in printBranchList without depending on
	// exact source-line offsets.
	for after := 0; after < 4; after++ {
		after := after
		t.Run(fmt.Sprintf("after-%d-writes", after), func(t *testing.T) {
			t.Parallel()
			command := &cobra.Command{}
			command.SetOut(&cgxc1FailAfterWriter{after: after, err: sentinel})
			if err := printBranchList(command, outcome); !errors.Is(err, sentinel) {
				t.Fatalf("after=%d err=%v", after, err)
			}
		})
	}

	t.Run("empty entries first write failure", func(t *testing.T) {
		t.Parallel()
		command := &cobra.Command{}
		command.SetOut(&cgxc1FailAfterWriter{after: 0, err: sentinel})
		if err := printBranchList(command, worktrees.BranchListOutcome{}); !errors.Is(err, sentinel) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestCgxc1PrintBranchCleanupPropagatesWriteErrorsForEveryRowKind(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("stdout is closed")
	outcome := worktrees.BranchCleanupOutcome{
		Apply: true,
		Results: []worktrees.BranchCleanupResult{
			{BranchEntry: worktrees.BranchEntry{Repository: "acme/app", Scope: "local", Branch: "task/one", ShortSHA: "abc1234"}, Applied: true},
			{BranchEntry: worktrees.BranchEntry{Repository: "acme/app", Scope: "remote", Branch: "origin/task/two", ShortSHA: "def5678"}, Eligible: true},
			{BranchEntry: worktrees.BranchEntry{Repository: "acme/app", Scope: "local", Branch: "task/three", ShortSHA: "0123456"}, Error: "delete failed"},
			{BranchEntry: worktrees.BranchEntry{Repository: "beta/tool", Disposition: "unreadable"}, SkipReason: "target could not be fetched"},
		},
	}
	// Write order: repo header(acme/app), deleted row, eligible row, failed
	// row, repo header(beta/tool), skip row, trailing blank line, summary
	// line = 8 writes. Failing at each position covers every branch of the
	// per-row switch plus the trailing summary line.
	for after := 0; after < 8; after++ {
		after := after
		t.Run(fmt.Sprintf("after-%d-writes", after), func(t *testing.T) {
			t.Parallel()
			command := &cobra.Command{}
			command.SetOut(&cgxc1FailAfterWriter{after: after, err: sentinel})
			if err := printBranchCleanup(command, outcome); !errors.Is(err, sentinel) {
				t.Fatalf("after=%d err=%v", after, err)
			}
		})
	}

	t.Run("no results first write failure", func(t *testing.T) {
		t.Parallel()
		command := &cobra.Command{}
		command.SetOut(&cgxc1FailAfterWriter{after: 0, err: sentinel})
		if err := printBranchCleanup(command, worktrees.BranchCleanupOutcome{}); !errors.Is(err, sentinel) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestCgxc1BranchCountSwitchFormatCoversEveryBranch(t *testing.T) {
	root := t.TempDir()
	inv := &invocation{projectsRoot: root}
	for _, format := range []string{"text", "json", "yaml", "bogus"} {
		format := format
		t.Run(format, func(t *testing.T) {
			command := newBranchCountCmd(inv)
			var out strings.Builder
			command.SetOut(&out)
			command.SetErr(&out)
			command.SetArgs([]string{"--format", format})
			err := command.Execute()
			if format == "bogus" {
				if err == nil || !strings.Contains(err.Error(), "unsupported format") {
					t.Fatalf("format=%s err=%v", format, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("format=%s err=%v out=%s", format, err, out.String())
			}
		})
	}
}

func TestCgxc1BranchListSwitchFormatCoversEveryBranch(t *testing.T) {
	root := t.TempDir()
	inv := &invocation{projectsRoot: root}
	for _, format := range []string{"text", "json", "yaml", "bogus"} {
		format := format
		t.Run(format, func(t *testing.T) {
			command := newBranchListCmd(inv)
			var out strings.Builder
			command.SetOut(&out)
			command.SetErr(&out)
			command.SetArgs([]string{"--format", format})
			err := command.Execute()
			if format == "bogus" {
				if err == nil || !strings.Contains(err.Error(), "unsupported format") {
					t.Fatalf("format=%s err=%v", format, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("format=%s err=%v out=%s", format, err, out.String())
			}
		})
	}
}

func TestCgxc1BranchCleanupSwitchFormatCoversEveryBranch(t *testing.T) {
	root := t.TempDir()
	inv := &invocation{projectsRoot: root}
	for _, format := range []string{"text", "json", "bogus"} {
		format := format
		t.Run(format, func(t *testing.T) {
			command := newBranchCleanupCmd(inv)
			var out strings.Builder
			command.SetOut(&out)
			command.SetErr(&out)
			command.SetArgs([]string{"--format", format})
			err := command.Execute()
			if format == "bogus" {
				if err == nil || !strings.Contains(err.Error(), "unsupported format") {
					t.Fatalf("format=%s err=%v", format, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("format=%s err=%v out=%s", format, err, out.String())
			}
		})
	}
}
