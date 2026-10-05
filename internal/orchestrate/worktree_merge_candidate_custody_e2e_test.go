//go:build e2e

package orchestrate

import (
	"errors"
	"github.com/sneat-dev/wb/internal/runner"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestE2ECandidateCustodyNativeAdmissionsAndSupersessionDescendant(t *testing.T) {
	t.Parallel()
	f := newConflictRecoveryFixture(t)
	exact := f.receipt
	exact.Candidate.SHA = f.head
	before := exact
	claim, err := validateMergeAcknowledgementCandidate(t.Context(), f.engine.githubDir, exact, exact.Candidate)
	if err != nil || claim == nil || claim.Task != exact.Candidate.Task || claim.BaseSHA != exact.TargetSHA || !reflect.DeepEqual(exact, before) {
		t.Fatalf("native exact custody claim=%+v error=%v", claim, err)
	}
	exact.Status = WorktreeMergePrepared
	if claim, head, err := validatePrepareFailureSupersessionCandidate(t.Context(), f.engine.githubDir, exact); err != nil || claim == nil || head != "" {
		t.Fatalf("ordinary custody head=%q claim=%+v error=%v", head, claim, err)
	}
	exact.Status = WorktreeMergeConflict
	if claim, head, err := validatePrepareFailureSupersessionCandidate(t.Context(), f.engine.githubDir, exact); err != nil || claim == nil || head != "" {
		t.Fatalf("unchanged conflict custody head=%q error=%v", head, err)
	}
	for _, status := range []WorktreeMergeStatus{WorktreeMergeConflict, WorktreeMergeValidationFailed} {
		descendant := f.receipt
		descendant.Status = status
		if claim, head, err := validatePrepareFailureSupersessionCandidate(t.Context(), f.engine.githubDir, descendant); err != nil || claim == nil || head != f.head {
			t.Fatalf("native recoverable descendant status=%s head=%q want=%q error=%v", status, head, f.head, err)
		}
	}
	replacement := createMergeSource(t, f.engine, "custody-replacement", "wb/custody-replacement", "replacement.txt", "replacement\n")
	replacementSHA := strings.TrimSpace(runEngineGit(t, replacement.WorktreeDir, "rev-parse", "HEAD"))
	candidate, claim, err := validateValidationFailureReplacement(t.Context(), f.engine.githubDir, exact, replacement.WorktreeDir)
	want := WorktreeMergeCandidate{Task: "custody-replacement", Worktree: replacement.WorktreeDir, Branch: replacement.Branch, SHA: replacementSHA}
	if err != nil || claim == nil || candidate != want || claim.BaseSHA != replacement.BaseSHA {
		t.Fatalf("native replacement=%+v want=%+v claim=%+v error=%v", candidate, want, claim, err)
	}
}

func TestE2ECandidateCustodyNativeAdmissionRefusals(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"missing", "canonical", "branch", "HEAD", "repository", "task", "base", "dirty", "prompts"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			f := newConflictRecoveryFixture(t)
			receipt := f.receipt
			receipt.Candidate.SHA = f.head
			path := receipt.Candidate.Worktree
			both := true
			switch fault {
			case "missing":
				path = filepath.Join(t.TempDir(), "missing")
				receipt.Candidate.Worktree = path
			case "canonical":
				path = f.engine.canonical
				receipt.Candidate.Worktree = path
			case "branch":
				receipt.Candidate.Branch = "wb/other"
				both = false
			case "HEAD":
				receipt.Candidate.SHA = receipt.TargetSHA
				both = false
			case "repository":
				receipt.Repository = "acme/other"
			case "task":
				receipt.Candidate.Task = "other"
				both = false
			case "base":
				receipt.Target = "develop"
			case "dirty":
				writeEngineFile(t, filepath.Join(path, "dirty.txt"), "private dirty data\n")
			case "prompts":
				prompts := filepath.Join(path, ".wb", "local", "prompts")
				if err := os.RemoveAll(prompts); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(prompts, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := receipt
			if claim, err := validateMergeAcknowledgementCandidate(t.Context(), f.engine.githubDir, receipt, receipt.Candidate); err == nil || claim != nil || !reflect.DeepEqual(receipt, before) {
				t.Fatalf("native candidate fault=%s claim=%+v error=%v", fault, claim, err)
			}
			if both {
				if candidate, claim, err := validateValidationFailureReplacement(t.Context(), f.engine.githubDir, receipt, path); err == nil || claim != nil || candidate != (WorktreeMergeCandidate{}) || !reflect.DeepEqual(receipt, before) {
					t.Fatalf("native replacement fault=%s candidate=%+v claim=%+v error=%v", fault, candidate, claim, err)
				}
			}
		})
	}
}

