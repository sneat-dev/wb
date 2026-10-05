package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/retiredcandidateack"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// Each observation delegates native positive results and can refuse only its
// named negative stage; it never supplies a successful custody or Git result.
type retirementOwnerRunner struct {
	runner.Runner
	before func(context.Context, string, string, []string) error
}

func (r retirementOwnerRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if r.before != nil {
		if err := r.before(ctx, dir, name, args); err != nil {
			return runner.Result{}, err
		}
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}

// The original legacy fixture recipe uses explicit roots here. Its sources are
// recorded legacy schema, not a claim that those sources have native custody.
func retirementOwnerFixture(t *testing.T, diverged bool) (engineFixture, WorktreeMergeReceipt) {
	t.Helper()
	f := newExplicitRootEngineFixture(t)
	runEngineGit(t, f.repository.CloneURL, "symbolic-ref", "HEAD", "refs/heads/main")
	runEngineGit(t, f.canonical, "branch", "deleted-target", "main")
	if diverged {
		runEngineGit(t, f.canonical, "checkout", "deleted-target")
		writeEngineFile(t, filepath.Join(f.canonical, "only-on-deleted-target.txt"), "target only\n")
		runEngineGit(t, f.canonical, "add", "only-on-deleted-target.txt")
		runEngineGit(t, f.canonical, "commit", "-m", "target only")
		runEngineGit(t, f.canonical, "checkout", "main")
	}
	runEngineGit(t, f.canonical, "push", "origin", "deleted-target")
	created, err := worktrees.Create(t.Context(), []string{f.repository.Slug}, worktrees.CreateOptions{ProjectsRoot: f.githubDir, Operation: "retired-prepare-candidate", Base: "deleted-target", WorkLog: worktrees.WorkLogOptions{Model: "test-model"}})
	if err != nil || len(created) != 1 {
		t.Fatalf("native legacy candidate: %+v %v", created, err)
	}
	c := created[0]
	home, err := wbhome.Root(f.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	r := WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, ID: "retired-prepare-receipt", Lane: worktreeMergeLaneID(f.repository.Slug, "deleted-target"), Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeConflict, Repository: f.repository.Slug, Target: "deleted-target", TargetSHA: c.BaseSHA, Candidate: WorktreeMergeCandidate{Task: "retired-prepare-candidate", Worktree: c.WorktreeDir, Branch: c.Branch}, Sources: []WorktreeMergeSource{{Task: "source", Worktree: filepath.Join(f.githubDir, "source"), Branch: "feature/source", SHA: strings.Repeat("b", 40)}}, ReceiptPath: filepath.Join(home, "reports", "worktree-merge", "retired-prepare-receipt.json")}
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, f.canonical, "push", "origin", ":deleted-target")
	return f, r
}
func retirementOwnerReceiptBytes(t *testing.T, r WorktreeMergeReceipt) func() {
	t.Helper()
	before, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		after, err := os.ReadFile(r.ReceiptPath)
		if err != nil || string(after) != string(before) {
			t.Fatalf("immutable receipt changed: %v", err)
		}
	}
}
func retirementOwnerReleased(t *testing.T, root, lane string) {
	t.Helper()
	lock, err := AcquireOperationLock(root, lane, true)
	if err != nil {
		t.Fatalf("owned lock not released: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}
func retirementOwnerOptions(f engineFixture, r WorktreeMergeReceipt) WorktreeMergeRetiredPrepareCandidateAcknowledgementOptions {
	return WorktreeMergeRetiredPrepareCandidateAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "native legacy candidate is contained and target absent"}
}

func TestRetiredPrepareOwnerReceiptPolicyContracts(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	r := WorktreeMergeReceipt{ReceiptPath: path, ID: "legacy", Repository: "acme/app", Target: "gone", TargetSHA: strings.Repeat("a", 40), Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeConflict, Candidate: WorktreeMergeCandidate{Task: "candidate", Worktree: "/candidate", Branch: "wb/candidate"}, Sources: []WorktreeMergeSource{{Task: "source", Worktree: "/source", Branch: "feature/source", SHA: strings.Repeat("b", 40)}}}
	r.Lane = worktreeMergeLaneID(r.Repository, r.Target)
	if err := validateRetiredPrepareCandidateReceipt(r, path); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, want string
		mutate     func(*WorktreeMergeReceipt)
	}{
		{"path", "inconsistent", func(r *WorktreeMergeReceipt) { r.ReceiptPath = "different" }},
		{"id", "inconsistent", func(r *WorktreeMergeReceipt) { r.ID = "" }},
		{"lane", "inconsistent", func(r *WorktreeMergeReceipt) { r.Lane = "" }},
		{"phase", "failed prepare conflict", func(r *WorktreeMergeReceipt) { r.Phase = WorktreeMergePhaseLand }},
		{"status", "failed prepare conflict", func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePrepared }},
		{"landing", "failed prepare conflict", func(r *WorktreeMergeReceipt) { r.LandingSHA = r.TargetSHA }},
		{"pr", "failed prepare conflict", func(r *WorktreeMergeReceipt) { r.PullRequest = "17" }},
		{"published", "failed prepare conflict", func(r *WorktreeMergeReceipt) { r.PublishedCandidateSHA = r.TargetSHA }},
		{"target sha", "empty-candidate", func(r *WorktreeMergeReceipt) { r.TargetSHA = "" }},
		{"candidate sha", "empty-candidate", func(r *WorktreeMergeReceipt) { r.Candidate.SHA = r.TargetSHA }},
		{"confused source", "candidate-confused", func(r *WorktreeMergeReceipt) { r.Sources[0].Worktree = r.Candidate.Worktree }},
		{"missing source", "candidate-confused", func(r *WorktreeMergeReceipt) { r.Sources = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			copy := r
			copy.Sources = append([]WorktreeMergeSource(nil), r.Sources...)
			tc.mutate(&copy)
			if err := validateRetiredPrepareCandidateReceipt(copy, path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("actual schema refusal: %v want %s", err, tc.want)
			}
		})
	}
}

