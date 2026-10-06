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

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type engineStageObservedRunner struct {
	runner.Runner
	name     string
	args     []string
	cause    error
	consumed int
	after    func(string, string, []string) error
}

func (r *engineStageObservedRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if name == r.name && (r.args == nil || reflect.DeepEqual(args, r.args)) {
		r.consumed++
		return runner.Result{ExitCode: 1}, r.cause
	}
	result, err := r.Runner.RunOpts(ctx, dir, opts, name, args...)
	if err == nil && r.after != nil {
		err = r.after(dir, name, args)
	}
	return result, err
}

type engineStageInspectionHandler struct {
	textHandler
	before func() error
}

func (h engineStageInspectionHandler) Inspect(ctx context.Context, dir, base string, repository Repository) (Assessment[string], error) {
	result, err := h.textHandler.Inspect(ctx, dir, base, repository)
	if err == nil {
		err = h.before()
	}
	return result, err
}

type engineInPlaceInspectionHandler struct {
	textHandler
	inspected *int
}

func (h engineInPlaceInspectionHandler) InspectWorkingTree(ctx context.Context, dir string, repository Repository) (Assessment[string], error) {
	*h.inspected++
	return h.Inspect(ctx, dir, "HEAD", repository)
}

func TestE2EEngineRepositoryStageFailuresPreserveCanonicalContents(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name            string
		args            []string
		commit, managed bool
	}{
		{name: "creation manifest read", args: []string{"rev-parse", "origin/main"}},
		{name: "managed baseline status", args: []string{"status", "--porcelain=v1", "-z"}, managed: true},
		{name: "changed file status", args: []string{"status", "--porcelain=v1", "-z"}},
		{name: "branch ahead", args: []string{"rev-list", "origin/main..HEAD"}},
		{name: "stage changed files", args: []string{"add", "-A"}, commit: true},
		{name: "commit changed files", args: []string{"commit", "-m", "chore: update dependency"}, commit: true},
		{name: "read committed head", args: []string{"rev-parse", "HEAD"}, commit: true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExplicitRootEngineFixture(t)
			repository := fixture.repository
			options, normalizeErr := Normalize(fixture.options())
			if normalizeErr != nil {
				t.Fatal(normalizeErr)
			}
			options.Commit = row.commit
			if row.managed {
				created, err := worktrees.Create(context.Background(), []string{repository.Slug}, worktrees.CreateOptions{ProjectsRoot: fixture.githubDir, Operation: "stage-input", Branch: "feature/stage-input", BranchChosen: true, WorkLog: worktrees.WorkLogOptions{Model: "test"}})
				if err != nil || len(created) != 1 {
					t.Fatalf("managed input: created=%+v err=%v", created, err)
				}
				repository.Path = created[0].WorktreeDir
			}
			cause := errors.New("owned stage refusal: " + row.name)
			observed := &engineStageObservedRunner{Runner: defaultRunner, name: "git", args: row.args, cause: cause}
			options.run = observed
			before := mustReadEngineFile(t, filepath.Join(fixture.canonical, "dependency.txt"))
			result := Result[string]{Repository: repository.Slug}
			err := processRepository(context.Background(), repository, textHandler{}, options, &result)
			if !errors.Is(err, cause) || result.Status != "failed" || observed.consumed != 1 {
				t.Fatalf("stage: result=%+v err=%v consumed=%d", result, err, observed.consumed)
			}
			if after := mustReadEngineFile(t, filepath.Join(fixture.canonical, "dependency.txt")); after != before {
				t.Fatalf("canonical changed: before=%q after=%q", before, after)
			}
			if row.name == "creation manifest read" {
				if _, err := os.Stat(result.WorktreeDir); err != nil {
					t.Fatalf("manifest refusal lost actual created checkout: %v", err)
				}
			}
		})
	}
}

