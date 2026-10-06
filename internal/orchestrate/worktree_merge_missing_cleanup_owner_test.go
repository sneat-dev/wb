package orchestrate

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

func TestMissingCleanupOwnerEntryStagesRetainNativeReceipt(t *testing.T) {
	t.Parallel()
	f, r := landedFailureOwnerFixture(t)
	before, e := os.ReadFile(r.ReceiptPath)
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range []struct {
		name  string
		at    int
		want  string
		apply bool
	}{
		{"initial read", 1, "selected initial read", false},
		{"held second read", 2, "selected held second read", false},
		{"lane", 0, "has no lane identity", false},
		{"audit", 0, "--actor and --reason", true},
	} {
		//nolint:paralleltest // Rows reuse one actual receipt and operation lane, including held-lock second reads and the final unchanged-byte check.
		t.Run(row.name, func(t *testing.T) {
			count := 0
			sentinel := errors.New(row.want)
			consumed := false
			read := func(path string) (WorktreeMergeReceipt, error) {
				count++
				native, err := readWorktreeMergeReceipt(path)
				if err != nil {
					return native, err
				}
				if count == row.at {
					consumed = true
					if count == 2 {
						lock, err := AcquireOperationLock(f.githubDir, r.Lane, true)
						if err == nil {
							_ = lock.Release()
							t.Fatal("second read outside actual lane")
						}
					}
					return WorktreeMergeReceipt{}, sentinel
				}
				if row.name == "lane" {
					native.Lane = ""
				}
				return native, nil
			}
			_, err := acknowledgeMissingWorktreeMergeCleanup(t.Context(), WorktreeMergeMissingCleanupAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: row.apply}, defaultRunner, read, worktreeMergeReceiptSHA256, persistMissingCleanupAcknowledgement)
			if err == nil || !strings.Contains(err.Error(), row.want) {
				t.Fatalf("stage=%s error=%v", row.name, err)
			}
			if row.at > 0 && (!consumed || !errors.Is(err, sentinel)) {
				t.Fatalf("read cause=%v consumed=%t", err, consumed)
			}
		})
	}
	after, e := os.ReadFile(r.ReceiptPath)
	if e != nil || string(after) != string(before) {
		t.Fatalf("native receipt changed: %v", e)
	}
}

func TestMissingCleanupNativeInputPoliciesRefuseBeforeEffects(t *testing.T) {
	t.Parallel()
	f, r := landedFailureOwnerFixture(t)
	for _, row := range []struct {
		name, want string
		change     func(*WorktreeMergeReceipt)
	}{
		{"status", "exact landed cleanup-pending", func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePrepared }},
		{"checks", "completed exact checks", func(r *WorktreeMergeReceipt) { r.Checks.Status = githubchecks.PullRequestWaitFailed }},
		{"candidate", "exact landed cleanup-pending", func(r *WorktreeMergeReceipt) { r.Candidate.Task = "" }},
		{"source", "exact source identity", func(r *WorktreeMergeReceipt) { r.Sources[0].Branch = "" }},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			changed := r
			changed.Sources = append([]WorktreeMergeSource(nil), r.Sources...)
			changed.Status = WorktreeMergeLanded
			changed.Phase = WorktreeMergePhaseLand
			changed.Cleanup = true
			changed.LandingSHA = r.Candidate.SHA
			changed.Checks.Status = githubchecks.PullRequestWaitPassed
			changed.CanonicalSync = "fast_forwarded"
			row.change(&changed)
			_, e := inspectMissingWorktreeMergeCleanup(t.Context(), defaultRunner, worktreeMergeReceiptSHA256, f.githubDir, changed, "reviewer", "negative record", 0, 0)
			if e == nil || !strings.Contains(e.Error(), row.want) {
				t.Fatalf("negative input %s=%v", row.name, e)
			}
		})
	}
}
