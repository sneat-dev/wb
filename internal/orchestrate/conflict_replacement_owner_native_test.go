package orchestrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type conflictReplacementNativeFixture struct {
	fixture                  engineFixture
	receipt                  WorktreeMergeReceipt
	options                  WorktreeMergeConflictCandidateRefreshOptions
	claimPath                string
	receiptBytes, claimBytes []byte
}

// All claims and DAG observations come from real private native Prepare/Guard/
// Work Log. The initial conflict is physically resolved without changing its receipt.
func newConflictReplacementNativeFixture(t *testing.T) conflictReplacementNativeFixture {
	t.Helper()
	return newConflictReplacementNativeFixtureWithFixture(t, newExplicitRootEngineFixture(t))
}

func newConflictReplacementNativeFixtureWithFixture(t *testing.T, f engineFixture) conflictReplacementNativeFixture {
	t.Helper()
	first := createMergeSource(t, f, "replacement-owner-first", "feature/replacement-owner-first", "shared.txt", "first\n")
	second := createMergeSource(t, f, "replacement-owner-second", "feature/replacement-owner-second", "shared.txt", "second\n")
	r, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{first.WorktreeDir, second.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err == nil || r.Status != WorktreeMergeConflict {
		t.Fatalf("native initial conflict=%+v err=%v", r, err)
	}
	cmd := exec.CommandContext(t.Context(), "git", "merge", "--no-commit", r.Sources[1].SHA)
	cmd.Dir = r.Candidate.Worktree
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("native second-source conflict unexpectedly succeeded %s", out)
	}
	writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "shared.txt"), "resolved\n")
	runEngineGit(t, r.Candidate.Worktree, "add", "shared.txt")
	runEngineGit(t, r.Candidate.Worktree, "commit", "-m", "test: resolve authentic conflict replacement input")
	claim, observed, err := validatePrepareFailureSupersessionCandidate(t.Context(), f.githubDir, r)
	if err != nil || observed == "" {
		t.Fatalf("native resolved input=%+v observed=%s err=%v", claim, observed, err)
	}
	rb, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := os.ReadFile(claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := worktreeMergeReceiptSHA256(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	target, err := fetchExactMergeTarget(t.Context(), r.Candidate.Worktree, r.Target)
	if err != nil {
		t.Fatal(err)
	}
	o := WorktreeMergeConflictCandidateRefreshOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Sources: []string{first.WorktreeDir, second.WorktreeDir}, ExpectedSourceSHAs: []string{r.Sources[0].SHA, r.Sources[1].SHA}, ExpectedReceiptSHA256: hash, ExpectedImmutableClaimSHA256: sha256Hex(cb), ExpectedCurrentTargetSHA: target, Actor: "reviewer", Reason: "native complete replacement contract", Timeout: 10 * time.Second}
	return conflictReplacementNativeFixture{f, r, o, claim.ClaimPath, rb, cb}
}

