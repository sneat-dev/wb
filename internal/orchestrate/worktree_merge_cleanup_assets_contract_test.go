package orchestrate

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestCleanupAssetsAdmissionAndTaskErrorsPreserveReceipt(t *testing.T) {
	t.Parallel()
	t.Run("missing rebatch original", func(t *testing.T) {
		t.Parallel()
		original := filepath.Join(t.TempDir(), "missing-original.json")
		receipt := WorktreeMergeReceipt{RebatchOf: original, CleanedTasks: []string{"already-cleaned"}, CleanupReports: []string{"prior-report"}}
		before := receipt
		err := cleanupWorktreeMergeAssets(t.Context(), t.TempDir(), &receipt)
		var pathErr *os.PathError
		if !errors.Is(err, os.ErrNotExist) || !errors.As(err, &pathErr) || pathErr.Path != original || !strings.Contains(err.Error(), "read rebatched receipt before cleanup") {
			t.Fatalf("rebatch admission lost actual original read error: %v", err)
		}
		if !reflect.DeepEqual(receipt, before) {
			t.Fatalf("refused admission mutated receipt: before=%+v after=%+v", before, receipt)
		}
	})
	t.Run("missing repository task", func(t *testing.T) {
		t.Parallel()
		root := filepath.Join(t.TempDir(), "missing-projects-root")
		receipt := WorktreeMergeReceipt{Repository: "owned/missing", Target: "main", Candidate: WorktreeMergeCandidate{Task: "pending-task"}}
		before := receipt
		err := cleanupWorktreeMergeAssets(t.Context(), root, &receipt)
		if err == nil || !strings.Contains(err.Error(), "cleanup task pending-task:") || !strings.Contains(err.Error(), "pending-task") {
			t.Fatalf("native inventory failure lost exact task context: %v", err)
		}
		if !reflect.DeepEqual(receipt, before) {
			t.Fatalf("failed cleanup mutated receipt: before=%+v after=%+v", before, receipt)
		}
	})
}

func TestPullRequestCreateExitCodesRetainLandedIncomplete(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		outcome CreateOutcome
		code    int
	}{
		{CreateSuccess, 0},
		{CreateFindings, 1},
		{CreateRefused, 2},
		{CreateLandedIncomplete, ExitLandedIncomplete},
		{"unknown", 1},
	} {
		t.Run(string(test.outcome), func(t *testing.T) {
			t.Parallel()
			result := PullRequestCreateResult{Outcome: test.outcome}
			if got := result.ExitCode(); got != test.code {
				t.Fatalf("create outcome %q exit=%d want=%d", test.outcome, got, test.code)
			}
		})
	}
}

func TestPullRequestCreateDeferredEventsUseResolvedEnvelopeRepository(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	worktree := filepath.Join(root, "missing-worktree")
	fallback, selected := &createEventRecorder{}, &createEventRecorder{}
	var repositories []string
	result, err := CreatePullRequest(t.Context(), PullRequestCreateOptions{
		ProjectsRoot: root, Worktree: worktree, Events: fallback, Stream: "fallback-stream",
		EventsForRepository: func(repository string) (streams.EventAppender, string) {
			repositories = append(repositories, repository)
			return selected, "unknown-repository-stream"
		},
	})
	// A nonexistent absolute directory is refused as an unsafe task segment
	// before branch validation, inventory discovery, or repository Guard.
	// The deferred envelope must route that actual unknown repository identity.
	if err == nil || result.Outcome != CreateFindings || result.Repository != "" || result.Reason != err.Error() || !strings.Contains(err.Error(), "one safe path segment") || !strings.Contains(err.Error(), strconv.Quote(worktree)) {
		t.Fatalf("missing worktree path produced inconsistent failure envelope: %+v: %v", result, err)
	}
	if !reflect.DeepEqual(repositories, []string{result.Repository}) || len(fallback.events) != 0 || len(selected.events) != 1 {
		t.Fatalf("deferred routing did not select exactly final repository: repositories=%v fallback=%+v selected=%+v", repositories, fallback.events, selected.events)
	}
	event := selected.events[0]
	if event.Stream != "unknown-repository-stream" || event.Repository != result.Repository || event.Verb != result.Verb || event.Outcome != string(result.Outcome) || event.Detail != err.Error() || event.RefusalCode != result.RefusalCode {
		t.Fatalf("selected audit event differs from actual create failure: event=%+v result=%+v err=%v", event, result, err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("missing-path create failure mutated owned root: entries=%v err=%v", entries, err)
	}
}

func TestPeekWorktreeMergeReceiptPreservesResolverFilesystemFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	if err := os.Mkdir(candidate, 0700); err != nil {
		t.Fatal(err)
	}
	home, err := wbhome.Root(root)
	if err != nil {
		t.Fatal(err)
	}
	reports := filepath.Join(home, "reports", "worktree-merge")
	result, err := PeekWorktreeMergeReceipt(root, candidate)
	var pathErr *os.PathError
	if !errors.Is(err, os.ErrNotExist) || !errors.As(err, &pathErr) || pathErr.Path != reports || !reflect.DeepEqual(result, WorktreeMergeReceipt{}) {
		t.Fatalf("peek lost actual resolver filesystem error or invented receipt: result=%+v err=%v", result, err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 1 || entries[0].Name() != "candidate" {
		t.Fatalf("failed peek created report store or mutated input: entries=%v err=%v", entries, err)
	}
	if entries, err := os.ReadDir(candidate); err != nil || len(entries) != 0 {
		t.Fatalf("failed peek mutated candidate: entries=%v err=%v", entries, err)
	}
}
