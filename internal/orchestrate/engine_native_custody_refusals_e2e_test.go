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

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestE2EEngineManagedInputNativeIdentityRefusals(t *testing.T) {
	t.Parallel()
	t.Run("canonical_gitdir_file", func(t *testing.T) {
		t.Parallel()
		fixture := newExplicitRootEngineFixture(t)
		head := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
		gitDir := filepath.Join(filepath.Dir(fixture.canonical), "private-app-gitdir")
		if err := os.Rename(filepath.Join(fixture.canonical, ".git"), gitDir); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture.canonical, ".git"), []byte("gitdir: "+filepath.ToSlash(gitDir)+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		actual, err := worktrees.Guard(context.Background(), fixture.canonical, worktrees.GuardOptions{ProjectsRoot: fixture.githubDir, Base: "main"})
		if err != nil || actual.Kind != "canonical" {
			t.Fatalf("native canonical custody %+v %v", actual, err)
		}
		guard, err := managedInputWorktree(context.Background(), fixture.canonical, fixture.repository.Slug, Options{GitHubDir: fixture.githubDir, Ref: "main"})
		if guard != nil || err == nil || !strings.Contains(err.Error(), "is not a linked worktree") {
			t.Fatalf("nonlinked refusal %+v %v", guard, err)
		}
		if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != head {
			t.Fatalf("canonical moved %s", got)
		}
		if got := runEngineGit(t, fixture.canonical, "status", "--porcelain"); strings.TrimSpace(got) != "" {
			t.Fatalf("canonical changed %s", got)
		}
		if got := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); got != head {
			t.Fatalf("remote changed %s", got)
		}
		if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "--absolute-git-dir")); filepath.Clean(got) != filepath.Clean(gitDir) {
			t.Fatalf("gitdir custody changed %s", got)
		}
	})
	t.Run("invalid_expected_repository", func(t *testing.T) {
		t.Parallel()
		fixture := newExplicitRootEngineFixture(t)
		source := createMergeSource(t, fixture, "engine-input", "feature/input", "input.txt", "native input\n")
		head := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
		base := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
		claim, err := os.ReadFile(source.WorkLogPath)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := worktrees.Guard(context.Background(), source.WorktreeDir, worktrees.GuardOptions{ProjectsRoot: fixture.githubDir, Base: "main"})
		if err != nil || actual.Kind != "linked" {
			t.Fatalf("native linked custody %+v %v", actual, err)
		}
		_, expected := worktrees.CanonicalRepositoryPath(fixture.githubDir, "invalid-single-segment")
		if expected == nil {
			t.Fatal("fixture repository must be invalid")
		}
		guard, err := managedInputWorktree(context.Background(), source.WorktreeDir, "invalid-single-segment", Options{GitHubDir: fixture.githubDir, Ref: "main"})
		if guard != nil || err == nil || err.Error() != expected.Error() {
			t.Fatalf("expected path refusal %+v %v want %v", guard, err, expected)
		}
		if got := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); got != head {
			t.Fatalf("source moved %s", got)
		}
		if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != base {
			t.Fatalf("canonical moved %s", got)
		}
		if got := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); got != base {
			t.Fatalf("remote moved %s", got)
		}
		after, err := os.ReadFile(source.WorkLogPath)
		if err != nil || !reflect.DeepEqual(after, claim) {
			t.Fatalf("claim changed %v", err)
		}
		// Successful linked admission uses the same real guard and canonical path.
		admitted, err := managedInputWorktree(context.Background(), source.WorktreeDir, fixture.repository.Slug, Options{GitHubDir: fixture.githubDir, Ref: "main"})
		if err != nil || admitted == nil || admitted.Kind != "linked" || filepath.Clean(admitted.CanonicalDir) != filepath.Clean(fixture.canonical) {
			t.Fatalf("native linked admission %+v %v", admitted, err)
		}
	})
}

type engineFallbackVerificationRunner struct {
	runner.Runner
	t         *testing.T
	canonical string
	cause     error
	calls     [][]string
}

func (r *engineFallbackVerificationRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if dir != r.canonical || name != "git" || !opts.CaptureCombined || !reflect.DeepEqual(opts.Env, console.Env()) {
		r.t.Fatalf("canonical command %q %q %v %+v", dir, name, args, opts)
	}
	r.calls = append(r.calls, append([]string{name}, args...))
	if reflect.DeepEqual(args, []string{"rev-parse", "--verify", "origin/main^{commit}"}) {
		return runner.Result{}, r.cause
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}

func TestE2EEngineCanonicalFallbackVerificationPreservesCheckout(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	head := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	before := runEngineGit(t, fixture.canonical, "status", "--porcelain")
	cause := errors.New("default branch verification refused")
	run := &engineFallbackVerificationRunner{Runner: defaultRunner, t: t, canonical: fixture.canonical, cause: cause}
	options := Options{GitHubDir: fixture.githubDir, Ref: "absent-configured-ref", run: run}
	got, err := EnsureCanonical(context.Background(), fixture.repository, fixture.canonical, options)
	if got != (ResolvedBase{}) || !errors.Is(err, cause) || !strings.Contains(err.Error(), "does not contain origin/absent-configured-ref, and its default branch origin/main also failed verification") || options.Ref != "absent-configured-ref" {
		t.Fatalf("fallback verification %+v %v", got, err)
	}
	want := [][]string{{"git", "fetch", "--quiet", "origin"}, {"git", "rev-parse", "--verify", "origin/absent-configured-ref^{commit}"}, {"git", "symbolic-ref", "refs/remotes/origin/HEAD"}, {"git", "rev-parse", "--verify", "origin/main^{commit}"}}
	if !reflect.DeepEqual(run.calls, want) {
		t.Fatalf("native fallback order %v", run.calls)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != head {
		t.Fatalf("canonical moved %s", got)
	}
	if got := runEngineGit(t, fixture.canonical, "status", "--porcelain"); got != before {
		t.Fatalf("canonical contents changed %s", got)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); got != head {
		t.Fatalf("remote moved %s", got)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "--verify", "origin/main^{commit}")); got != head {
		t.Fatalf("native fetched default identity %s", got)
	}
}