func (f conflictReplacementNativeFixture) assertHistoricalBytesAndUnlocked(t *testing.T) {
	t.Helper()
	for _, record := range []struct {
		path string
		want []byte
	}{{f.receipt.ReceiptPath, f.receiptBytes}, {f.claimPath, f.claimBytes}} {
		b, err := os.ReadFile(record.path)
		if err != nil || !bytes.Equal(b, record.want) {
			t.Fatalf("historical bytes changed %s: %v", record.path, err)
		}
	}
	lock, err := AcquireOperationLock(f.fixture.githubDir, f.receipt.Lane, true)
	if err != nil {
		t.Fatalf("native lane remained held: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

func (f conflictReplacementNativeFixture) noReplacement(t *testing.T) {
	t.Helper()
	assertNoConflictCandidateRefresh(t, f.fixture, f.receipt, f.options)
}

type conflictReplacementObservedRunner struct {
	runner.Runner
	before func(context.Context, string, string, []string) error
	after  func(context.Context, string, string, []string, runner.Result, error)
}

func (r conflictReplacementObservedRunner) RunOpts(ctx context.Context, dir string, o runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if r.before != nil {
		if err := r.before(ctx, dir, name, args); err != nil {
			return runner.Result{}, err
		}
	}
	result, err := r.Runner.RunOpts(ctx, dir, o, name, args...)
	if r.after != nil {
		r.after(ctx, dir, name, args, result, err)
	}
	return result, err
}

func TestConflictReplacementOwnerNativeDryRunApplyResumeAndConsume(t *testing.T) {
	t.Parallel()
	f := newConflictReplacementNativeFixture(t)
	planned, err := PrepareConflictWorktreeMergeReplacement(t.Context(), f.options)
	if err != nil || planned.Status != "conflict_candidate_refresh_planned" || planned.ObservedCandidateDescendant == "" {
		t.Fatalf("native plan=%+v %v", planned, err)
	}
	f.noReplacement(t)
	f.assertHistoricalBytesAndUnlocked(t)
	o := f.options
	o.Apply = true
	prepared, err := PrepareConflictWorktreeMergeReplacement(t.Context(), o)
	if err != nil || prepared.Status != "conflict_candidate_refresh_prepared" {
		t.Fatalf("native prepare=%+v %v", prepared, err)
	}
	for _, root := range planned.RequiredRoots {
		ok, err := isMergeAncestor(t.Context(), prepared.Candidate.Worktree, root.SHA, prepared.Candidate.SHA)
		if err != nil || !ok {
			t.Fatalf("native missing root %+v: %t %v", root, ok, err)
		}
	}
	view, err := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{ProjectsRoot: f.fixture.githubDir, Worktree: prepared.Candidate.Worktree})
	if err != nil || view.Claim == nil || view.Claim.Task != o.RefreshTask() || view.Claim.BaseSHA != o.ExpectedCurrentTargetSHA {
		t.Fatalf("native replacement claim=%+v %v", view, err)
	}
	f.assertHistoricalBytesAndUnlocked(t)
	resumed, err := PrepareConflictWorktreeMergeReplacement(t.Context(), o)
	if err != nil || resumed.Candidate != prepared.Candidate {
		t.Fatalf("native resume=%+v %v", resumed, err)
	}
	f.assertHistoricalBytesAndUnlocked(t)
	if _, err := SupersedeValidationFailedWorktreeMerge(t.Context(), WorktreeMergeValidationFailureSupersessionOptions{ProjectsRoot: f.fixture.githubDir, Receipt: f.receipt.ReceiptPath, ReplacementWorktree: prepared.Candidate.Worktree, Apply: true, Actor: "reviewer", Reason: "consume actual replacement"}); err != nil {
		t.Fatal(err)
	}
	if yes, err := hasValidationFailureSupersession(t.Context(), f.fixture.githubDir, f.receipt); err != nil || !yes {
		t.Fatalf("native consume=%t %v", yes, err)
	}
}

func TestConflictReplacementOwnerNativeEntryAndPinnedPolicy(t *testing.T) {
	t.Parallel()
	f := newConflictReplacementNativeFixture(t)
	for _, mode := range []string{"resolve", "initial read", "invalid receipt", "validation failed receipt", "receipt hash", "receipt mismatch", "claim read", "claim mismatch", "source mismatch", "wrong source", "published", "held lock"} {
		//nolint:paralleltest // Synchronous mutations of this private fixture are immediately restored.
		t.Run(mode, func(t *testing.T) { // Synchronous read-only or immediately restored native mutations share this private fixture.
			o := f.options
			o.Sources = append([]string(nil), o.Sources...)
			o.ExpectedSourceSHAs = append([]string(nil), o.ExpectedSourceSHAs...)
			read := readWorktreeMergeReceipt
			hash := worktreeMergeReceiptSHA256
			readClaim := os.ReadFile
			sentinel := errors.New("selected " + mode)
			consumed := false
			want := ""
			cause := false
			restoreSucceeded := true
			restore := func() {}
			t.Cleanup(func() { restore() })
			switch mode {
			case "resolve":
				o.Receipt = filepath.Join(t.TempDir(), "outside.json")
				if err := os.WriteFile(o.Receipt, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = "outside the authoritative"
			case "initial read":
				read = func(path string) (WorktreeMergeReceipt, error) {
					consumed = true
					return WorktreeMergeReceipt{}, sentinel
				}
				cause = true
			case "invalid receipt", "validation failed receipt":
				r := f.receipt
				if mode == "invalid receipt" {
					r.SchemaVersion = -1
					want = "has invalid identity"
				} else {
					r.Status = WorktreeMergeValidationFailed
					want = "not an unpublished prepare conflict"
				}
				if err := persistWorktreeMergeReceipt(r); err != nil {
					t.Fatal(err)
				}
				restore = func() {
					if err := os.WriteFile(r.ReceiptPath, f.receiptBytes, 0o600); err != nil {
						t.Error(err)
					}
				}
			case "receipt hash":
				hash = func(path string) (string, error) { consumed = true; return "", sentinel }
				cause = true
			case "receipt mismatch":
				o.ExpectedReceiptSHA256 = strings.Repeat("f", 64)
				want = "does not match expected"
			case "claim read":
				readClaim = func(path string) ([]byte, error) {
					if path == f.claimPath {
						consumed = true
						return nil, sentinel
					}
					return os.ReadFile(path)
				}
				cause = true
			case "claim mismatch":
				o.ExpectedImmutableClaimSHA256 = strings.Repeat("f", 64)
				want = "immutable claim SHA256"
			case "source mismatch":
				o.ExpectedSourceSHAs[0] = strings.Repeat("f", 40)
				want = "does not match expected"
			case "wrong source":
				o.Sources = []string{f.fixture.canonical, f.options.Sources[1]}
				want = ""
			case "published":
				want = "published"
				restoreSucceeded = false
				remoteDeleted := false
				restore = func() {
					if restoreSucceeded {
						return
					}
					cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if !remoteDeleted {
						result, err := defaultRunner.RunOpts(cleanupCtx, f.fixture.canonical, runner.RunOptions{CaptureCombined: true}, "git", "push", "origin", "--delete", f.receipt.Candidate.Branch)
						if err != nil || result.ExitCode != 0 {
							t.Errorf("restore original unpublished remote branch: %+v %v", result, err)
							return
						}
						remoteDeleted = true
					}
					result, err := defaultRunner.RunOpts(cleanupCtx, f.fixture.canonical, runner.RunOptions{CaptureCombined: true}, "git", "ls-remote", "--heads", "origin", "refs/heads/"+f.receipt.Candidate.Branch)
					if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.CombinedOutput) != "" {
						t.Errorf("restored remote branch still present: %+v %v", result, err)
						return
					}
					restoreSucceeded = true
				}
				runEngineGit(t, f.receipt.Candidate.Worktree, "push", "origin", f.receipt.Candidate.Branch)
			case "held lock":
				lock, err := AcquireOperationLock(f.fixture.githubDir, f.receipt.Lane, true)
				if err != nil {
					t.Fatal(err)
				}
				restore = func() {
					if err := lock.Release(); err != nil {
						t.Error(err)
					}
				}
			}
			got, err := prepareConflictWorktreeMergeReplacement(t.Context(), o, defaultRunner, read, hash, readClaim, nil, nil)
			if err == nil || !reflect.DeepEqual(got, WorktreeMergeConflictCandidateRefresh{}) || want != "" && !strings.Contains(err.Error(), want) || cause && (!consumed || !errors.Is(err, sentinel)) {
				t.Fatalf("%s got=%+v err=%v consumed=%t", mode, got, err, consumed)
			}
			restore()
			if !restoreSucceeded {
				t.Fatal("native restoration failed; fallback remains armed")
			}
			restore = func() {}
			f.noReplacement(t)
			f.assertHistoricalBytesAndUnlocked(t)
		})
	}
}

func TestConflictReplacementOwnerNativeInspectionRefusals(t *testing.T) {
	t.Parallel()
	f := newConflictReplacementNativeFixture(t)
	for _, mode := range []string{"candidate HEAD", "candidate clean", "publication", "source HEAD", "target fetch", "target HEAD", "different repository", "source identities", "current target", "observed absent"} {
		//nolint:paralleltest // Rows share one real lane and synchronously restore every native mutation.
		t.Run(mode, func(t *testing.T) {
			o := f.options
			o.Sources = append([]string(nil), o.Sources...)
			o.ExpectedSourceSHAs = append([]string(nil), o.ExpectedSourceSHAs...)
			sentinel := errors.New("selected native " + mode)
			consumed := false
			want := ""
			run := conflictReplacementObservedRunner{Runner: defaultRunner}
			restoreSucceeded := true
			restore := func() {}
			t.Cleanup(func() { restore() })
			args := []string(nil)
			dir := ""
			positive := false
			switch mode {
			case "candidate HEAD":
				dir = f.receipt.Candidate.Worktree
				args = []string{"rev-parse", "--verify", "HEAD^{commit}"}
				want = "read candidate HEAD"
			case "candidate clean":
				dir = f.receipt.Candidate.Worktree
				args = []string{"status", "--porcelain=v1"}
				want = "candidate is not clean"
			case "publication":
				dir = f.receipt.Candidate.Worktree
				args = []string{"ls-remote", "--heads", "origin", "refs/heads/" + f.receipt.Candidate.Branch}
				want = "publication state"
			case "source HEAD":
				dir = o.Sources[0]
				args = []string{"rev-parse", "--verify", "HEAD^{commit}"}
			case "target fetch":
				dir = f.receipt.Candidate.Worktree
				args = []string{"fetch", "--no-tags", "origin", "+refs/heads/main:refs/remotes/origin/main"}
			case "target HEAD":
				dir = f.receipt.Candidate.Worktree
				args = []string{"rev-parse", "--verify", "refs/remotes/origin/main^{commit}"}
			case "different repository":
				otherCanonical := filepath.Join(f.fixture.githubDir, "acme", "other")
				runEngineGit(t, f.fixture.canonical, "clone", "--no-hardlinks", f.fixture.repository.CloneURL, otherCanonical)
				runEngineGit(t, otherCanonical, "config", "user.name", "WB Test")
				runEngineGit(t, otherCanonical, "config", "user.email", "wb@example.test")
				other := engineFixture{githubDir: f.fixture.githubDir, canonical: otherCanonical, repository: Repository{Slug: "acme/other", Path: otherCanonical, CloneURL: f.fixture.repository.CloneURL}}
				source := createMergeSource(t, other, "other-source", "feature/other-source", "other.txt", "other\n")
				o.Sources = []string{source.WorktreeDir}
				o.ExpectedSourceSHAs = []string{strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))}
				want = "want receipt repository"
				positive = true
			case "source identities":
				o.Sources = []string{o.Sources[1], o.Sources[0]}
				o.ExpectedSourceSHAs = []string{o.ExpectedSourceSHAs[1], o.ExpectedSourceSHAs[0]}
				want = "immutable receipt identities"
				positive = true
			case "current target":
				o.ExpectedCurrentTargetSHA = strings.Repeat("f", 40)
				want = "current target"
				positive = true
			case "observed absent": // The original recorded candidate HEAD is still a native commit. Temporarily reset only the private candidate, then restore the authentic descendant.
				old := strings.TrimSpace(runEngineGit(t, f.receipt.Candidate.Worktree, "rev-parse", "HEAD"))
				restoreSucceeded = false
				restore = func() {
					if restoreSucceeded {
						return
					}
					cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					result, err := defaultRunner.RunOpts(cleanupCtx, f.receipt.Candidate.Worktree, runner.RunOptions{CaptureCombined: true}, "git", "reset", "--hard", old)
					if err != nil || result.ExitCode != 0 {
						t.Errorf("restore authentic descendant HEAD: %+v %v", result, err)
						return
					}
					result, err = defaultRunner.RunOpts(cleanupCtx, f.receipt.Candidate.Worktree, runner.RunOptions{CaptureCombined: true}, "git", "rev-parse", "--verify", "HEAD^{commit}")
					if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.CombinedOutput) != old {
						t.Errorf("restored descendant HEAD differs: %+v %v", result, err)
						return
					}
					restoreSucceeded = true
				}
				runEngineGit(t, f.receipt.Candidate.Worktree, "reset", "--hard", f.receipt.Candidate.SHA)
				positive = true
			}
			run.before = func(_ context.Context, path, name string, got []string) error {
				if !consumed && path == dir && name == "git" && reflect.DeepEqual(got, args) {
					consumed = true
					return sentinel
				}
				return nil
			}
			got, err := prepareConflictWorktreeMergeReplacement(t.Context(), o, run, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256, os.ReadFile, nil, nil)
			if mode == "observed absent" {
				if err != nil || got.ObservedCandidateDescendant != "" || got.Status != "conflict_candidate_refresh_planned" {
					t.Fatalf("native same-head=%+v %v", got, err)
				}
			} else if err == nil || !reflect.DeepEqual(got, WorktreeMergeConflictCandidateRefresh{}) || want != "" && !strings.Contains(err.Error(), want) || !positive && (!consumed || !errors.Is(err, sentinel)) {
				t.Fatalf("native inspection %s got=%+v %v consumed=%t", mode, got, err, consumed)
			}
			restore()
			if !restoreSucceeded {
				t.Fatal("native restoration failed; fallback remains armed")
			}
			restore = func() {}
			f.noReplacement(t)
			f.assertHistoricalBytesAndUnlocked(t)
		})
	}
}

