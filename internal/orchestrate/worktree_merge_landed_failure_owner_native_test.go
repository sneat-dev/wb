package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func landedFailureOwnerFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt) {
	t.Helper()
	f := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, f, "landed-failure-owner-source", "feature/landed-failure-owner", "owner.txt", "native owner\n")
	r, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	r.Status = WorktreeMergeValidationFailed
	r.Failure = "historical failed validation"
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, f.canonical, "update-ref", "refs/heads/main", r.Candidate.SHA)
	runEngineGit(t, f.canonical, "push", "origin", "main")
	return f, r
}

type landedFailureOwnerRunner struct {
	runner.Runner
	path          string
	args          []string
	ordinal, seen int
	sentinel      error
}

func (r *landedFailureOwnerRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if dir == r.path && name == "git" && reflect.DeepEqual(args, r.args) {
		r.seen++
		if r.seen == r.ordinal {
			return runner.Result{}, r.sentinel
		}
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}

func TestLandedFailureOwnerExactNativeObservationRefusals(t *testing.T) {
	t.Parallel()
	f, r := landedFailureOwnerFixture(t)
	before, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	// Each source containment query precedes the fresh remote containment query.
	remoteOrdinal := 1
	for _, source := range r.Sources {
		if source.SHA == r.Candidate.SHA {
			remoteOrdinal++
		}
	}
	for _, row := range []struct {
		name    string
		args    []string
		ordinal int
	}{
		{"clean", []string{"status", "--porcelain=v1"}, 1},
		{"HEAD", []string{"rev-parse", "--verify", "HEAD^{commit}"}, 1},
		{"claim base", []string{"merge-base", r.TargetSHA, r.Candidate.SHA}, 1},
		{"receipt target", []string{"merge-base", r.TargetSHA, r.Candidate.SHA}, 2},
		{"source", []string{"merge-base", r.Sources[0].SHA, r.Candidate.SHA}, 1},
		{"fetch", []string{"fetch", "--no-tags", "origin", "+refs/heads/main:refs/remotes/origin/main"}, 1},
		{"fetched revision", []string{"rev-parse", "--verify", "refs/remotes/origin/main^{commit}"}, 1},
		{"remote containment", []string{"merge-base", r.Candidate.SHA, r.Candidate.SHA}, remoteOrdinal},
	} {
		t.Run(row.name, func(t *testing.T) {
			sentinel := errors.New("selected " + row.name)
			run := &landedFailureOwnerRunner{Runner: defaultRunner, path: r.Candidate.Worktree, args: row.args, ordinal: row.ordinal, sentinel: sentinel}
			_, e := acknowledgeLandedMergeFailure(t.Context(), WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath}, run, readWorktreeMergeReceipt, persistLandedFailureAcknowledgement)
			if !errors.Is(e, sentinel) || run.seen != row.ordinal {
				t.Fatalf("error=%v consumed=%d want=%d", e, run.seen, row.ordinal)
			}
			lock, e := AcquireOperationLock(f.githubDir, r.Lane, true)
			if e != nil {
				t.Fatalf("lane leaked: %v", e)
			}
			if e := lock.Release(); e != nil {
				t.Fatal(e)
			}
		})
	}
	after, err := os.ReadFile(r.ReceiptPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("historical receipt changed: %v", err)
	}
}

func TestLandedFailureOwnerPhysicalCandidateAndRemoteRefusals(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"dirty", "advanced", "missing", "remote no candidate"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, r := landedFailureOwnerFixture(t)
			want := ""
			switch name {
			case "dirty":
				if err := os.WriteFile(filepath.Join(r.Candidate.Worktree, "dirty.txt"), []byte("dirty"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "candidate is not clean"
			case "advanced":
				runEngineGit(t, r.Candidate.Worktree, "commit", "--allow-empty", "-m", "advance native candidate")
				want = "does not match receipted candidate"
			case "missing":
				if err := os.RemoveAll(r.Candidate.Worktree); err != nil {
					t.Fatal(err)
				}
				want = "load candidate Work Log"
			case "remote no candidate":
				runEngineGit(t, f.canonical, "push", "--force", "origin", r.TargetSHA+":main")
				want = "does not contain receipted candidate"
			}
			_, e := AcknowledgeLandedMergeFailure(t.Context(), WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath})
			if e == nil || !strings.Contains(e.Error(), want) {
				t.Fatalf("error=%v want=%q", e, want)
			}
			if _, e := os.Stat(landedFailureAcknowledgementPath(r.ReceiptPath)); !os.IsNotExist(e) {
				t.Fatalf("refusal sidecar=%v", e)
			}
		})
	}
}

