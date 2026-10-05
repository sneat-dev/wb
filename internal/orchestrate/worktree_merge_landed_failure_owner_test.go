package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLandedFailureOwnerEntryRefusalsPreserveNativeRecords(t *testing.T) {
	t.Parallel()
	f, r := landedFailureOwnerFixture(t)
	before, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("selected receipt read")
	for _, row := range []struct {
		name, want string
		mutate     func(*WorktreeMergeReceipt)
		readError  bool
		apply      bool
	}{
		{name: "read", readError: true, want: sentinel.Error()},
		{name: "lane", want: "inconsistent immutable receipt identity", mutate: func(r *WorktreeMergeReceipt) { r.Lane = "" }},
		{name: "status", want: "want prepare validation_failed", mutate: func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePrepared }},
		{name: "phase", want: "invalid prepare failure state", mutate: func(r *WorktreeMergeReceipt) { r.Phase = WorktreeMergePhaseLand }},
		{name: "candidate identity", want: "lacks complete immutable", mutate: func(r *WorktreeMergeReceipt) { r.Candidate.Branch = "" }},
		{name: "sources", want: "lacks complete immutable", mutate: func(r *WorktreeMergeReceipt) { r.Sources = nil }},
		{name: "audit", want: "--actor and --reason", apply: true},
	} {
		//nolint:paralleltest // Rows reuse one native receipt and operation lane, with a final unchanged receipt-byte assertion after refusals.
		t.Run(row.name, func(t *testing.T) {
			changed := r
			changed.Sources = append([]WorktreeMergeSource(nil), r.Sources...)
			if row.mutate != nil {
				row.mutate(&changed)
			}
			_, got := acknowledgeLandedMergeFailure(t.Context(), WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: row.apply}, defaultRunner, func(path string) (WorktreeMergeReceipt, error) {
				if path != r.ReceiptPath {
					t.Fatalf("read path=%q", path)
				}
				if row.readError {
					return WorktreeMergeReceipt{}, sentinel
				}
				native, e := readWorktreeMergeReceipt(path)
				if e != nil {
					return native, e
				}
				return changed, nil
			}, func(string, WorktreeMergeLandedFailureAcknowledgement) error {
				t.Fatal("entry refusal persisted")
				return nil
			})
			if got == nil || !strings.Contains(got.Error(), row.want) {
				t.Fatalf("error=%v want=%q", got, row.want)
			}
			if row.readError && !errors.Is(got, sentinel) {
				t.Fatalf("lost read identity: %v", got)
			}
		})
	}
	after, err := os.ReadFile(r.ReceiptPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("receipt changed: %v", err)
	}
}

func TestLandedFailureFinalizerUsesRealSidecarsAndRenamePersistence(t *testing.T) {
	t.Parallel()
	f, r := landedFailureOwnerFixture(t)
	ack, err := AcknowledgeLandedMergeFailure(t.Context(), WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Actor: " reviewer ", Reason: " genuine native proof "})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dry run", "persist refusal", "native apply", "existing", "different target", "malformed"} {
		//nolint:paralleltest // Rows remove, replace and publish the same acknowledgement sidecar before testing each persistence outcome.
		t.Run(name, func(t *testing.T) {
			path := landedFailureAcknowledgementPath(r.ReceiptPath)
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Remove(path) })
			input := ack
			sentinel := errors.New("selected acknowledgement save")
			persist := persistLandedFailureAcknowledgement
			if name == "persist refusal" {
				persist = func(p string, a WorktreeMergeLandedFailureAcknowledgement) error {
					if p != path || a.ID != ack.ID {
						t.Fatalf("wrong save: %s %+v", p, a)
					}
					return sentinel
				}
			}
			if name == "existing" || name == "different target" {
				existing := ack
				if name == "different target" {
					existing.CurrentTargetSHA = r.TargetSHA
					existing.ID = landedFailureAcknowledgementID(existing)
				}
				if err := persistLandedFailureAcknowledgement(path, existing); err != nil {
					t.Fatal(err)
				}
			}
			if name == "malformed" {
				if err := os.WriteFile(path, []byte("{invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, e := finishLandedFailureAcknowledgement(r, input, name != "dry run", persist)
			switch name {
			case "persist refusal":
				if !errors.Is(e, sentinel) {
					t.Fatalf("save error=%v", e)
				}
			case "different target":
				if e == nil || !strings.Contains(e.Error(), "binds different target or candidate evidence") {
					t.Fatalf("mismatch=%v", e)
				}
			case "malformed":
				if e == nil {
					t.Fatal("malformed sidecar accepted")
				}
			default:
				if e != nil || got.ID != ack.ID {
					t.Fatalf("ack=%+v error=%v", got, e)
				}
			}
			if name == "dry run" || name == "persist refusal" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("refusal/dryrun wrote: %v", err)
				}
			}
			if name == "native apply" {
				stored, e := readLandedFailureAcknowledgement(path, r)
				if e != nil || stored.ID != ack.ID {
					t.Fatalf("native saved ack=%+v error=%v", stored, e)
				}
				info, e := os.Stat(path)
				if e != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("mode=%v error=%v", info, e)
				}
			}
		})
	}
}

func TestLandedFailureOwnerPathAndLockRefusals(t *testing.T) {
	t.Parallel()
	if _, err := acknowledgeLandedMergeFailure(t.Context(), WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: t.TempDir(), Receipt: "missing"}, defaultRunner, readWorktreeMergeReceipt, persistLandedFailureAcknowledgement); err == nil {
		t.Fatal("missing selector accepted")
	}
	f, r := landedFailureOwnerFixture(t)
	lock, err := AcquireOperationLock(f.githubDir, r.Lane, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	if _, err := AcknowledgeLandedMergeFailure(t.Context(), WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath}); err == nil {
		t.Fatal("held lane accepted")
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath}); err != nil {
		t.Fatalf("released lane not usable: %v", err)
	}
	if _, err := os.Stat(filepath.Clean(r.Candidate.Worktree)); err != nil {
		t.Fatal(err)
	}
}