func TestConflictReplacementOwnerNativeHeldRereadFaults(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		kind string
		at   int
	}{{"read", 2}, {"read", 3}, {"read", 4}, {"hash", 2}, {"hash", 3}, {"claim", 2}, {"claim", 3}, {"receipt validator", 2}} {
		t.Run(fmt.Sprintf("%s_%d", row.kind, row.at), func(t *testing.T) {
			t.Parallel()
			f := newConflictReplacementNativeFixture(t)
			o := f.options
			o.Apply = row.at > 2 || row.kind != "read" && row.kind != "receipt validator"
			count := 0
			consumed := false
			sentinel := errors.New("exact held " + row.kind)
			restore := func() {}
			t.Cleanup(func() { restore() })
			read := readWorktreeMergeReceipt
			hash := worktreeMergeReceiptSHA256
			readClaim := os.ReadFile
			assertHeld := func() {
				t.Helper()
				other, err := AcquireOperationLock(f.fixture.githubDir, f.receipt.Lane, true)
				if err == nil {
					_ = other.Release()
					t.Fatal("late negative observation was not under real lane lock")
				}
			}
			switch row.kind {
			case "read", "receipt validator":
				read = func(path string) (WorktreeMergeReceipt, error) {
					if path == f.receipt.ReceiptPath {
						count++
						if count == row.at {
							assertHeld()
							consumed = true
							if row.kind == "receipt validator" {
								restore = func() {
									if err := os.WriteFile(path, f.receiptBytes, 0o600); err != nil {
										t.Error(err)
									}
								}
								changed := f.receipt
								changed.Status = WorktreeMergeValidationFailed
								if err := persistWorktreeMergeReceipt(changed); err != nil {
									t.Fatal(err)
								}
								return readWorktreeMergeReceipt(path)
							}
							return WorktreeMergeReceipt{}, sentinel
						}
					}
					return readWorktreeMergeReceipt(path)
				}
			case "hash":
				hash = func(path string) (string, error) {
					if path == f.receipt.ReceiptPath {
						count++
						if count == row.at {
							assertHeld()
							consumed = true
							return "", sentinel
						}
					}
					return worktreeMergeReceiptSHA256(path)
				}
			case "claim":
				readClaim = func(path string) ([]byte, error) {
					if path == f.claimPath {
						count++
						if count == row.at {
							assertHeld()
							consumed = true
							return nil, sentinel
						}
					}
					return os.ReadFile(path)
				}
			}
			got, err := prepareConflictWorktreeMergeReplacement(t.Context(), o, defaultRunner, read, hash, readClaim, nil, nil)
			restore()
			if row.kind == "receipt validator" {
				if !consumed || count != 2 || err == nil || err.Error() != "receipt is not an unpublished prepare conflict" || !reflect.DeepEqual(got, WorktreeMergeConflictCandidateRefresh{}) {
					t.Fatalf("actual held receipt policy got=%+v err=%v consumed=%t reads=%d", got, err, consumed, count)
				}
			} else if !consumed || !errors.Is(err, sentinel) || !reflect.DeepEqual(got, WorktreeMergeConflictCandidateRefresh{}) {
				t.Fatalf("held observation got=%+v err=%v consumed=%t", got, err, consumed)
			}
			f.noReplacement(t)
			f.assertHistoricalBytesAndUnlocked(t)
		})
	}
}

