//go:build e2e

package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type publishedRecoveryTreeRefusal struct {
	runner.Runner
	t     *testing.T
	path  string
	want  [][]string
	calls [][]string
	cause error
}

func (r *publishedRecoveryTreeRefusal) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	index := len(r.calls)
	if index >= len(r.want) || dir != r.path || name != "git" || !reflect.DeepEqual(args, r.want[index]) || !reflect.DeepEqual(opts, runner.RunOptions{Env: console.Env(), CaptureCombined: true}) {
		r.t.Fatalf("tree observation %d: cwd=%q argv=%q opts=%+v", index, dir, args, opts)
	}
	if _, bounded := ctx.Deadline(); bounded {
		r.t.Fatal("zero-timeout ancestry/tree observation acquired a new deadline")
	}
	r.calls = append(r.calls, append([]string(nil), args...))
	if index == len(r.want)-1 {
		return runner.Result{}, r.cause
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}

func TestE2EPublishedRecoveryAbsorptionUsesNativeAncestryAndExactTreeObservations(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	base := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	source := createMergeSource(t, fixture, "absorption-observation", "feature/absorption-observation", "native.txt", "candidate content\n")
	candidate := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	claim, err := os.ReadFile(source.WorkLogPath)
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, source.WorktreeDir, "push", "origin", source.Branch)
	candidateTree := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", candidate+"^{tree}"))
	baseTree := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", base+"^{tree}"))
	landing := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "commit-tree", candidateTree, "-p", base, "-m", "native exact-tree squash"))
	unequal := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "commit-tree", baseTree, "-p", base, "-m", "native unequal-tree landing"))
	runEngineGit(t, fixture.canonical, "merge", "--ff-only", landing)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	prior := WorktreeMergeReceipt{PullRequest: "https://example.test/acme/app/pull/41", PublishedCandidateSHA: candidate, LandingSHA: landing, Candidate: WorktreeMergeCandidate{SHA: candidate, Worktree: source.WorktreeDir, Branch: source.Branch}}
	for _, mode := range []string{"candidate tree refusal", "landing tree refusal", "equal trees", "unequal trees", "graph containment", "incomplete publication", "landing not contained", "landing observation error"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			owned := prior
			remoteTarget := landing
			cause := errors.New("owned exact tree observation refused")
			var absorbed, contained bool
			var err error
			if strings.HasSuffix(mode, "tree refusal") {
				want := [][]string{{"merge-base", candidate, landing}, {"merge-base", landing, landing}, {"rev-parse", "--verify", candidate + "^{tree}"}}
				stage := "resolve prior candidate tree " + candidate
				if mode == "landing tree refusal" {
					want = append(want, []string{"rev-parse", "--verify", landing + "^{tree}"})
					stage = "resolve prior landing tree " + landing
				}
				run := &publishedRecoveryTreeRefusal{Runner: defaultRunner, t: t, path: source.WorktreeDir, want: want, cause: cause}
				absorbed, contained, err = worktreeMergeCandidateAbsorbedWithRunner(context.Background(), run, source.WorktreeDir, owned, remoteTarget)
				if !errors.Is(err, cause) || !strings.Contains(err.Error(), stage) || !reflect.DeepEqual(run.calls, want) {
					t.Fatalf("tree refusal = %v calls=%v", err, run.calls)
				}
			} else {
				switch mode {
				case "unequal trees":
					owned.LandingSHA, remoteTarget = unequal, unequal
				case "graph containment":
					remoteTarget = candidate
				case "incomplete publication":
					owned.PublishedCandidateSHA = ""
				case "landing not contained":
					remoteTarget = base
				case "landing observation error":
					owned.LandingSHA = strings.Repeat("f", 40)
				}
				absorbed, contained, err = worktreeMergeCandidateAbsorbed(t.Context(), source.WorktreeDir, owned, remoteTarget)
				if mode == "landing observation error" {
					if err == nil || !strings.Contains(err.Error(), "git merge-base "+owned.LandingSHA) {
						t.Fatalf("missing landing observation = %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
			wantAbsorbed, wantContained := mode == "equal trees" || mode == "graph containment", mode == "graph containment"
			if absorbed != wantAbsorbed || contained != wantContained {
				t.Fatalf("custody mode=%s absorbed=%t contained=%t err=%v", mode, absorbed, contained, err)
			}
			if got := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); got != candidate {
				t.Fatalf("source moved %s", got)
			}
			if got, err := os.ReadFile(source.WorkLogPath); err != nil || !reflect.DeepEqual(got, claim) {
				t.Fatalf("source claim changed %v", err)
			}
			if got := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); got != landing {
				t.Fatalf("native target moved %s", got)
			}
			if got := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+source.Branch)); got != candidate {
				t.Fatalf("published source moved %s", got)
			}
			if _, err := worktrees.Guard(t.Context(), source.WorktreeDir, worktrees.GuardOptions{ProjectsRoot: fixture.githubDir, Base: "main"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestE2EPublishedRecoveryPostMergeHeadFaultPreservesReceipt(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	base := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	source := createMergeSource(t, fixture, "post-merge-head-fault", "feature/post-merge-head-fault", "candidate.txt", "candidate\n")
	candidate := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	claim, err := os.ReadFile(source.WorkLogPath)
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, source.WorktreeDir, "push", "origin", source.Branch)
	writeEngineFile(t, filepath.Join(fixture.canonical, "target.txt"), "target advance\n")
	runEngineGit(t, fixture.canonical, "add", "target.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "advance owned target")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	target := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	receipt := WorktreeMergeReceipt{TargetSHA: base, PullRequest: "https://example.test/acme/app/pull/41", ReceiptPath: filepath.Join(t.TempDir(), "owned-receipt.json"), PublishedCandidateSHA: candidate, Candidate: WorktreeMergeCandidate{SHA: candidate, Worktree: source.WorktreeDir, Branch: source.Branch}}
	before, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	observed := filepath.Join(t.TempDir(), "post-merge-head")
	hooks := t.TempDir()
	body := "#!/bin/sh\nset -eu\ngit rev-parse --verify HEAD^{commit} > " + shellQuote(observed) + "\ngit symbolic-ref HEAD refs/heads/owned-unborn-refresh\n"
	if err := testenv.WriteExecutableFile(filepath.Join(hooks, "post-merge"), []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, source.WorktreeDir, "config", "core.hooksPath", hooks)
	originalRef := "refs/heads/" + source.Branch
	restored := false
	restore := func() {
		if restored {
			return
		}
		_, _, err := runCommand(context.Background(), defaultRunner, 30*time.Second, 0, source.WorktreeDir, "git", "symbolic-ref", "HEAD", originalRef)
		if err != nil {
			t.Fatalf("restore exact owned symbolic HEAD: %v", err)
		}
		restored = true
	}
	t.Cleanup(restore)
	err = refreshPublishedWorktreeMergeCandidateTarget(t.Context(), &receipt, target, 30*time.Second, 0)
	restore()
	if err == nil || !strings.Contains(err.Error(), "read refreshed candidate head") || !strings.Contains(err.Error(), "HEAD^{commit}") {
		t.Fatalf("actual post-merge head read fault = %v", err)
	}
	headBytes, readErr := os.ReadFile(observed)
	if readErr != nil {
		t.Fatalf("native post-merge hook did not record actual successful merge: %v", readErr)
	}
	merged := strings.TrimSpace(string(headBytes))
	if merged == "" || merged == candidate || merged == target || strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")) != merged {
		t.Fatalf("native merge/restored head %q", merged)
	}
	for _, ancestor := range []string{candidate, target} {
		runEngineGit(t, source.WorktreeDir, "merge-base", "--is-ancestor", ancestor, merged)
	}
	after, marshalErr := json.Marshal(receipt)
	if marshalErr != nil || string(after) != string(before) {
		t.Fatalf("head refusal mutated receipt %v: %s -> %s", marshalErr, before, after)
	}
	if got, err := os.ReadFile(source.WorkLogPath); err != nil || !reflect.DeepEqual(got, claim) {
		t.Fatalf("native head fault changed WorkLog %v", err)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+source.Branch)); got != candidate {
		t.Fatalf("head fault republished source %s", got)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); got != target {
		t.Fatalf("head fault moved target %s", got)
	}
	if _, err := worktrees.Guard(t.Context(), source.WorktreeDir, worktrees.GuardOptions{ProjectsRoot: fixture.githubDir, Base: "main"}); err != nil {
		t.Fatalf("native restored custody %v", err)
	}
}

func TestE2EPublishedRecoveryRefreshSuccessAndMergeRefusalsRetainNativeCustody(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "conflict", "unavailable target"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fixture := newExplicitRootEngineFixture(t)
			writeEngineFile(t, filepath.Join(fixture.canonical, "shared.txt"), "base\n")
			runEngineGit(t, fixture.canonical, "add", "shared.txt")
			runEngineGit(t, fixture.canonical, "commit", "-m", "owned base")
			runEngineGit(t, fixture.canonical, "push", "origin", "main")
			base := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
			source := createMergeSource(t, fixture, "published-refresh-"+strings.ReplaceAll(mode, " ", "-"), "feature/published-refresh-"+strings.ReplaceAll(mode, " ", "-"), "shared.txt", "candidate\n")
			candidate := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
			claim, err := os.ReadFile(source.WorkLogPath)
			if err != nil {
				t.Fatal(err)
			}
			runEngineGit(t, source.WorktreeDir, "push", "origin", source.Branch)
			file := "target.txt"
			if mode == "conflict" {
				file = "shared.txt"
			}
			writeEngineFile(t, filepath.Join(fixture.canonical, file), "target\n")
			runEngineGit(t, fixture.canonical, "add", file)
			runEngineGit(t, fixture.canonical, "commit", "-m", "owned target advance")
			runEngineGit(t, fixture.canonical, "push", "origin", "main")
			target := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
			receipt := WorktreeMergeReceipt{TargetSHA: base, PullRequest: "https://example.test/acme/app/pull/41", ReceiptPath: filepath.Join(t.TempDir(), "receipt.json"), Candidate: WorktreeMergeCandidate{SHA: candidate, Worktree: source.WorktreeDir, Branch: source.Branch}}
			before, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			requested := target
			if mode == "unavailable target" {
				requested = strings.Repeat("f", 40)
			}
			err = refreshPublishedWorktreeMergeCandidateTarget(t.Context(), &receipt, requested, 30*time.Second, 0)
			if mode == "success" {
				if err != nil || receipt.TargetSHA != target || receipt.Candidate.SHA == candidate || len(receipt.TargetRefreshes) != 1 {
					t.Fatalf("native refresh receipt %+v %v", receipt, err)
				}
				refresh := receipt.TargetRefreshes[0]
				if refresh.PreviousTargetSHA != base || refresh.NewTargetSHA != target || refresh.PreviousCandidateSHA != candidate || refresh.NewCandidateSHA != receipt.Candidate.SHA || refresh.RecordedAt.IsZero() {
					t.Fatalf("native refresh identities %+v", refresh)
				}
				for _, ancestor := range []string{candidate, target} {
					runEngineGit(t, source.WorktreeDir, "merge-base", "--is-ancestor", ancestor, receipt.Candidate.SHA)
				}
			} else {
				want := "failed to merge cleanly"
				if mode == "conflict" {
					want = "conflicts in shared.txt"
				}
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("native merge refusal %v", err)
				}
				after, err := json.Marshal(receipt)
				if err != nil || string(after) != string(before) || strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")) != candidate {
					t.Fatalf("merge refusal changed receipt/HEAD %v", err)
				}
				if got := runEngineGit(t, source.WorktreeDir, "status", "--porcelain=v1"); strings.TrimSpace(got) != "" {
					t.Fatalf("merge abort left changes %q", got)
				}
			}
			if got, err := os.ReadFile(source.WorkLogPath); err != nil || !reflect.DeepEqual(got, claim) {
				t.Fatalf("refresh changed claim %v", err)
			}
			if got := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+source.Branch)); got != candidate {
				t.Fatalf("refresh republished source %s", got)
			}
			if got := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); got != target {
				t.Fatalf("refresh moved target %s", got)
			}
			if _, err := worktrees.Guard(t.Context(), source.WorktreeDir, worktrees.GuardOptions{ProjectsRoot: fixture.githubDir, Base: "main"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
