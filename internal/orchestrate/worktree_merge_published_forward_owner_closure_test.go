package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/runner"
	"os"
	"testing"
	"time"
)

type forwardClosureRunner struct {
	runner.Runner
	observe func(context.Context, string, string, []string) error
	after   func(context.Context, string, string, []string, runner.Result, error)
}

func (r forwardClosureRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if err := r.observe(ctx, dir, name, args); err != nil {
		return runner.Result{}, err
	}
	result, err := r.Runner.RunOpts(ctx, dir, opts, name, args...)
	if r.after != nil {
		r.after(ctx, dir, name, args, result, err)
	}
	return result, err
}
func forwardClosureWriteRecord(t *testing.T, path string, value any) {
	t.Helper()
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
func forwardClosurePinnedBytes(t *testing.T, paths ...string) func() {
	t.Helper()
	saved := make(map[string][]byte, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		saved[p] = b
	}
	return func() {
		t.Helper()
		for p, b := range saved {
			got, err := os.ReadFile(p)
			if err != nil || string(got) != string(b) {
				t.Fatalf("native immutable bytes changed: %s: %v", p, err)
			}
		}
	}
}
func forwardClosureCorrection(t *testing.T, r WorktreeMergeReceipt, s WorktreeMergeValidationFailureSupersession, o WorktreeMergePublishedForwardRepairOptions) WorktreeMergeSelfSupersessionCorrection {
	t.Helper()
	candidate, claim, err := validateValidationFailureReplacement(t.Context(), o.ProjectsRoot, r, o.Sources[1])
	if err != nil {
		t.Fatal(err)
	}
	c := WorktreeMergeSelfSupersessionCorrection{SchemaVersion: worktreeMergeSelfSupersessionCorrectionSchemaVersion, Status: "validation_failure_self_supersession_corrected", CorrectionPath: selfSupersessionCorrectionPath(r.ReceiptPath), ReceiptPath: r.ReceiptPath, ReceiptSHA256: o.ExpectedReceiptSHA256, ImmutableClaimSHA256: o.ExpectedImmutableClaimSHA256, SupersessionPath: s.AcknowledgementPath, SupersessionSHA256: o.ExpectedSupersessionSHA256, SupersessionID: s.ID, OriginalCandidate: r.Candidate, OriginalClaimBaseSHA: s.OriginalClaimBaseSHA, CorrectedReplacement: candidate, ReplacementClaimBaseSHA: claim.BaseSHA, CurrentTargetSHA: s.CurrentTargetSHA, Sources: append([]WorktreeMergeSource(nil), r.Sources...), Actor: "record-observer", Reason: "binding refusal fixture, not a positive native correction proof", RecordedAt: time.Now().UTC()}
	c.ID = selfSupersessionCorrectionID(c)
	return c
}
func forwardClosureAssertReleased(t *testing.T, root, lane string) {
	t.Helper()
	lock, err := AcquireOperationLock(root, lane, true)
	if err != nil {
		t.Fatalf("actual lane lock leaked: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

func forwardClosureNativeReplacement(t *testing.T, dir, head, replacement, ancestor string) func() {
	t.Helper()
	if contains, err := isMergeAncestorWithRunner(t.Context(), defaultRunner, dir, ancestor, head); err != nil || !contains {
		t.Fatalf("native pre-replacement ancestry not true: %v %v", contains, err)
	}
	installed := false
	restored := false
	restore := func() error {
		if restored {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if installed {
			if _, _, err := runCommand(ctx, defaultRunner, 0, 0, dir, "git", "replace", "-d", head); err != nil {
				return err
			}
			installed = false
		}
		contains, err := isMergeAncestorWithRunner(ctx, defaultRunner, dir, ancestor, head)
		if err != nil || !contains {
			return fmt.Errorf("restored native true ancestry=%v: %w", contains, err)
		}
		restored = true
		return nil
	}
	t.Cleanup(func() {
		if !restored {
			if err := restore(); err != nil {
				t.Errorf("fallback native Git restoration: %v", err)
			}
		}
	})
	installed = true
	runEngineGit(t, dir, "replace", head, replacement)
	return func() {
		t.Helper()
		if err := restore(); err != nil {
			t.Fatalf("immediate native Git restoration: %v", err)
		}
	}
}