func TestConflictReplacementOwnerNativeTemporalDriftAndResumedRetention(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"before create receipt", "before create source", "after create source", "after create receipt", "resumed source"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			f := newConflictReplacementNativeFixture(t)
			o := f.options
			o.Apply = true
			var existing WorktreeMergeConflictCandidateRefresh
			if stage == "resumed source" {
				var err error
				existing, err = PrepareConflictWorktreeMergeReplacement(t.Context(), o)
				if err != nil {
					t.Fatal(err)
				}
			}
			restore := func() {}
			t.Cleanup(func() { restore() })
			fired := false
			mutate := func() {
				fired = true
				if strings.Contains(stage, "receipt") {
					b := append(append([]byte(nil), f.receiptBytes...), '\n')
					restore = func() {
						if err := os.WriteFile(f.receipt.ReceiptPath, f.receiptBytes, 0o600); err != nil {
							t.Error(err)
						}
					}
					if err := os.WriteFile(f.receipt.ReceiptPath, b, 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					path := filepath.Join(o.Sources[0], "late-private-drift.txt")
					restore = func() {
						if err := os.Remove(path); err != nil {
							t.Error(err)
						}
					}
					writeEngineFile(t, path, "changed after native observation\n")
				}
			}
			var before, after func()
			if strings.HasPrefix(stage, "before create") {
				before = mutate
			} else {
				after = mutate
			}
			got, err := prepareConflictWorktreeMergeReplacement(t.Context(), o, defaultRunner, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256, os.ReadFile, before, after)
			if !fired || err == nil || !strings.Contains(err.Error(), "revalidate conflict replacement evidence") || !reflect.DeepEqual(got, WorktreeMergeConflictCandidateRefresh{}) {
				t.Fatalf("temporal %s got=%+v %v fired=%t", stage, got, err, fired)
			}
			restore()
			restore = func() {}
			f.assertHistoricalBytesAndUnlocked(t)
			if stage == "resumed source" {
				if _, err := worktrees.Guard(t.Context(), existing.Candidate.Worktree, worktrees.GuardOptions{ProjectsRoot: f.fixture.githubDir, Base: f.receipt.Target}); err != nil {
					t.Fatalf("resumed candidate retired: %v", err)
				}
				if head := strings.TrimSpace(runEngineGit(t, existing.Candidate.Worktree, "rev-parse", "HEAD")); head != existing.Candidate.SHA {
					t.Fatal("resumed candidate HEAD changed")
				}
			} else {
				f.noReplacement(t)
			}
		})
	}
}

