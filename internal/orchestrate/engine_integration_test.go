package orchestrate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type textHandler struct{}

func (textHandler) Inspect(ctx context.Context, canonical, base string, _ Repository) (Assessment[string], error) {
	contents, _, err := runCommand(ctx, time.Minute, 0, canonical, "git", "show", base+":dependency.txt")
	if err != nil {
		return Assessment[string]{}, err
	}
	if !strings.Contains(contents, "old") {
		return Assessment[string]{Metadata: contents, Applicable: true, Reason: "already current"}, nil
	}
	return Assessment[string]{Metadata: contents, Applicable: true, NeedsChange: true, Reason: "requires update"}, nil
}

func (textHandler) Apply(_ context.Context, worktree string, _ Repository) (string, error) {
	path := filepath.Join(worktree, "dependency.txt")
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	updated := strings.ReplaceAll(string(contents), "old", "new")
	return updated, os.WriteFile(path, []byte(updated), 0o644)
}

func (textHandler) CommitMessage(Repository) string { return "chore: update dependency" }

func (textHandler) ValidatePublishable(context.Context, string, Repository) error { return nil }
func (textHandler) PullRequest(Repository) (string, string) {
	return "Update dependency", "Automated test update."
}

func TestRunIsolatesDirtyCanonicalClone(t *testing.T) {
	fixture := newEngineFixture(t)
	dirty := filepath.Join(fixture.canonical, "notes.txt")
	writeEngineFile(t, dirty, "unfinished\n")
	results, err := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, fixture.options())
	if err != nil {
		t.Fatal(err)
	}
	result := results[0]
	if result.Status != "changed" || len(result.ChangedFiles) != 1 || result.Metadata != "new\n" {
		t.Fatalf("result = %+v", result)
	}
	canonical := mustReadEngineFile(t, filepath.Join(fixture.canonical, "dependency.txt"))
	if canonical != "old\n" || mustReadEngineFile(t, dirty) != "unfinished\n" {
		t.Fatalf("canonical clone changed: dependency=%q dirty=%q", canonical, mustReadEngineFile(t, dirty))
	}
	if worktree := mustReadEngineFile(t, filepath.Join(result.WorktreeDir, "dependency.txt")); worktree != "new\n" {
		t.Fatalf("worktree dependency = %q", worktree)
	}
}

func TestRunDryRunCreatesNoOperationState(t *testing.T) {
	fixture := newEngineFixture(t)
	options := fixture.options()
	options.DryRun = true
	results, err := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, options)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != "planned" || results[0].Reason != "requires update" {
		t.Fatalf("result = %+v", results[0])
	}
	if _, err := os.Stat(filepath.Join(fixture.githubDir, ".wb")); !os.IsNotExist(err) {
		t.Fatalf("dry run created operation state: %v", err)
	}
}

func TestRunCommitsWithoutPushing(t *testing.T) {
	fixture := newEngineFixture(t)
	options := fixture.options()
	options.Commit = true
	results, err := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, options)
	if err != nil {
		t.Fatal(err)
	}
	result := results[0]
	if result.Status != "committed" || result.Commit == "" || result.Pushed {
		t.Fatalf("result = %+v", result)
	}
	message := strings.TrimSpace(runEngineGit(t, result.WorktreeDir, "log", "-1", "--format=%s"))
	if message != "chore: update dependency" {
		t.Fatalf("message = %q", message)
	}
}

type rejectingPublishHandler struct{ textHandler }

func (rejectingPublishHandler) ValidatePublishable(context.Context, string, Repository) error {
	return errors.New("local replacement remains")
}

func TestRunValidatesPublishabilityBeforeCommit(t *testing.T) {
	fixture := newEngineFixture(t)
	options := fixture.options()
	options.Commit = true
	results, err := Run(context.Background(), []Repository{fixture.repository}, rejectingPublishHandler{}, options)
	if err == nil || results[0].Status != "failed" || !strings.Contains(results[0].Reason, "local replacement remains") {
		t.Fatalf("result = %+v, error = %v", results[0], err)
	}
	if ahead := strings.TrimSpace(runEngineGit(t, results[0].WorktreeDir, "rev-list", "origin/main..HEAD")); ahead != "" {
		t.Fatalf("publishability failure created commit %s", ahead)
	}
}

