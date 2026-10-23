//go:build e2e

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

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/runner"
)

type keepNativeStageGit struct {
	Git
	t                                *testing.T
	stage, canonical, base, worktree string
	cause                            error
	cancel                           context.CancelFunc
	events                           []string
	aggregate                        []string
	kept                             []string
	cleanup                          bool
}

func (g *keepNativeStageGit) WorktreeAddDetached(ctx context.Context, dir, path, revision string) error {
	g.events = append(g.events, "add")
	g.worktree = path
	if dir != g.canonical || revision != g.base || filepath.Base(path) != "rewrite" {
		g.t.Fatalf("add %q %q %q", dir, path, revision)
	}
	if g.stage == "detached" {
		g.cancel()
		return g.cause
	}
	return g.Git.WorktreeAddDetached(ctx, dir, path, revision)
}
func (g *keepNativeStageGit) CherryPickNoCommit(ctx context.Context, dir string, shas ...string) error {
	g.events = append(g.events, "aggregate")
	if dir != g.worktree || !reflect.DeepEqual(shas, g.aggregate) {
		g.t.Fatalf("aggregate %q %v", dir, shas)
	}
	return g.Git.CherryPickNoCommit(ctx, dir, shas...)
}
func (g *keepNativeStageGit) CherryPick(ctx context.Context, dir, sha string) error {
	g.events = append(g.events, "keep")
	g.kept = append(g.kept, sha)
	if dir != g.worktree {
		g.t.Fatalf("kept cwd %q", dir)
	}
	return g.Git.CherryPick(ctx, dir, sha)
}
func (g *keepNativeStageGit) CommitNoVerify(ctx context.Context, dir, message string) error {
	g.events = append(g.events, "commit")
	if dir != g.worktree || !strings.HasPrefix(message, "Reviewed change\n\n") || !strings.Contains(message, shortMergeRevision(g.aggregate[0])) || !strings.Contains(message, "reviewer") || !strings.Contains(message, "keep bisectable") {
		g.t.Fatalf("aggregate message %q cwd %q", message, dir)
	}
	if g.stage == "aggregate_commit" {
		return g.cause
	}
	return g.Git.CommitNoVerify(ctx, dir, message)
}
func (g *keepNativeStageGit) RevParse(ctx context.Context, dir, revision string) (string, error) {
	g.events = append(g.events, "head")
	if dir != g.worktree || revision != "HEAD" {
		g.t.Fatalf("final head %q %q", dir, revision)
	}
	return "", g.cause
}
func (g *keepNativeStageGit) PushForceWithLeaseHead(context.Context, string, string, string) error {
	g.t.Fatal("failed rewrite must not publish")
	return g.cause
}
func (g *keepNativeStageGit) WorktreeRemoveForce(ctx context.Context, dir, path string) error {
	g.events = append(g.events, "cleanup")
	deadline, ok := ctx.Deadline()
	remaining := time.Until(deadline)
	if ctx.Err() != nil || !ok || remaining <= 0 || remaining > 30*time.Second || dir != g.canonical || path != g.worktree {
		g.t.Fatalf("cleanup context/path %v %v %v %q %q", ctx.Err(), ok, remaining, dir, path)
	}
	g.cleanup = true
	return g.Git.WorktreeRemoveForce(ctx, dir, path)
}

type keepNativeBuildRunner struct {
	runner.Runner
	t        *testing.T
	git      *keepNativeStageGit
	subjects []string
}

func (r *keepNativeBuildRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if dir != r.git.worktree || name != "sh" || !reflect.DeepEqual(args, []string{"-c", "exit 0"}) || !opts.CaptureCombined {
		r.t.Fatalf("build %q %q %q %+v", dir, name, args, opts)
	}
	r.subjects = append(r.subjects, strings.TrimSpace(runEngineGit(r.t, dir, "show", "-s", "--format=%s", "HEAD")))
	r.git.events = append(r.git.events, "build")
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}