func TestConflictReplacementOwnerNativeRevalidationComparesEveryPinnedField(t *testing.T) {
	t.Parallel()
	f := newConflictReplacementNativeFixture(t)
	state, err := inspectConflictCandidateRefresh(t.Context(), f.options, defaultRunner, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256, os.ReadFile, f.receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"receipt", "claim", "observed", "target", "canonical", "sources", "equal"} {
		//nolint:paralleltest // Each native observation uses the same pinned private fixture.
		t.Run(field, func(t *testing.T) {
			expected := state
			expected.sources = append([]WorktreeMergeSource(nil), state.sources...)
			switch field {
			case "receipt":
				expected.receiptHash = "different"
			case "claim":
				expected.claimHash = "different"
			case "observed":
				expected.observed = "different"
			case "target":
				expected.currentTarget = "different"
			case "canonical":
				expected.canonical = filepath.Join(t.TempDir(), "different")
			case "sources":
				expected.sources[0].Task += "-different"
			}
			err := revalidateConflictCandidateRefresh(t.Context(), f.options, defaultRunner, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256, os.ReadFile, f.receipt.ReceiptPath, expected)
			if field == "equal" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != "conflict replacement evidence changed during construction" {
				t.Fatalf("field %s compare=%v", field, err)
			}
			f.assertHistoricalBytesAndUnlocked(t)
		})
	}
}

