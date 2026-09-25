package main

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/strongo/cli-helpers/skillsync"
	skillscmd "github.com/strongo/cli-helpers/skillsync/cobracmd"
)

// failAfterWriter fails every Write once more than allowedWrites writes have
// been made, so a caller can force a specific fmt.Fprintf call inside a
// print function to observe an error and take its error-return branch.
type failAfterWriter struct {
	allowedWrites int
	calls         int
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls > w.allowedWrites {
		return 0, fmt.Errorf("zz_rwi03: forced write failure on call %d", w.calls)
	}
	return len(p), nil
}

// AC: cov-rwi-03 unit03 seam list, cmd/wb/skills_sync.go
// skillsSyncErrors.Failure. A *skillscmd.UsageError cause must surface as
// exitUsage, distinct from every other failure which is exitFindings.
func TestSkillsSyncErrorsFailureMapsAUsageErrorToExitUsage(t *testing.T) {
	t.Parallel()
	cause := &skillscmd.UsageError{Err: errors.New("--dir and --harness are mutually exclusive")}
	err := skillsSyncErrors{}.Failure(cause)
	var coded *exitError
	if !errors.As(err, &coded) {
		t.Fatalf("err = %v, not an *exitError", err)
	}
	if coded.code != exitUsage {
		t.Errorf("code = %d, want exitUsage (%d)", coded.code, exitUsage)
	}
}

func TestSkillsSyncErrorsFailureMapsAnOrdinaryErrorToExitFindings(t *testing.T) {
	t.Parallel()
	err := skillsSyncErrors{}.Failure(errors.New("legacy marker unreadable"))
	var coded *exitError
	if !errors.As(err, &coded) {
		t.Fatalf("err = %v, not an *exitError", err)
	}
	if coded.code != exitFindings {
		t.Errorf("code = %d, want exitFindings (%d)", coded.code, exitFindings)
	}
	if !errors.Is(err, err) || coded.message == "" {
		t.Errorf("message is empty")
	}
}

// AC: cov-rwi-03 unit03 seam list, cmd/wb/skills_sync.go writeSkillsSyncJSON.
// Zero targets must print nothing rather than an empty JSON array/null --
// there is nothing to report when a selector matched no harness.
func TestWriteSkillsSyncJSONWithNoResultsWritesNothing(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := writeSkillsSyncJSON(&out, nil); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("output = %q, want empty", out.String())
	}
}

// AC: cov-rwi-03 unit03 seam list, cmd/wb/skills_sync.go writeSkillsSyncText.
// Each of its Fprintf calls returns early on a write error; a writer that
// fails on exactly the Nth call proves that specific call's error path
// without needing the others to fail too.
func TestWriteSkillsSyncTextPropagatesTheHeaderLineWriteFailure(t *testing.T) {
	t.Parallel()
	w := &failAfterWriter{allowedWrites: 0}
	err := writeSkillsSyncText(w, skillscmd.TargetResult{
		Dir:    "/tmp/claude/skills",
		Report: skillsync.Report{Dir: "/tmp/claude/skills"},
	})
	if err == nil {
		t.Fatal("expected the header Fprintf failure to propagate")
	}
}

func TestWriteSkillsSyncTextPropagatesAnActionLineWriteFailure(t *testing.T) {
	t.Parallel()
	// allowedWrites=1 lets the "wb skills synced: <dir>" header through, then
	// fails the very next Fprintf -- the first action line ("added: ...").
	w := &failAfterWriter{allowedWrites: 1}
	err := writeSkillsSyncText(w, skillscmd.TargetResult{
		Dir: "/tmp/claude/skills",
		Report: skillsync.Report{
			Dir: "/tmp/claude/skills",
			Changes: []skillsync.Change{
				{Name: "wb-worktrees", Action: skillsync.Added},
			},
		},
	})
	if err == nil {
		t.Fatal("expected the action-line Fprintf failure to propagate")
	}
}

func TestWriteSkillsSyncTextPropagatesTheFailedTargetHeaderWriteFailure(t *testing.T) {
	t.Parallel()
	// A failed target prints "wb skills sync failed: <dir>" before its error
	// line; failing the very first write must surface, not be swallowed by
	// the second Fprintf that follows it.
	w := &failAfterWriter{allowedWrites: 0}
	err := writeSkillsSyncText(w, skillscmd.TargetResult{
		Dir: "/tmp/claude/skills",
		Err: errors.New("legacy marker content differs"),
	})
	if err == nil {
		t.Fatal("expected the failed-target header Fprintf failure to propagate")
	}
}

// AC: cov-rwi-03 unit03 seam list, cmd/wb/skills_sync.go skillsSyncPayload.
// Status is derived from the report: conflicts outrank a plain "changed",
// which outranks "unchanged" -- checked here directly against the payload
// rather than through JSON rendering.
func TestSkillsSyncPayloadReportsConflictStatusOverChanged(t *testing.T) {
	t.Parallel()
	payload := skillsSyncPayload(skillscmd.TargetResult{
		Dir: "/tmp/claude/skills",
		Report: skillsync.Report{
			Changes: []skillsync.Change{
				{Name: "wb-worktrees", Action: skillsync.Added},
				{Name: "wb-hooks", Action: skillsync.Conflict},
			},
		},
	})
	if payload.Status != "conflict" {
		t.Errorf("status = %q, want conflict", payload.Status)
	}
}

func TestSkillsSyncPayloadReportsChangedStatusWhenNothingConflicts(t *testing.T) {
	t.Parallel()
	payload := skillsSyncPayload(skillscmd.TargetResult{
		Dir: "/tmp/claude/skills",
		Report: skillsync.Report{
			Changes: []skillsync.Change{
				{Name: "wb-worktrees", Action: skillsync.Added},
			},
		},
	})
	if payload.Status != "changed" {
		t.Errorf("status = %q, want changed", payload.Status)
	}
}