func TestRetiredPrepareOwnerReadAdmissionAndSecondReadErrors(t *testing.T) {
	t.Parallel()
	f, r := retirementOwnerFixture(t, false)
	verify := retirementOwnerReceiptBytes(t, r)
	o := retirementOwnerOptions(f, r)
	sentinel := errors.New("selected receipt read refusal")
	// All rows are synchronous on one private read-only native fixture.
	for _, stage := range []string{"resolver", "first read", "first validation", "actor", "second read", "second validation", "lock"} {
		//nolint:paralleltest // Rows share one private native fixture and execute mutations/locks synchronously.
		t.Run(stage, func(t *testing.T) {
			calls := 0
			options := o
			read := func(path string) (WorktreeMergeReceipt, error) {
				calls++
				if stage == "first read" && calls == 1 || stage == "second read" && calls == 2 {
					return WorktreeMergeReceipt{}, sentinel
				}
				actual, err := readWorktreeMergeReceipt(path)
				if err == nil && (stage == "first validation" && calls == 1 || stage == "second validation" && calls == 2) {
					actual.Phase = WorktreeMergePhaseLand
				}
				return actual, err
			}
			if stage == "resolver" {
				options.Receipt = ""
			}
			if stage == "actor" {
				options.Actor = ""
			}
			var held OperationLock
			if stage == "lock" {
				var err error
				held, err = AcquireOperationLock(f.githubDir, r.Lane, true)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := held.Release(); err != nil {
						t.Error(err)
					}
				}()
			}
			_, err := acknowledgeRetiredPrepareCandidate(t.Context(), options, read, defaultRunner, retiredcandidateack.FileSHA256, retiredcandidateack.Persist)
			if err == nil {
				t.Fatal("selected negative stage accepted")
			}
			if strings.Contains(stage, "read") && !errors.Is(err, sentinel) {
				t.Fatalf("read identity lost: %v", err)
			}
			if stage == "second read" || stage == "second validation" {
				if calls != 2 {
					t.Fatalf("second read not reached: %d", calls)
				}
			}
			if stage == "resolver" && calls != 0 {
				t.Fatalf("resolver refusal observed read: %d", calls)
			}
			if stage == "actor" && calls != 1 {
				t.Fatalf("actor refusal observed late effects: %d", calls)
			}
			if stage != "lock" {
				retirementOwnerReleased(t, f.githubDir, r.Lane)
			}
			verify()
			if _, err := os.Stat(retiredcandidateack.Path(r.ReceiptPath)); !os.IsNotExist(err) {
				t.Fatalf("refusal published sidecar: %v", err)
			}
		})
	}
}