func TestConflictReplacementOwnerNativePostCreationRunnerFaults(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"replacement clean", "replacement HEAD", "merge root", "final HEAD", "final clean", "final ancestry error", "final ancestry false", "abort secondary"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			f := newConflictReplacementNativeFixture(t)
			o := f.options
			o.Apply = true
			sentinel := errors.New("exact post-create " + stage)
			consumed := false
			createdPath := ""
			creationStarted := false
			mergeStarted, mergeCompleted := false, false
			want := ""
			nativeFalse := false
			nativeReplaceRemoved := false
			restoreNativeDAG := func() {}
			headReads := 0
			plan, planErr := PrepareConflictWorktreeMergeReplacement(t.Context(), f.options)
			if planErr != nil {
				t.Fatal(planErr)
			}
			uniqueRoots := map[string]bool{}
			for _, root := range plan.RequiredRoots {
				if root.SHA != "" {
					uniqueRoots[root.SHA] = true
				}
			}
			o.Progress = func(e progress.Event) {
				if e.Phase == "create_replacement" && e.State == progress.Started {
					if e.Detail != o.RefreshTask() {
						t.Fatalf("actual create task=%q", e.Detail)
					}
					creationStarted = true
				}
				if e.Phase == "merge_required_roots" {
					if e.State == progress.Started {
						mergeStarted = true
					}
					if e.State == progress.Completed {
						mergeCompleted = true
					}
				}
			}
			run := conflictReplacementObservedRunner{Runner: defaultRunner}
			run.before = func(ctx context.Context, dir, name string, args []string) error {
				if creationStarted && createdPath == "" && name == "git" {
					listed, err := worktrees.List(ctx, worktrees.ListOptions{ProjectsRoot: f.fixture.githubDir, Task: o.RefreshTask(), Base: f.receipt.Target, Workers: 1})
					if err != nil || len(listed) != 1 {
						t.Fatalf("actual created task inventory=%+v %v", listed, err)
					}
					if filepath.Clean(dir) != filepath.Clean(listed[0].WorktreeDir) {
						return nil
					}
					guard, err := worktrees.Guard(ctx, dir, worktrees.GuardOptions{ProjectsRoot: f.fixture.githubDir, Base: f.receipt.Target})
					if err != nil || guard.Branch != plan.Candidate.Branch {
						t.Fatalf("actual created Guard=%+v %v", guard, err)
					}
					view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: f.fixture.githubDir, Worktree: dir})
					if err != nil || view.Claim == nil || view.Claim.Task != o.RefreshTask() || view.Claim.Repository != f.receipt.Repository || view.Claim.Branch != plan.Candidate.Branch || view.Claim.Base != f.receipt.Target || view.Claim.BaseSHA != o.ExpectedCurrentTargetSHA {
						t.Fatalf("actual created Work Log=%+v %v", view, err)
					}
					createdPath = dir
				}
				if consumed || createdPath == "" || dir != createdPath || name != "git" {
					return nil
				}
				selected := false
				if mergeStarted && !mergeCompleted && reflect.DeepEqual(args, []string{"rev-parse", "--verify", "HEAD^{commit}"}) {
					headReads++
				}
				switch stage {
				case "replacement clean":
					selected = !mergeStarted && reflect.DeepEqual(args, []string{"status", "--porcelain=v1"})
					want = "replacement is not clean"
				case "replacement HEAD":
					selected = !mergeStarted && reflect.DeepEqual(args, []string{"rev-parse", "--verify", "HEAD^{commit}"})
					want = "read replacement HEAD"
				case "merge root":
					selected = mergeStarted && !mergeCompleted && len(args) == 3 && args[0] == "merge" && args[1] == "--no-edit"
					want = "merge required"
				case "final HEAD":
					selected = mergeStarted && !mergeCompleted && reflect.DeepEqual(args, []string{"rev-parse", "--verify", "HEAD^{commit}"}) && headReads == len(uniqueRoots)+1
				case "final clean", "abort secondary":
					selected = mergeStarted && !mergeCompleted && reflect.DeepEqual(args, []string{"status", "--porcelain=v1"})
					want = "conflict replacement candidate is not clean"
				case "final ancestry error":
					selected = mergeCompleted && len(args) == 3 && args[0] == "merge-base"
				case "final ancestry false":
					selected = mergeCompleted && len(args) == 3 && args[0] == "merge-base" && args[1] == f.receipt.Candidate.SHA
				}
				if !selected {
					return nil
				}
				consumed = true
				if stage == "abort secondary" {
					view, viewErr := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: f.fixture.githubDir, Worktree: dir})
					if viewErr != nil || view.Claim == nil {
						t.Fatalf("real abort preflight=%+v %v", view, viewErr)
					}
					path := view.Claim.ClaimPath
					before, readErr := os.ReadFile(path)
					if readErr != nil {
						t.Fatal(readErr)
					}
					t.Cleanup(func() {
						if err := os.WriteFile(path, before, 0o600); err != nil {
							t.Error(err)
							return
						}
						cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						if _, err := worktrees.Abort(cleanupCtx, worktrees.AbortOptions{ProjectsRoot: f.fixture.githubDir, Task: o.RefreshTask(), Base: f.receipt.Target, All: true, Apply: true, Disposition: worktrees.AbortDiscarded, DeleteRemote: true}); err != nil {
							t.Error(err)
						}
					})
					if err := os.WriteFile(path, []byte("not JSON\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if stage == "final ancestry false" {
					root, head := args[1], args[2]
					pre, err := isMergeAncestor(ctx, dir, root, head)
					if err != nil || !pre {
						t.Fatalf("real pre-DAG=%t %v", pre, err)
					}
					tree := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", head+"^{tree}"))
					replacement := strings.TrimSpace(runEngineGit(t, dir, "commit-tree", tree, "-p", f.receipt.TargetSHA, "-m", "test: temporal connected DAG drift"))
					restoreNativeDAG = func() {
						if !nativeFalse {
							return
						}
						cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						if !nativeReplaceRemoved {
							result, err := defaultRunner.RunOpts(cleanupCtx, dir, runner.RunOptions{CaptureCombined: true}, "git", "replace", "-d", head)
							if err != nil || result.ExitCode != 0 {
								t.Errorf("restore native DAG: %+v %v", result, err)
								return
							}
							nativeReplaceRemoved = true
						}
						if ok, err := isMergeAncestor(cleanupCtx, dir, root, head); err != nil || !ok {
							t.Errorf("restored native ancestry=%t %v", ok, err)
							return
						}
						nativeFalse = false
					}
					t.Cleanup(func() { restoreNativeDAG() })
					nativeFalse = true
					runEngineGit(t, dir, "replace", head, replacement)
					want = "does not contain required"
					return nil
				}
				return sentinel
			}
			run.after = func(_ context.Context, dir, name string, args []string, result runner.Result, err error) {
				if nativeFalse && dir == createdPath && name == "git" && len(args) == 3 && args[0] == "merge-base" && args[1] == f.receipt.Candidate.SHA {
					if err != nil || (strings.TrimSpace(result.CombinedOutput) == args[1] || strings.TrimSpace(result.CombinedOutput) == "") {
						t.Fatalf("real false/nil query=%+v %v", result, err)
					}
					restoreNativeDAG()
					if nativeFalse {
						t.Fatal("native DAG restoration failed before owner cleanup")
					}
				}
			}
			got, err := prepareConflictWorktreeMergeReplacement(t.Context(), o, run, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256, os.ReadFile, nil, nil)
			if !consumed || createdPath == "" || err == nil || !reflect.DeepEqual(got, WorktreeMergeConflictCandidateRefresh{}) || want != "" && !strings.Contains(err.Error(), want) || stage != "final ancestry false" && !errors.Is(err, sentinel) {
				t.Fatalf("post-create %s got=%+v %v consumed=%t", stage, got, err, consumed)
			}
			if stage == "abort secondary" {
				if !strings.Contains(err.Error(), "retire invalid conflict replacement candidate:") {
					t.Fatalf("missing actual secondary Abort refusal: %v", err)
				}
				if _, statErr := os.Stat(createdPath); statErr != nil {
					t.Fatalf("refused native cleanup lost candidate: %v", statErr)
				}
			} else {
				f.noReplacement(t)
			}
			f.assertHistoricalBytesAndUnlocked(t)
		})
	}
}

func TestConflictReplacementOwnerNativeBaseDriftAndImmutableReceipt(t *testing.T) {
	t.Parallel()
	f := newConflictReplacementNativeFixture(t)
	o := f.options
	o.Apply = true
	reads := 0
	consumed := false
	advanced := ""
	run := conflictReplacementObservedRunner{Runner: defaultRunner}
	run.after = func(_ context.Context, dir, name string, args []string, _ runner.Result, err error) {
		if err == nil && dir == f.receipt.Candidate.Worktree && name == "git" && reflect.DeepEqual(args, []string{"rev-parse", "--verify", "refs/remotes/origin/main^{commit}"}) {
			reads++
			if reads == 2 {
				consumed = true
				writeEngineFile(t, filepath.Join(f.fixture.canonical, "late-target.txt"), "real target drift after second authenticated capture\n")
				runEngineGit(t, f.fixture.canonical, "add", "late-target.txt")
				runEngineGit(t, f.fixture.canonical, "commit", "-m", "test: native target moves before create")
				runEngineGit(t, f.fixture.canonical, "push", "origin", "main")
				advanced = strings.TrimSpace(runEngineGit(t, f.fixture.canonical, "rev-parse", "HEAD"))
			}
		}
	}
	got, err := prepareConflictWorktreeMergeReplacement(t.Context(), o, run, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256, os.ReadFile, nil, nil)
	if !consumed || advanced == "" || advanced == o.ExpectedCurrentTargetSHA || err == nil || !strings.Contains(err.Error(), "target or canonical identity drifted") || !reflect.DeepEqual(got, WorktreeMergeConflictCandidateRefresh{}) {
		t.Fatalf("native target drift=%+v %v reads=%d", got, err, reads)
	}
	f.noReplacement(t)
	f.assertHistoricalBytesAndUnlocked(t)
}