func TestE2ECandidateCustodyExactNegativeReadsRetainNativePreconditions(t *testing.T) {
	t.Parallel()
	f := newConflictRecoveryFixture(t)
	receipt := f.receipt
	receipt.Candidate.SHA = f.head
	sentinel := errors.New("controlled exact custody read refused")
	for _, tc := range []struct{ name, args string }{
		{"cleanliness", "status --porcelain=v1"},
		{"HEAD", "rev-parse --verify HEAD^{commit}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := conflictNegativeRunner{Runner: runner.New(), dir: receipt.Candidate.Worktree, argv: tc.args, err: sentinel}
			if claim, err := validateMergeAcknowledgementCandidateWithRunner(t.Context(), run, f.engine.githubDir, receipt, receipt.Candidate); claim != nil || !errors.Is(err, sentinel) {
				t.Fatalf("candidate exact read claim=%+v error=%v", claim, err)
			}
			if candidate, claim, err := validateValidationFailureReplacementWithRunner(t.Context(), run, f.engine.githubDir, receipt, receipt.Candidate.Worktree); candidate != (WorktreeMergeCandidate{}) || claim != nil || !errors.Is(err, sentinel) {
				t.Fatalf("replacement exact read claim=%+v error=%v", claim, err)
			}
			if claim, head, err := validatePrepareFailureSupersessionCandidateWithRunner(t.Context(), run, f.engine.githubDir, f.receipt); claim != nil || head != "" || !errors.Is(err, sentinel) {
				t.Fatalf("supersession exact read head=%q claim=%+v error=%v", head, claim, err)
			}
		})
	}
	// Ordinary states delegate to exact candidate custody, including its refusal.
	ordinary := receipt
	ordinary.Status = WorktreeMergePrepared
	ordinary.Candidate.Task = "other"
	if claim, head, err := validatePrepareFailureSupersessionCandidate(t.Context(), f.engine.githubDir, ordinary); claim != nil || head != "" || err == nil {
		t.Fatalf("ordinary delegation head=%q claim=%+v error=%v", head, claim, err)
	}
	run := conflictNegativeRunner{Runner: runner.New(), dir: f.receipt.Candidate.Worktree, argv: "merge-base " + f.receipt.Candidate.SHA + " " + f.head, err: sentinel}
	if claim, head, err := validatePrepareFailureSupersessionCandidateWithRunner(t.Context(), run, f.engine.githubDir, f.receipt); claim != nil || head != "" || !errors.Is(err, sentinel) || !strings.HasPrefix(err.Error(), "verify candidate descendant ancestry: ") {
		t.Fatalf("supersession descendant exact read head=%q error=%v", head, err)
	}
	extra := createMergeSource(t, f.engine, "custody-unrelated", "feature/custody-unrelated", "extra.txt", "extra\n")
	unrelated := f.receipt
	unrelated.Candidate.SHA = strings.TrimSpace(runEngineGit(t, extra.WorktreeDir, "rev-parse", "HEAD"))
	if claim, head, err := validatePrepareFailureSupersessionCandidate(t.Context(), f.engine.githubDir, unrelated); claim != nil || head != "" || err == nil || !strings.Contains(err.Error(), "is not a descendant") {
		t.Fatalf("native rewritten predecessor head=%q error=%v", head, err)
	}
}