func TestRetiredPrepareOwnerExactNativeObservationFailures(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"status", "branch", "head", "candidate publication", "target ref", "default branch", "fetch", "default revision", "containment", "hash", "persist", "second proof"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			f, r := retirementOwnerFixture(t, false)
			verify := retirementOwnerReceiptBytes(t, r)
			o := retirementOwnerOptions(f, r)
			sentinel := errors.New("selected native retirement observation " + stage)
			consumed := 0
			args := map[string][]string{"status": {"status", "--porcelain"}, "branch": {"symbolic-ref", "--quiet", "--short", "HEAD"}, "head": {"rev-parse", "--verify", "HEAD^{commit}"}, "candidate publication": {"ls-remote", "origin", "refs/heads/" + r.Candidate.Branch}, "target ref": {"ls-remote", "origin", "refs/heads/" + r.Target}, "default branch": {"ls-remote", "--symref", "origin", "HEAD"}, "fetch": {"fetch", "--no-tags", "origin", "+refs/heads/main:refs/remotes/origin/main"}, "default revision": {"rev-parse", "--verify", "refs/remotes/origin/main^{commit}"}, "containment": {"merge-base", r.TargetSHA, r.TargetSHA}}
			run := retirementOwnerRunner{Runner: defaultRunner, before: func(_ context.Context, dir, name string, actual []string) error {
				if dir == r.Candidate.Worktree && name == "git" && reflect.DeepEqual(actual, args[stage]) {
					consumed++
					return sentinel
				}
				return nil
			}}
			hash := retiredcandidateack.FileSHA256
			persist := retiredcandidateack.Persist
			if stage == "hash" {
				hash = func(path string) (string, error) {
					if path != r.ReceiptPath {
						t.Fatal("hash physical scope changed")
					}
					consumed++
					return "", sentinel
				}
			}
			if stage == "persist" {
				persist = func(path string, a retiredcandidateack.Acknowledgement) error {
					if path != retiredcandidateack.Path(r.ReceiptPath) || a.Candidate.SHA != r.TargetSHA {
						t.Fatalf("real proof not retained: %+v", a)
					}
					consumed++
					return sentinel
				}
			}
			if stage == "second proof" {
				hash = func(path string) (string, error) {
					value, err := retiredcandidateack.FileSHA256(path)
					if err == nil {
						consumed++
						if consumed == 1 {
							writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "late-dirty.txt"), "actual change after first proof\n")
						}
					}
					return value, err
				}
			}
			_, err := acknowledgeRetiredPrepareCandidate(t.Context(), o, readWorktreeMergeReceipt, run, hash, persist)
			if err == nil || consumed != 1 {
				t.Fatalf("named native stage not consumed: %v count=%d", err, consumed)
			}
			if stage == "second proof" {
				if !strings.Contains(err.Error(), "local changes") {
					t.Fatalf("mutable second proof ignored: %v", err)
				}
			} else if !errors.Is(err, sentinel) {
				t.Fatalf("primary observation identity lost: %v", err)
			}
			verify()
			if _, err := os.Stat(retiredcandidateack.Path(r.ReceiptPath)); !os.IsNotExist(err) {
				t.Fatalf("failed stage published sidecar: %v", err)
			}
			retirementOwnerReleased(t, f.githubDir, r.Lane)
		})
	}
}

