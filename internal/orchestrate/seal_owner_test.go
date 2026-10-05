package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// sealOwnerRunner only refuses selected native observations or mutates a private
// fixture around a real command. It never supplies successful Git/custody output.
type sealOwnerRunner struct {
	runner.Runner
	before func(context.Context, string, string, []string) error
	after  func(context.Context, string, string, []string, runner.Result, error)
}

func (r *sealOwnerRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if r.before != nil {
		if err := r.before(ctx, dir, name, args); err != nil {
			return runner.Result{}, err
		}
	}
	result, err := r.Runner.RunOpts(ctx, dir, opts, name, args...)
	if r.after != nil {
		r.after(ctx, dir, name, args, result, err)
	}
	return result, err
}

// This is the original seal's historical validation-failure record recipe,
// rooted explicitly. Native Prepare/claims/locks/Git are real; recording an
// earlier failure is test input, not a claim that the original validation failed.
func sealOwnerFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt, worktrees.CreateResult) {
	t.Helper()
	return sealOwnerFixtureWithFixture(t, newExplicitRootEngineFixture(t))
}

func sealOwnerFixtureWithFixture(t *testing.T, f engineFixture) (engineFixture, WorktreeMergeReceipt, worktrees.CreateResult) {
	t.Helper()
	source := createMergeSource(t, f, "seal-owner-source", "feature/seal-owner-source", "source.txt", "source\n")
	r, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	r.Status = WorktreeMergeValidationFailed
	r.Failure = "historical validation failure"
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	return f, r, source
}
func sealOwnerOptions(f engineFixture, r WorktreeMergeReceipt) WorktreeMergeValidationFailureSealOptions {
	return WorktreeMergeValidationFailureSealOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "reviewed no-content recovery", Model: "test-model", AgentRuntime: "test", Timeout: time.Minute}
}
func sealOwnerAssertReleased(t *testing.T, f engineFixture, r WorktreeMergeReceipt) {
	t.Helper()
	lock, err := AcquireOperationLock(f.githubDir, r.Lane, true)
	if err != nil {
		t.Fatalf("lane retained: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}
func sealOwnerEntryRecord(r WorktreeMergeReceipt) WorktreeMergeReceipt {
	r.Sources = append([]WorktreeMergeSource(nil), r.Sources...)
	return r
}

func TestSealOwnerEntryContractsAndNativeDryRun(t *testing.T) {
	t.Parallel()
	f, r, source := sealOwnerFixture(t)
	receiptBytes, candidateBytes, sourceBytes := validationFailureSealImmutableBytes(t, f, r, source)
	sentinel := errors.New("selected receipt read")
	cases := []struct {
		name, want string
		edit       func(*WorktreeMergeReceipt)
		readErr    error
		actor      string
		dry        bool
	}{
		{name: "read failure", readErr: sentinel},
		{name: "wrong phase", want: "want prepare validation_failed", edit: func(r *WorktreeMergeReceipt) { r.Phase = WorktreeMergePhaseLand }},
		{name: "wrong status", want: "want prepare validation_failed", edit: func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePrepared }},
		{name: "landing", want: "want prepare validation_failed", edit: func(r *WorktreeMergeReceipt) { r.LandingSHA = r.Candidate.SHA }},
		{name: "receipt identity", want: "inconsistent immutable receipt identity", edit: func(r *WorktreeMergeReceipt) { r.ID = "" }},
		{name: "incomplete candidate", want: "lacks complete immutable", edit: func(r *WorktreeMergeReceipt) { r.Candidate.SHA = "" }},
		{name: "audit identity", want: "--actor and --reason", actor: " "},
		{name: "native dry run", dry: true},
	}
	for _, tc := range cases {
		//nolint:paralleltest // Rows reuse an immutable native receipt and inspect the same lane synchronously.
		t.Run(tc.name, func(t *testing.T) {
			options := sealOwnerOptions(f, r)
			if tc.actor != "" {
				options.Actor = tc.actor
			}
			options.Apply = !tc.dry
			read := func(path string) (WorktreeMergeReceipt, error) {
				if path != r.ReceiptPath {
					t.Fatalf("read path %s", path)
				}
				if tc.readErr != nil {
					return WorktreeMergeReceipt{}, tc.readErr
				}
				nativeReceipt, err := readWorktreeMergeReceipt(path)
				if err != nil {
					return WorktreeMergeReceipt{}, err
				}
				value := sealOwnerEntryRecord(nativeReceipt)
				if tc.edit != nil {
					tc.edit(&value)
				}
				return value, nil
			}
			got, err := prepareValidationFailedWorktreeMergeSeal(t.Context(), options, defaultRunner, read, worktreeMergeReceiptSHA256)
			if tc.readErr != nil {
				if !errors.Is(err, tc.readErr) {
					t.Fatalf("read cause %v", err)
				}
			} else if tc.dry {
				if err != nil || got.Status != "validation_failure_seal_planned" || got.Candidate.Worktree != "" || len(got.RequiredRoots) != 4 {
					t.Fatalf("native planned result %+v err=%v", got, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v want %q", err, tc.want)
			}
			sealOwnerAssertReleased(t, f, r)
			assertValidationFailureSealImmutableBytes(t, f, r, source, receiptBytes, candidateBytes, sourceBytes)
		})
	}
	_, err := prepareValidationFailedWorktreeMergeSeal(t.Context(), WorktreeMergeValidationFailureSealOptions{ProjectsRoot: f.githubDir}, defaultRunner, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256)
	if err == nil {
		t.Fatal("empty selector accepted")
	}
	lock, err := AcquireOperationLock(f.githubDir, r.Lane, false)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			released = true
			if err := lock.Release(); err != nil {
				t.Error(err)
			}
		}
	})
	_, err = prepareValidationFailedWorktreeMergeSeal(t.Context(), sealOwnerOptions(f, r), defaultRunner, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256)
	if err == nil {
		t.Fatal("held lane accepted")
	}
	released = true
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestSealOwnerRootsAndActualPromptBytes(t *testing.T) {
	t.Parallel()
	r := WorktreeMergeReceipt{ReceiptPath: "/private/receipt", Repository: "acme/app", Target: "main", TargetSHA: "target", Sources: []WorktreeMergeSource{{Task: "first", SHA: "source-a"}, {Task: "second", SHA: "source-b"}}}
	roots := validationFailureSealRoots("base", r, "current", map[string]string{"first": "descendant"})
	want := []WorktreeMergeValidationFailureSealRoot{{Kind: "failed_candidate_claim_base", SHA: "base"}, {Kind: "receipt_target", SHA: "target"}, {Kind: "current_remote_target", SHA: "current"}, {Kind: "receipted_source:first", SHA: "source-a"}, {Kind: "landed_source_descendant:first", SHA: "descendant"}, {Kind: "receipted_source:second", SHA: "source-b"}}
	if !reflect.DeepEqual(roots, want) {
		t.Fatalf("roots %+v", roots)
	}
	path, err := writeValidationFailureSealPrompt(r, "current", "tree", roots, "actor", "reason")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
	})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"receipt /private/receipt.\n", "Repository: acme/app\nTarget: main at current\nRequired tree: tree\n", "- landed_source_descendant:first descendant\n", "Actor: actor\nReason: reason\n"} {
		if !strings.Contains(string(data), part) {
			t.Fatalf("prompt missing %q: %s", part, data)
		}
	}
	if path != filepath.Clean(path) {
		t.Fatalf("unclean prompt path %s", path)
	}
}