func TestRunSkipsArchivedRepository(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	results, err := Run(context.Background(), []Repository{{Slug: "acme/retired", Archived: true}}, textHandler{}, Options{
		GitHubDir: directory, Operation: "archived-test", DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != "skipped" || results[0].Reason != "repository is archived" {
		t.Fatalf("result = %+v", results[0])
	}
}

func TestNormalizePublicationImplicationsAndValidation(t *testing.T) {
	t.Parallel()
	options, err := Normalize(Options{GitHubDir: t.TempDir(), Operation: "test", Merge: true})
	if err != nil {
		t.Fatal(err)
	}
	if !options.Commit || !options.Push || !options.PR || !options.Merge || options.Ref != "main" || options.Parallel != 1 {
		t.Fatalf("options = %+v", options)
	}
	if _, err := Normalize(Options{GitHubDir: t.TempDir()}); err == nil {
		t.Fatal("missing operation was accepted")
	}
	if _, err := Normalize(Options{GitHubDir: t.TempDir(), Operation: "test", Parallel: -1}); err == nil {
		t.Fatal("negative parallelism was accepted")
	}
	if _, err := Normalize(Options{GitHubDir: t.TempDir(), Operation: "test", DryRun: true, Commit: true}); err == nil {
		t.Fatal("dry-run commit was accepted")
	}
}

func TestWaitAndMergeRetriesUntilGitHubChecksAppear(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "checks-state")
	script := `#!/bin/sh
if [ "$2" = checks ]; then
  if [ ! -f "$WB_CHECK_STATE" ]; then
    : > "$WB_CHECK_STATE"
    echo "no checks reported on the branch" >&2
    exit 1
  fi
  echo '[{"name":"CI","bucket":"pass","link":"https://example.test/check"}]'
  exit 0
fi
if [ "$2" = merge ]; then
  exit 0
fi
exit 2
`
	writeEngineFile(t, filepath.Join(bin, "gh"), script)
	if err := os.Chmod(filepath.Join(bin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WB_CHECK_STATE", state)

	result := Result[string]{WorktreeDir: t.TempDir(), PR: "https://github.com/acme/app/pull/1"}
	if err := waitAndMerge(context.Background(), Options{Timeout: time.Second, CheckPollInterval: time.Millisecond}, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Merged || result.Status != "merged" || len(result.Checks) != 1 || result.Checks[0].Bucket != "pass" {
		t.Fatalf("result = %+v", result)
	}
}

type engineFixture struct {
	githubDir  string
	canonical  string
	repository Repository
}

func newEngineFixture(t *testing.T) engineFixture {
	t.Helper()
	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	remote := filepath.Join(root, "remote.git")
	githubDir := filepath.Join(root, "projects")
	canonical := filepath.Join(githubDir, "acme", "app")
	writeEngineFile(t, filepath.Join(seed, "dependency.txt"), "old\n")
	runEngineGit(t, seed, "init", "-b", "main")
	runEngineGit(t, seed, "config", "user.name", "WB Test")
	runEngineGit(t, seed, "config", "user.email", "wb@example.test")
	runEngineGit(t, seed, "add", "-A")
	runEngineGit(t, seed, "commit", "-m", "initial")
	runEngineGit(t, root, "clone", "--bare", seed, remote)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, root, "clone", remote, canonical)
	runEngineGit(t, canonical, "config", "user.name", "WB Test")
	runEngineGit(t, canonical, "config", "user.email", "wb@example.test")
	return engineFixture{githubDir: githubDir, canonical: canonical, repository: Repository{Slug: "acme/app", Path: canonical, CloneURL: remote}}
}

func (fixture engineFixture) options() Options {
	return Options{GitHubDir: fixture.githubDir, Operation: "dependency-test", Branch: "wb/deps/test", Ref: "main", Timeout: time.Minute}
}

func writeEngineFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustReadEngineFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func runEngineGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