func TestRetiredPrepareOwnerNativeStateAndWinnerContracts(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"dry run", "apply replay", "winner", "missing winner", "malformed sidecar", "dirty", "detached", "wrong branch", "advanced head", "published", "target exists", "missing default", "detached default", "uncontained"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			f, r := retirementOwnerFixture(t, stage == "uncontained")
			verify := retirementOwnerReceiptBytes(t, r)
			o := retirementOwnerOptions(f, r)
			persist := retiredcandidateack.Persist
			want := ""
			win := false
			switch stage {
			case "dry run":
				o.Apply = false
			case "apply replay":
			case "winner":
				persist = func(path string, a retiredcandidateack.Acknowledgement) error {
					if err := retiredcandidateack.Persist(path, a); err != nil {
						return err
					}
					win = true
					return os.ErrExist
				}
			case "missing winner":
				persist = func(string, retiredcandidateack.Acknowledgement) error { win = true; return os.ErrExist }
			case "malformed sidecar":
				if err := os.WriteFile(retiredcandidateack.Path(r.ReceiptPath), []byte("not JSON\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = ""
			case "dirty":
				writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "dirty.txt"), "native dirty\n")
				want = "local changes"
			case "detached":
				runEngineGit(t, r.Candidate.Worktree, "checkout", "--detach")
				want = "read candidate branch"
			case "wrong branch":
				runEngineGit(t, r.Candidate.Worktree, "checkout", "-b", "wrong-branch")
				want = "no longer matches"
			case "advanced head":
				writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "advance.txt"), "native head change\n")
				runEngineGit(t, r.Candidate.Worktree, "add", "advance.txt")
				runEngineGit(t, r.Candidate.Worktree, "commit", "-m", "test: head advance")
				want = "no longer matches"
			case "published":
				runEngineGit(t, r.Candidate.Worktree, "push", "origin", r.Candidate.Branch)
				want = "published"
			case "target exists":
				runEngineGit(t, r.Candidate.Worktree, "push", "origin", r.TargetSHA+":refs/heads/"+r.Target)
				want = "still exists"
			case "missing default":
				runEngineGit(t, f.repository.CloneURL, "symbolic-ref", "HEAD", "refs/heads/missing")
				want = "no default branch"
			case "detached default":
				runEngineGit(t, f.repository.CloneURL, "update-ref", "--no-deref", "HEAD", r.TargetSHA)
				want = "no default branch"
			case "uncontained":
				want = "not contained"
			}
			got, err := acknowledgeRetiredPrepareCandidate(t.Context(), o, readWorktreeMergeReceipt, defaultRunner, retiredcandidateack.FileSHA256, persist)
			positive := stage == "dry run" || stage == "apply replay" || stage == "winner"
			if positive {
				if err != nil || got.Candidate.SHA != r.TargetSHA || got.DefaultBranch != "main" || got.ID == "" {
					t.Fatalf("actual native proof: %+v %v", got, err)
				}
				if stage == "dry run" {
					if _, err := os.Stat(retiredcandidateack.Path(r.ReceiptPath)); !os.IsNotExist(err) {
						t.Fatalf("dryrun sidecar: %v", err)
					}
				} else {
					loaded, err := retiredcandidateack.Load(retiredcandidateack.Path(r.ReceiptPath), retiredPrepareCandidateIdentity(r, r.ReceiptPath))
					if err != nil || loaded.ID != got.ID {
						t.Fatalf("actual persisted winner invalid: %+v %v", loaded, err)
					}
				}
				if stage == "winner" && !win {
					t.Fatal("real winner path not consumed")
				}
				if stage == "apply replay" {
					again, err := AcknowledgeRetiredPrepareCandidate(t.Context(), o)
					if err != nil || again.ID != got.ID {
						t.Fatalf("native replay: %+v %v", again, err)
					}
				}
			} else {
				if err == nil || want != "" && !strings.Contains(err.Error(), want) {
					t.Fatalf("native refusal %s: %v want %s", stage, err, want)
				}
				if stage == "missing winner" && (!win || !errors.Is(err, os.ErrNotExist)) {
					t.Fatalf("winner authentication absent: %v", err)
				}
			}
			verify()
			retirementOwnerReleased(t, f.githubDir, r.Lane)
		})
	}
}