func TestConflictReplacementOwnerNativeInventoryAndCreateRefusals(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"multiple candidates", "native session refusal"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			f := newConflictReplacementNativeFixture(t)
			o := f.options
			o.Apply = true
			want := "create conflict replacement worktree"
			if stage == "native session refusal" {
				o.SessionRequired = true
			} else {
				other := filepath.Join(f.fixture.githubDir, "acme", "other")
				runEngineGit(t, filepath.Dir(other), "clone", "--no-hardlinks", f.fixture.repository.CloneURL, other)
				runEngineGit(t, other, "config", "user.name", "WB Test")
				runEngineGit(t, other, "config", "user.email", "wb@example.test")
				prompt := filepath.Join(t.TempDir(), "prompt.txt")
				writeEngineFile(t, prompt, "native two-repository task inventory\n")
				created, err := worktrees.Create(t.Context(), []string{f.receipt.Repository, "acme/other"}, worktrees.CreateOptions{ProjectsRoot: f.fixture.githubDir, Operation: o.RefreshTask(), Base: f.receipt.Target, Branch: "wb/recovery/" + f.receipt.Target + "/" + mergeOperationSuffix(o.RefreshTask()) + "-conflict-replacement", BranchChosen: true, WorkLog: worktrees.WorkLogOptions{AgentRuntime: "test", Model: "test-model", OriginalPrompt: prompt, RequireOriginalPrompt: true}})
				if err != nil || len(created) != 2 {
					t.Fatalf("real two-repository setup=%+v %v", created, err)
				}
				want = "resolves to 2 worktrees"
			}
			got, err := PrepareConflictWorktreeMergeReplacement(t.Context(), o)
			if err == nil || !strings.Contains(err.Error(), want) || !reflect.DeepEqual(got, WorktreeMergeConflictCandidateRefresh{}) {
				t.Fatalf("inventory %s=%+v %v", stage, got, err)
			}
			f.assertHistoricalBytesAndUnlocked(t)
			if stage == "native session refusal" {
				f.noReplacement(t)
			} else {
				listed, err := worktrees.List(t.Context(), worktrees.ListOptions{ProjectsRoot: f.fixture.githubDir, Task: o.RefreshTask(), Base: f.receipt.Target, Workers: 1})
				if err != nil || len(listed) != 2 {
					t.Fatalf("preexisting inventory changed=%+v %v", listed, err)
				}
			}
		})
	}
}

//nolint:paralleltest // XDG_CONFIG_HOME and native scratch environment changes are process-wide; every mutation root is private.
func TestConflictReplacementOwnerNativeListAndPromptFilesystemRefusals(t *testing.T) {
	for _, stage := range []string{"list root", "scratch prompt"} {
		//nolint:paralleltest // Each child pins process-wide configuration or scratch environment.
		t.Run(stage, func(t *testing.T) {
			configHome := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", configHome)
			f := newConflictReplacementNativeFixture(t)
			o := f.options
			o.Apply = true
			blocker := filepath.Join(t.TempDir(), "not-a-directory")
			writeEngineFile(t, blocker, "real private ENOTDIR\n")
			calls := 0
			consumed := false
			configPath := filepath.Join(configHome, "wb", "worktrees.yaml")
			originalScratch := make(map[string]struct {
				value   string
				present bool
			})
			for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
				value, present := os.LookupEnv(key)
				originalScratch[key] = struct {
					value   string
					present bool
				}{value, present}
			}
			restoreScratch := func() {
				for key, original := range originalScratch {
					var err error
					if original.present {
						err = os.Setenv(key, original.value)
					} else {
						err = os.Unsetenv(key)
					}
					if err != nil {
						t.Error(err)
					}
					if value, present := os.LookupEnv(key); value != original.value || present != original.present {
						t.Errorf("scratch environment %s not restored exactly", key)
					}
				}
			}
			t.Cleanup(restoreScratch)
			run := conflictReplacementObservedRunner{Runner: defaultRunner, after: func(_ context.Context, dir, name string, args []string, result runner.Result, err error) {
				if dir != f.receipt.Candidate.Worktree || name != "git" || !reflect.DeepEqual(args, []string{"rev-parse", "--verify", "refs/remotes/origin/main^{commit}"}) {
					return
				}
				calls++
				if calls != 2 {
					return
				}
				if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.CombinedOutput) != o.ExpectedCurrentTargetSHA {
					t.Fatalf("native captured precreation target=%+v %v", result, err)
				}
				consumed = true
				if stage == "list root" {
					t.Cleanup(func() {
						if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
							t.Error(err)
						}
					})
					writeEngineFile(t, configPath, fmt.Sprintf("version: 1\nworktrees:\n  root: %q\n", blocker))
				} else {
					for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
						if err := os.Setenv(key, blocker); err != nil {
							t.Fatal(err)
						}
					}
				}
			}}
			got, err := prepareConflictWorktreeMergeReplacement(t.Context(), o, run, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256, os.ReadFile, nil, nil)
			restoreScratch()
			if !consumed || calls != 2 || err == nil || !reflect.DeepEqual(got, WorktreeMergeConflictCandidateRefresh{}) {
				t.Fatalf("actual filesystem %s=%+v %v consumed=%t", stage, got, err, consumed)
			}
			if stage == "list root" {
				if !strings.Contains(err.Error(), "inspect conflict replacement worktree") || !strings.Contains(err.Error(), "read worktree tasks") {
					t.Fatalf("native List error=%v", err)
				}
				if err := os.Remove(configPath); err != nil {
					t.Fatal(err)
				}
			} else {
				var pathErr *os.PathError
				if !errors.As(err, &pathErr) || filepath.Dir(pathErr.Path) != blocker || !strings.HasPrefix(filepath.Base(pathErr.Path), "wb-conflict-candidate-refresh-prompt-") {
					t.Fatalf("native scratch error=%v", err)
				}
			}
			f.noReplacement(t)
			f.assertHistoricalBytesAndUnlocked(t)
		})
	}
}