func TestE2EEngineRepositoryPhysicalPreparationFailures(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"canonical disappeared", "blocked write home", "existing branch"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fixture := newExplicitRootEngineFixture(t)
			options, normalizeErr := Normalize(fixture.options())
			if normalizeErr != nil {
				t.Fatal(normalizeErr)
			}
			moved := fixture.canonical + "-held"
			didMove := false
			t.Cleanup(func() {
				if didMove {
					if err := os.Rename(moved, fixture.canonical); err != nil {
						t.Error(err)
					}
				}
			})
			handler := engineStageInspectionHandler{textHandler: textHandler{}, before: func() error {
				if mode == "blocked write home" {
					return os.WriteFile(filepath.Join(fixture.githubDir, ".wb"), []byte("physical blocker"), 0600)
				}
				if mode == "existing branch" {
					_, _, err := runCommand(context.Background(), defaultRunner, options.Timeout, 0, fixture.canonical, "git", "branch", options.Branch)
					return err
				}
				return nil
			}}
			observed := &engineStageObservedRunner{Runner: defaultRunner, after: func(dir, name string, args []string) error {
				if mode == "canonical disappeared" && name == "git" && reflect.DeepEqual(args, []string{"rev-parse", "--verify", "origin/main^{commit}"}) {
					if err := os.Rename(fixture.canonical, moved); err != nil {
						return err
					}
					didMove = true
				}
				return nil
			}}
			options.run = observed
			result := Result[string]{Repository: fixture.repository.Slug}
			err := processRepository(context.Background(), fixture.repository, handler, options, &result)
			if err == nil || result.Status != "failed" {
				t.Fatalf("physical refusal: result=%+v err=%v", result, err)
			}
			switch mode {
			case "canonical disappeared":
				if !didMove || !strings.Contains(err.Error(), "resolve canonical repository path") {
					t.Fatalf("wrong physical stage: moved=%t err=%v", didMove, err)
				}
			case "existing branch":
				if !strings.Contains(err.Error(), "operation branch already exists") {
					t.Fatalf("wrong branch refusal: %v", err)
				}
			case "blocked write home":
				if info, statErr := os.Stat(filepath.Join(fixture.githubDir, ".wb")); statErr != nil || !info.Mode().IsRegular() {
					t.Fatalf("home blocker absent: %v", statErr)
				}
			}
			canonical := fixture.canonical
			if didMove {
				canonical = moved
			}
			if contents := mustReadEngineFile(t, filepath.Join(canonical, "dependency.txt")); contents != "old\n" {
				t.Fatalf("canonical mutation: %q", contents)
			}
		})
	}
}

func TestE2EEngineInPlaceInspectionUsesManagedCheckout(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	created, err := worktrees.Create(context.Background(), []string{fixture.repository.Slug}, worktrees.CreateOptions{ProjectsRoot: fixture.githubDir, Operation: "inplace-inspector", Branch: "feature/inplace-inspector", BranchChosen: true, WorkLog: worktrees.WorkLogOptions{Model: "test"}})
	if err != nil || len(created) != 1 {
		t.Fatalf("managed checkout: %+v %v", created, err)
	}
	repository := fixture.repository
	repository.Path = created[0].WorktreeDir
	inspected := 0
	result := Result[string]{Repository: repository.Slug}
	err = processRepository(context.Background(), repository, engineInPlaceInspectionHandler{textHandler: textHandler{}, inspected: &inspected}, fixture.options(), &result)
	if err != nil || inspected != 1 || result.Status != "changed" || result.WorktreeDir != repository.Path {
		t.Fatalf("inplace result=%+v err=%v inspected=%d", result, err, inspected)
	}
	if after := mustReadEngineFile(t, filepath.Join(fixture.canonical, "dependency.txt")); after != "old\n" {
		t.Fatalf("canonical changed: %q", after)
	}
}

func TestE2EEngineRepositorySelectionFailuresPrecedeInspection(t *testing.T) {
	t.Parallel()
	for _, slug := range []string{"invalid", "../app"} {
		t.Run(slug, func(t *testing.T) {
			t.Parallel()
			inspected := 0
			result := Result[string]{}
			err := processRepository(context.Background(), Repository{Slug: slug}, staticHandler{inspected: &inspected}, Options{GitHubDir: t.TempDir()}, &result)
			if err == nil || result.Status != "failed" || inspected != 0 {
				t.Fatalf("selection result=%+v err=%v inspected=%d", result, err, inspected)
			}
		})
	}
}

func TestE2EEngineRepositoryPullRequestResultReasons(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"verification passed", "checks pending", "verification not run", "create refused"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fixture := newExplicitRootEngineFixture(t)
			options, normalizeErr := Normalize(fixture.options())
			if normalizeErr != nil {
				t.Fatal(normalizeErr)
			}
			options.Commit = true
			options.PR = true
			options.Verify = mode == "verification passed"
			options.Merge = mode == "checks pending"
			ctx := githubobserver.WithReader(context.Background(), githubobserver.Reader{Read: func(context.Context, string, ...string) ([]byte, error) {
				if mode == "create refused" {
					return nil, nil
				}
				return []byte("https://github.test/acme/app/pull/7"), nil
			}})
			observed := &engineStageObservedRunner{Runner: defaultRunner, name: "gh", cause: errors.New("owned PR transport refusal")}
			if mode == "create refused" {
				// The list read is context-local; only the actual create call reaches this runner.
				options.run = observed
			}
			result := Result[string]{Repository: fixture.repository.Slug}
			err := processRepository(ctx, fixture.repository, textHandler{}, options, &result)
			if mode == "create refused" {
				if err == nil || result.Status != "failed" || !strings.Contains(err.Error(), "owned PR transport refusal") || observed.consumed != 1 {
					t.Fatalf("PR refusal result=%+v err=%v", result, err)
				}
				return
			}
			reason := map[string]string{"verification passed": "pull request opened; local verification passed", "checks pending": "pull request opened; exact PR-head GitHub checks are pending", "verification not run": "pull request opened; full local verification was not run"}[mode]
			if err != nil || result.Status != "pr_open" || result.PR != "https://github.test/acme/app/pull/7" || result.Reason != reason {
				t.Fatalf("PR projection result=%+v err=%v wantreason=%q", result, err, reason)
			}
		})
	}
}