func TestE2EKeepRewriteNativeStageFailuresCleanScratch(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"detached", "aggregate_commit", "final_head"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			fixture := newExplicitRootEngineFixture(t)
			source := createMergeSource(t, fixture, "keep-stage", "feature/source", "first.txt", "first\n")
			for _, name := range []string{"middle.txt", "last.txt"} {
				writeEngineFile(t, filepath.Join(source.WorktreeDir, name), name+"\n")
				runEngineGit(t, source.WorktreeDir, "add", name)
				runEngineGit(t, source.WorktreeDir, "commit", "-m", "feat: add "+name)
			}
			runEngineGit(t, source.WorktreeDir, "push", "origin", source.Branch)
			base := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
			original := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
			commits := orchCovSourceCommits(t, source.WorktreeDir, base, original)
			if len(commits) != 3 {
				t.Fatalf("native source commits %v", commits)
			}
			claim, err := os.ReadFile(source.WorkLogPath)
			if err != nil {
				t.Fatal(err)
			}
			plan, refusal := planKeptCommits(commits, []string{commits[0].SHA, commits[2].SHA})
			if refusal != nil {
				t.Fatalf("plan %+v", refusal)
			}
			view := githubchecks.PullRequestView{Title: "Reviewed change", Number: 7}
			view.Head.SHA = original
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cause := errors.New("reject native " + stage)
			git := &keepNativeStageGit{Git: defaultGit, t: t, stage: stage, canonical: fixture.canonical, base: base, cause: cause, cancel: cancel, aggregate: []string{commits[1].SHA}}
			run := &keepNativeBuildRunner{Runner: defaultRunner, t: t, git: git}
			scratchRoot := t.TempDir()
			landed, head, refusal, err := rewriteBranchForKeptCommitsInTempDir(ctx, scratchRoot, git, run, fixture.canonical, "acme/app", source.Branch, base, plan, view, commits, "reviewer", "keep bisectable", []string{"sh", "-c", "exit 0"})
			if landed != nil || head != "" || refusal != nil || !errors.Is(err, cause) || !git.cleanup {
				t.Fatalf("failure receipt %v %q %+v %v cleanup=%v", landed, head, refusal, err, git.cleanup)
			}
			wantEvents := []string{"add", "cleanup"}
			var wantKept, wantSubjects []string
			if stage == "aggregate_commit" {
				wantEvents = []string{"add", "keep", "build", "aggregate", "commit", "cleanup"}
				wantKept = []string{commits[0].SHA}
				wantSubjects = []string{commits[0].Subject}
			}
			if stage == "final_head" {
				wantEvents = []string{"add", "keep", "build", "aggregate", "commit", "keep", "build", "head", "cleanup"}
				wantKept = []string{commits[0].SHA, commits[2].SHA}
				wantSubjects = []string{commits[0].Subject, commits[2].Subject}
			}
			if !reflect.DeepEqual(git.events, wantEvents) || !reflect.DeepEqual(git.kept, wantKept) || !reflect.DeepEqual(run.subjects, wantSubjects) {
				t.Fatalf("sequence %v kept %v builds %v", git.events, git.kept, run.subjects)
			}
			entries, err := os.ReadDir(scratchRoot)
			if err != nil || len(entries) != 0 {
				t.Fatalf("scratch residue %v %v", entries, err)
			}
			if _, err := os.Stat(git.worktree); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rewrite path remains: %v", err)
			}
			if listing := runEngineGit(t, fixture.canonical, "worktree", "list", "--porcelain"); strings.Contains(listing, git.worktree) {
				t.Fatalf("registered rewrite remains %s", listing)
			}
			if got := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); got != original {
				t.Fatalf("source moved %s", got)
			}
			if got := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+source.Branch)); got != original {
				t.Fatalf("remote moved %s", got)
			}
			if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != base {
				t.Fatalf("canonical moved %s", got)
			}
			after, err := os.ReadFile(source.WorkLogPath)
			if err != nil || !reflect.DeepEqual(after, claim) {
				t.Fatalf("claim changed %v", err)
			}
		})
	}
}

type keepFetchedBaseFailureGit struct {
	Git
	t                 *testing.T
	canonical, branch string
	cause             error
	events            []string
}

func (g *keepFetchedBaseFailureGit) FetchRefs(ctx context.Context, dir, remote string, refs ...string) error {
	g.events = append(g.events, "fetch")
	if dir != g.canonical || remote != "origin" || !reflect.DeepEqual(refs, []string{"main", g.branch}) {
		g.t.Fatalf("fetch %q %q %v", dir, remote, refs)
	}
	return g.Git.FetchRefs(ctx, dir, remote, refs...)
}
func (g *keepFetchedBaseFailureGit) RevParse(_ context.Context, dir, revision string) (string, error) {
	g.events = append(g.events, "base")
	if dir != g.canonical || revision != "refs/remotes/origin/main" {
		g.t.Fatalf("base %q %q", dir, revision)
	}
	return "", g.cause
}
func (g *keepFetchedBaseFailureGit) WorktreeAddDetached(context.Context, string, string, string) error {
	g.t.Fatal("base failure must precede scratch checkout")
	return g.cause
}
func (g *keepFetchedBaseFailureGit) PushForceWithLeaseHead(context.Context, string, string, string) error {
	g.t.Fatal("base failure must not publish")
	return g.cause
}

func TestE2EKeepRewriteFetchedBaseFailurePreservesSource(t *testing.T) {
	t.Parallel()
	fixture, source, p := nativeLandOwnerProtocol(t)
	ctx := nativeLandOwnerContext(t, p)
	commits := orchCovSourceCommits(t, source.WorktreeDir, p.target, p.head)
	claim, err := os.ReadFile(source.WorkLogPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeListing := runEngineGit(t, fixture.canonical, "worktree", "list", "--porcelain")
	cause := errors.New("fetched base cannot be resolved")
	git := &keepFetchedBaseFailureGit{Git: defaultGit, t: t, canonical: fixture.canonical, branch: source.Branch, cause: cause}
	view := githubchecks.PullRequestView{}
	view.Head.Ref, view.Head.SHA, view.Base.Ref = source.Branch, p.head, "main"
	landed, head, refusal, err := landKeepingCommits(ctx, PullRequestLandOptions{Repository: "acme/app", ProjectsRoot: fixture.githubDir, KeepCommits: []string{commits[0].SHA}, git: git, run: defaultRunner}, view, commits, "7", "reviewer")
	if landed != nil || head != "" || refusal != nil || err != cause || !reflect.DeepEqual(git.events, []string{"fetch", "base"}) {
		t.Fatalf("base failure %v %q %+v %v calls %v", landed, head, refusal, err, git.events)
	}
	if got := runEngineGit(t, fixture.canonical, "worktree", "list", "--porcelain"); got != beforeListing {
		t.Fatalf("worktree inventory changed %s", got)
	}
	for _, dir := range []string{source.WorktreeDir, fixture.repository.CloneURL} {
		revision := "HEAD"
		if dir == fixture.repository.CloneURL {
			revision = "refs/heads/" + source.Branch
		}
		if got := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", revision)); got != p.head {
			t.Fatalf("source identity moved %s %s", dir, got)
		}
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != p.target {
		t.Fatalf("canonical moved %s", got)
	}
	after, err := os.ReadFile(source.WorkLogPath)
	if err != nil || !reflect.DeepEqual(after, claim) {
		t.Fatalf("claim changed %v", err)
	}
}