func TestConflictReplacementOwnerNativeResumedHistoricalBaseIdentityRefusal(t *testing.T) {
	t.Parallel()
	native := newExplicitRootEngineFixture(t)
	writeEngineFile(t, filepath.Join(native.canonical, "target-parent.txt"), "target has a distinct genuine parent\n")
	runEngineGit(t, native.canonical, "add", "target-parent.txt")
	runEngineGit(t, native.canonical, "commit", "-m", "test: distinct native conflict replacement target parent")
	runEngineGit(t, native.canonical, "push", "origin", "main")
	f := newConflictReplacementNativeFixtureWithFixture(t, native)
	o := f.options
	o.Apply = true
	existing, err := PrepareConflictWorktreeMergeReplacement(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	consumed := false
	restore := func() {}
	t.Cleanup(func() { restore() })
	run := conflictReplacementObservedRunner{Runner: defaultRunner, after: func(ctx context.Context, dir, name string, args []string, result runner.Result, err error) {
		if consumed || err != nil || result.ExitCode != 0 || dir != existing.Candidate.Worktree || name != "git" || !reflect.DeepEqual(args, []string{"rev-parse", "--verify", "HEAD^{commit}"}) {
			return
		}
		view, viewErr := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: f.fixture.githubDir, Worktree: dir})
		if viewErr != nil || view.Claim == nil {
			t.Fatalf("actual resumed claim=%+v %v", view, viewErr)
		}
		claim := view.Claim
		oldBytes, readErr := os.ReadFile(claim.ClaimPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		projection := filepath.Join(dir, ".wb-worklog", "recovery.json")
		pointer, readErr := os.ReadFile(projection)
		if readErr != nil {
			t.Fatal(readErr)
		}
		earlier := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", f.receipt.TargetSHA+"^"))
		if earlier == claim.BaseSHA {
			t.Fatal("fixture needs real distinct base")
		}
		if yes, err := isMergeAncestor(ctx, dir, earlier, strings.TrimSpace(result.CombinedOutput)); err != nil || !yes {
			t.Fatalf("real alternate base=%t %v", yes, err)
		}
		newID := worktrees.WorkLogClaimID(claim.EffortID, worktrees.CreateResult{Repository: claim.Repository, WorktreeDir: claim.Worktree, Branch: claim.Branch, Base: claim.Base, BaseSHA: earlier})
		newPath := filepath.Join(filepath.Dir(claim.ClaimPath), newID+".json")
		restored := false
		restore = func() {
			if restored {
				return
			}
			if err := os.WriteFile(projection, pointer, 0o600); err != nil {
				t.Error(err)
			}
			if err := os.WriteFile(claim.ClaimPath, oldBytes, 0o600); err != nil {
				t.Error(err)
			}
			if err := os.Remove(newPath); err != nil && !os.IsNotExist(err) {
				t.Error(err)
			}
			restored = true
		}
		// This explicitly qualified private historical claim input is authenticated
		// by the real claim-ID, Work Log and DAG validators; no successful DTO is supplied.
		var record map[string]any
		if err := json.Unmarshal(oldBytes, &record); err != nil {
			t.Fatal(err)
		}
		record["base_sha"], record["claim_id"] = earlier, newID
		encoded, encodeErr := json.Marshal(record)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if err := os.WriteFile(newPath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(projection, bytes.ReplaceAll(pointer, []byte(claim.ClaimID), []byte(newID)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(claim.ClaimPath); err != nil {
			t.Fatal(err)
		}
		authentic, claimErr := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: f.fixture.githubDir, Worktree: dir})
		if claimErr != nil || authentic.Claim == nil || authentic.Claim.ClaimID != newID || authentic.Claim.Base != claim.Base || authentic.Claim.BaseSHA != earlier {
			t.Fatalf("real historical claim=%+v %v", authentic, claimErr)
		}
		validated, bound, validationErr := validateValidationFailureReplacementWithRunner(ctx, defaultRunner, f.fixture.githubDir, f.receipt, dir)
		if validationErr != nil || bound == nil || bound.BaseSHA != earlier || validated.Task != o.RefreshTask() {
			t.Fatalf("native authenticated historical base=%+v %+v %v", validated, bound, validationErr)
		}
		consumed = true
	}}
	got, err := prepareConflictWorktreeMergeReplacement(t.Context(), o, run, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256, os.ReadFile, nil, nil)
	restore()
	if !consumed || err == nil || !strings.Contains(err.Error(), "conflict replacement candidate has an unexpected identity") || !reflect.DeepEqual(got, WorktreeMergeConflictCandidateRefresh{}) {
		t.Fatalf("historical resumed identity=%+v %v consumed=%t", got, err, consumed)
	}
	if current := strings.TrimSpace(runEngineGit(t, existing.Candidate.Worktree, "rev-parse", "HEAD")); current != existing.Candidate.SHA {
		t.Fatal("resumed historical identity refusal changed candidate")
	}
	f.assertHistoricalBytesAndUnlocked(t)
	view, err := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{ProjectsRoot: f.fixture.githubDir, Worktree: existing.Candidate.Worktree})
	if err != nil || view.Claim == nil || view.Claim.BaseSHA != o.ExpectedCurrentTargetSHA {
		t.Fatalf("restored native resumed claim=%+v %v", view, err)
	}
}
