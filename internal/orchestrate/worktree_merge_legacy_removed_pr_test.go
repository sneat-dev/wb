//go:build e2e

package orchestrate

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

//nolint:paralleltest // The native GitHub shim uses process-wide environment variables.
func TestE2ELegacyRemovedPRAcknowledgementPreservesFailedReceiptAndRefusesRecreatedAssets(t *testing.T) {
	f, _, r, _ := landedTerminalCleanupFixture(t)
	runEngineGit(t, f.canonical, "commit", "--allow-empty", "-m", "server legacy merge")
	landing := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, f.canonical, "push", "origin", "main")
	r.Cleanup = true
	r.Route.Route = WorktreeMergeRoutePullRequest
	r.PullRequest = "https://github.com/acme/app/pull/168"
	r.PublishedCandidateSHA = r.Candidate.SHA
	r.LandingSHA = landing
	r.Status = WorktreeMergePostTargetCIFailed
	r.Checks.Status = githubchecks.PullRequestWaitFailed
	r.Checks.Head = landing
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	externallyTerminalizeMergeCleanup(t, f, &r)
	installStrandedLandingGH(t, r.PullRequest, f.repository.CloneURL)
	t.Setenv("WB_TEST_PR_STATE", "MERGED")
	t.Setenv("WB_TEST_CANDIDATE_SHA", r.Candidate.SHA)
	t.Setenv("WB_TEST_MERGE_COMMIT_SHA", landing)
	t.Setenv("WB_TEST_TARGET_SHA", landing)
	o := WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, LegacyRemovedPR: true, Actor: "reviewer", Reason: "legacy exact PR absence"}
	r, err := readWorktreeMergeReceipt(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := AcknowledgeLandedMergeFailure(t.Context(), o)
	if err != nil || preview.LegacyRemovedPR != r.PullRequest || preview.ClaimBaseSHA != "" || preview.ReceiptSHA256 == "" {
		t.Fatalf("preview=%+v error=%v", preview, err)
	}
	if _, err := os.Stat(preview.AcknowledgementPath); !os.IsNotExist(err) {
		t.Fatalf("preview persisted: %v", err)
	}
	for _, row := range []struct{ name, state, head, merge string }{
		{name: "unmerged", state: "OPEN"}, {name: "advanced head", head: landing}, {name: "wrong landing", merge: r.Candidate.SHA},
	} {
		//nolint:paralleltest // Rows mutate the process-wide native GitHub shim.
		t.Run(row.name, func(t *testing.T) {
			state, merge := "MERGED", landing
			if row.state != "" {
				state = row.state
			}
			if row.merge != "" {
				merge = row.merge
			}
			t.Setenv("WB_TEST_PR_STATE", state)
			t.Setenv("WB_TEST_PR_HEAD_SHA", row.head)
			t.Setenv("WB_TEST_MERGE_COMMIT_SHA", merge)
			if _, err := AcknowledgeLandedMergeFailure(t.Context(), o); err == nil || !strings.Contains(err.Error(), "exact merged head") {
				t.Fatalf("accepted wrong PR: %v", err)
			}
		})
	}
	if err := os.MkdirAll(r.Candidate.Worktree, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := AcknowledgeLandedMergeFailure(t.Context(), o); err == nil || !strings.Contains(err.Error(), "must be absent") {
		t.Fatalf("recreated checkout: %v", err)
	}
	if err := os.Remove(r.Candidate.Worktree); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, f.canonical, "branch", r.Candidate.Branch, r.Candidate.SHA)
	if _, err := AcknowledgeLandedMergeFailure(t.Context(), o); err == nil || !strings.Contains(err.Error(), "local branch") {
		t.Fatalf("recreated branch: %v", err)
	}
	runEngineGit(t, f.canonical, "branch", "-D", r.Candidate.Branch)
	for _, row := range []struct {
		name    string
		args    []string
		ordinal int
	}{
		{"first absence read", []string{"worktree", "list", "--porcelain"}, 1},
		{"late absence read", []string{"worktree", "list", "--porcelain"}, 2},
		{"fetch", []string{"fetch", "--no-tags", "origin", "+refs/heads/main:refs/remotes/origin/main"}, 1},
		{"root ancestry", []string{"merge-base", r.TargetSHA, landing}, 1},
		{"source ancestry", []string{"merge-base", r.Sources[0].SHA, r.Candidate.SHA}, 1},
		{"base ancestry", []string{"merge-base", r.TargetSHA, r.Candidate.SHA}, 1},
	} {
		//nolint:paralleltest // Rows re-acquire the same native recovery lane.
		t.Run(row.name, func(t *testing.T) {
			sentinel := errors.New("selected native observation")
			run := &landedFailureOwnerRunner{Runner: defaultRunner, path: f.canonical, args: row.args, ordinal: row.ordinal, sentinel: sentinel}
			if _, err := acknowledgeLegacyRemovedPRFailure(t.Context(), o, r, r.ReceiptPath, run, readWorktreeMergeReceipt, persistLandedFailureAcknowledgement); !errors.Is(err, sentinel) {
				t.Fatalf("native observation error: %v", err)
			}
		})
	}
	for _, mode := range []string{"read error", "changed tuple", "missing bytes", "late changed bytes", "late missing bytes"} {
		//nolint:paralleltest // Rows mutate and restore the same immutable fixture file.
		t.Run(mode, func(t *testing.T) {
			t.Cleanup(func() {
				if err := os.WriteFile(r.ReceiptPath, before, 0600); err != nil {
					t.Fatal(err)
				}
			})
			read := readWorktreeMergeReceipt
			run := runner.Runner(defaultRunner)
			switch mode {
			case "read error":
				read = func(string) (WorktreeMergeReceipt, error) { return WorktreeMergeReceipt{}, errors.New("selected read") }
			case "changed tuple":
				read = func(string) (WorktreeMergeReceipt, error) {
					changed := r
					changed.Failure += "changed"
					return changed, nil
				}
			case "missing bytes":
				read = func(string) (WorktreeMergeReceipt, error) {
					if err := os.Remove(r.ReceiptPath); err != nil {
						t.Fatal(err)
					}
					return r, nil
				}
			default:
				run = &legacyRemovedMutationRunner{Runner: defaultRunner, mutate: func() {
					if mode == "late missing bytes" {
						if err := os.Remove(r.ReceiptPath); err != nil {
							t.Fatal(err)
						}
					} else {
						if err := os.WriteFile(r.ReceiptPath, append(before, '\n'), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}}
			}
			if _, err := acknowledgeLegacyRemovedPRFailure(t.Context(), o, r, r.ReceiptPath, run, read, persistLandedFailureAcknowledgement); err == nil {
				t.Fatal("unstable receipt accepted")
			}
		})
	}
	// An absent path still registered in Git is interrupted cleanup, not evidence.
	assets, _ := terminalWorkLogExpectations(r)
	registered := newAlreadyLandedScriptedRunner(map[string]string{"worktree list --porcelain": "worktree " + r.Candidate.Worktree + "\n"})
	if err := requireLegacyRemovedPRAssetsAbsent(t.Context(), registered, o.ProjectsRoot, f.canonical, r, assets); err == nil {
		t.Fatal("registered missing checkout accepted")
	}
	if err := requireLegacyRemovedPRAssetsAbsent(t.Context(), defaultRunner, o.ProjectsRoot, f.canonical, r, []worktrees.TerminalWorkLogExpectation{{Worktree: f.canonical}}); err == nil {
		t.Fatal("canonical checkout identity accepted")
	}
	noAudit := o
	noAudit.Apply = true
	noAudit.Actor = ""
	if _, err := acknowledgeLegacyRemovedPRFailure(t.Context(), noAudit, r, r.ReceiptPath, defaultRunner, readWorktreeMergeReceipt, persistLandedFailureAcknowledgement); err == nil {
		t.Fatal("missing operator accepted")
	}
	invalid := r
	invalid.Status = WorktreeMergePrepared
	if _, err := acknowledgeLegacyRemovedPRFailure(t.Context(), o, invalid, r.ReceiptPath, defaultRunner, readWorktreeMergeReceipt, persistLandedFailureAcknowledgement); err == nil {
		t.Fatal("wrong phase accepted")
	}
	for _, row := range []struct{ name, key, want string }{
		{"target containment", "merge-base " + r.TargetSHA + " " + landing, "does not contain recorded root"},
		{"source containment", "merge-base " + r.Sources[0].SHA + " " + r.Candidate.SHA, "does not contain exact receipted source"},
		{"base containment", "merge-base " + r.TargetSHA + " " + r.Candidate.SHA, "does not contain immutable receipt base"},
	} {
		//nolint:paralleltest // Each observation owns the same fixture operation lane.
		t.Run(row.name, func(t *testing.T) {
			run := newAlreadyLandedScriptedRunner(map[string]string{row.key: "unrelated\n"})
			if _, err := acknowledgeLegacyRemovedPRFailure(t.Context(), o, r, r.ReceiptPath, run, readWorktreeMergeReceipt, persistLandedFailureAcknowledgement); err == nil || !strings.Contains(err.Error(), row.want) {
				t.Fatalf("containment refusal: %v", err)
			}
		})
	}
	lock, err := AcquireOperationLock(o.ProjectsRoot, r.Lane, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acknowledgeLegacyRemovedPRFailure(t.Context(), o, r, r.ReceiptPath, defaultRunner, readWorktreeMergeReceipt, persistLandedFailureAcknowledgement); err == nil {
		t.Fatal("live operation lock accepted")
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	badRepository := r
	badRepository.Repository = "invalid"
	badRepository.Lane = worktreeMergeLaneID(badRepository.Repository, r.Target)
	badRepository.ID = worktreeMergeOperationID(badRepository.Lane, r.Sources)
	badRepository.Candidate.Task = badRepository.ID
	if _, err := acknowledgeLegacyRemovedPRFailure(t.Context(), o, badRepository, r.ReceiptPath, defaultRunner, func(string) (WorktreeMergeReceipt, error) { return badRepository, nil }, persistLandedFailureAcknowledgement); err == nil {
		t.Fatal("invalid canonical repository accepted")
	}
	for _, row := range []struct{ name, env, value, want string }{
		{"foreign response repository", "WB_TEST_PR_URL", "https://github.com/other/app/pull/168", "foreign identity"},
		{"foreign response number", "WB_TEST_PR_URL", "https://github.com/acme/app/pull/169", "not receipted"},
		{"foreign numeric identity", "WB_TEST_PR_NUMBER", "169", "not receipted number"},
		{"missing response URL", "WB_TEST_PR_URL", "not-a-pull-request-url", "foreign identity"},
	} {
		//nolint:paralleltest // Rows mutate the process-wide native GitHub shim.
		t.Run(row.name, func(t *testing.T) {
			t.Setenv(row.env, row.value)
			if _, err := AcknowledgeLandedMergeFailure(t.Context(), o); err == nil || !strings.Contains(err.Error(), row.want) {
				t.Fatalf("foreign response identity accepted: %v", err)
			}
		})
	}
	gh, err := exec.LookPath("gh")
	if err != nil {
		t.Fatal(err)
	}
	originalGH, err := os.ReadFile(gh)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"#!/bin/sh\nexit 1\n", "#!/bin/sh\nprintf 'invalid-json'\n"} {
		//nolint:paralleltest // Cases replace and restore this fixture's private gh shim.
		t.Run(body, func(t *testing.T) {
			t.Cleanup(func() {
				if err := os.WriteFile(gh, originalGH, 0700); err != nil {
					t.Fatal(err)
				}
			})
			if err := os.WriteFile(gh, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := acknowledgeLegacyRemovedPRFailure(t.Context(), o, r, r.ReceiptPath, defaultRunner, readWorktreeMergeReceipt, persistLandedFailureAcknowledgement); err == nil {
				t.Fatal("unavailable PR proof accepted")
			}
		})
	}
	// The explicit variant is independent of terminal cleanup history. Remove only
	// this temporary fixture's proof directory, never a real WB record.
	if err := os.RemoveAll(filepath.Join(f.githubDir, ".wb", "reports", "worktree-cleanup")); err != nil {
		t.Fatal(err)
	}
	o.Apply = true
	a, err := AcknowledgeLandedMergeFailure(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := hasLandedFailureAcknowledgement(r); err != nil || !ok {
		t.Fatalf("lane ack=%t %v", ok, err)
	}
	after, err := os.ReadFile(r.ReceiptPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("failed receipt changed: %v", err)
	}
	if a.ReceiptStatus != WorktreeMergePostTargetCIFailed {
		t.Fatalf("failed CI lost: %+v", a)
	}
	// Another repository can expose an identical candidate, base, and landing
	// commit; the receipt's own URL must still name the receipted repository.
	foreign := r
	foreign.PullRequest = "https://github.com/other/app/pull/168"
	if err := persistWorktreeMergeReceipt(foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := AcknowledgeLandedMergeFailure(t.Context(), o); err == nil || !strings.Contains(err.Error(), "names repository") {
		t.Fatalf("foreign receipt repository accepted: %v", err)
	}
	if err := os.WriteFile(r.ReceiptPath, append(before, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLandedFailureAcknowledgement(a.AcknowledgementPath, r); err == nil {
		t.Fatal("receipt byte mutation accepted")
	}
}

// Mutates only the test fixture at the second physical absence observation.
type legacyRemovedMutationRunner struct {
	runner.Runner
	seen   int
	mutate func()
}

func (r *legacyRemovedMutationRunner) RunOpts(ctx context.Context, dir string, o runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if name == "git" && reflect.DeepEqual(args, []string{"worktree", "list", "--porcelain"}) {
		r.seen++
		if r.seen == 2 {
			r.mutate()
		}
	}
	return r.Runner.RunOpts(ctx, dir, o, name, args...)
}