//nolint:paralleltest // The native terminal fixture installs a process-wide PATH/GH provider and WB_PROJECTS_ROOT.
func TestLandedFailureOwnerCleanedNativeTerminalAndObservationRefusals(t *testing.T) {
	f, _, r, claims := landedTerminalCleanupFixture(t)
	r.Status = WorktreeMergePostTargetCIFailed
	r.Checks.Status = PullRequestWaitFailed
	r.Checks.Head = r.LandingSHA
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	externallyTerminalizeMergeCleanup(t, f, &r)
	options := WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath}
	baseline, err := AcknowledgeLandedMergeFailure(t.Context(), options)
	if err != nil || baseline.ClaimBaseSHA == "" {
		t.Fatalf("native cleaned proof=%+v error=%v", baseline, err)
	}
	for _, row := range []struct {
		name string
		args []string
	}{
		{"fetch", []string{"fetch", "--no-tags", "origin", "+refs/heads/main:refs/remotes/origin/main"}},
		{"revision", []string{"rev-parse", "--verify", "refs/remotes/origin/main^{commit}"}},
		{"root ancestry", []string{"merge-base", r.TargetSHA, r.Candidate.SHA}},
	} {
		t.Run(row.name, func(t *testing.T) {
			sentinel := errors.New("cleaned " + row.name)
			run := &landedFailureOwnerRunner{Runner: defaultRunner, path: f.canonical, args: row.args, ordinal: 1, sentinel: sentinel}
			_, e := acknowledgeLandedMergeFailure(t.Context(), options, run, readWorktreeMergeReceipt, persistLandedFailureAcknowledgement)
			if !errors.Is(e, sentinel) || run.seen != 1 {
				t.Fatalf("error=%v consumed=%d", e, run.seen)
			}
		})
	}
	t.Run("route", func(t *testing.T) {
		changed := r
		changed.Route.Route = WorktreeMergeRoutePullRequest
		_, e := acknowledgeCleanedDirectPostTargetFailure(t.Context(), options, changed, r.ReceiptPath, defaultRunner, persistLandedFailureAcknowledgement)
		if e == nil || !strings.Contains(e.Error(), "direct route") {
			t.Fatalf("route=%v", e)
		}
	})
	t.Run("checkout still exists", func(t *testing.T) {
		if err := os.MkdirAll(r.Candidate.Worktree, 0700); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(r.Candidate.Worktree) })
		_, e := acknowledgeCleanedDirectPostTargetFailure(t.Context(), options, r, r.ReceiptPath, defaultRunner, persistLandedFailureAcknowledgement)
		if e == nil || !strings.Contains(e.Error(), "still exists") {
			t.Fatalf("checkout=%v", e)
		}
	})
	// Mutate only existing authentic native files, restore before the next row.
	for _, name := range []string{"cleanup report", "terminal Work Log"} {
		t.Run(name, func(t *testing.T) {
			var path, want string
			if name == "cleanup report" {
				proof, e := worktrees.FindTerminalCleanupProof(f.githubDir, r.Repository, r.Target, r.Candidate.Task, r.Candidate.Worktree, r.Candidate.Branch)
				if e != nil {
					t.Fatal(e)
				}
				path = proof.ReportPath
				want = "terminal cleanup"
			} else {
				path = terminalWorkLogPath(claims[r.Sources[0].Task])
				want = "removed terminal Work Log"
			}
			before, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			restore := func() {
				if e := os.WriteFile(path, before, 0600); e != nil {
					t.Errorf("restore native evidence: %v", e)
				}
			}
			t.Cleanup(restore)
			if e := os.Remove(path); e != nil {
				t.Fatal(e)
			}
			_, e = AcknowledgeLandedMergeFailure(t.Context(), options)
			restore()
			if e == nil || !strings.Contains(e.Error(), want) {
				t.Fatalf("error=%v want=%q", e, want)
			}
		})
	}
}

func TestLandedFailureCleanedAncestryExactNativeFaults(t *testing.T) {
	t.Parallel()
	f, r := landedFailureOwnerFixture(t)
	bases := map[string]string{r.Candidate.Task: r.TargetSHA, r.Sources[0].Task: r.TargetSHA}
	for _, row := range []struct {
		name    string
		args    []string
		ordinal int
		want    string
	}{
		{"candidate target", []string{"merge-base", r.TargetSHA, r.Candidate.SHA}, 1, "candidate ancestry"},
		{"candidate claim base", []string{"merge-base", r.TargetSHA, r.Candidate.SHA}, 2, "candidate ancestry"},
		{"source claim base", []string{"merge-base", r.TargetSHA, r.Sources[0].SHA}, 1, "claim-base ancestry"},
		{"candidate source", []string{"merge-base", r.Sources[0].SHA, r.Candidate.SHA}, 1, "receipted source"},
	} {
		t.Run(row.name, func(t *testing.T) {
			// One-source fast-forward candidates can equal the source; count all identical authentic queries.
			ordinal := row.ordinal
			if row.name == "source claim base" && r.Sources[0].SHA == r.Candidate.SHA {
				ordinal = 3
			}
			if row.name == "candidate source" && r.Sources[0].SHA == r.TargetSHA {
				ordinal = 4
			}
			sentinel := errors.New("cleaned selected " + row.name)
			run := &landedFailureOwnerRunner{Runner: defaultRunner, path: f.canonical, args: row.args, ordinal: ordinal, sentinel: sentinel}
			e := validateCleanedLandedFailureAncestry(t.Context(), run, f.canonical, r, bases)
			if !errors.Is(e, sentinel) || !strings.Contains(e.Error(), row.want) || run.seen != ordinal {
				t.Fatalf("error=%v consumed=%d want=%d", e, run.seen, ordinal)
			}
		})
	}
	if e := validateCleanedLandedFailureAncestry(t.Context(), defaultRunner, f.canonical, r, bases); e != nil {
		t.Fatalf("real native baseline ancestry: %v", e)
	}
}
