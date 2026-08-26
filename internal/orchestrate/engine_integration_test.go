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

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/wbhome"
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

// TestRunFallsBackToRepositoryDefaultBranchForDownstreamWaveOperation pins
// that a repository whose default branch is not the operation's configured
// --ref (default "main") is not just tolerated during graph discovery: any
// downstream wave operation — worktree creation, commit, and the eventual
// pull request base — must use the repository's actual default branch too.
// Before EnsureCanonical returned the resolved base, this exact repository
// state made `wb deps bump go --fleet` fail outright for every fleet
// repository whose default branch is "master".
func TestRunFallsBackToRepositoryDefaultBranchForDownstreamWaveOperation(t *testing.T) {
	fixture := newEngineFixtureOnBranch(t, "master")
	options := fixture.options()
	options.Commit = true
	results, err := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, options)
	if err != nil {
		t.Fatal(err)
	}
	result := results[0]
	if result.Status != "committed" || result.Commit == "" {
		t.Fatalf("result = %+v", result)
	}
	if result.Ref != "master" {
		t.Fatalf("result.Ref = %q, want the repository's actual default branch %q", result.Ref, "master")
	}
	if ahead := strings.TrimSpace(runEngineGit(t, result.WorktreeDir, "rev-list", "origin/master..HEAD")); ahead == "" {
		t.Fatalf("worktree was not branched from origin/master")
	}
}

// TestEnsureCanonicalFallsBackToDefaultBranchWhenConfiguredRefIsAbsent is a
// focused unit test of EnsureCanonical itself: the function callers actually
// depend on for base-ref resolution, isolated from the rest of the Run
// pipeline exercised above.
func TestEnsureCanonicalFallsBackToDefaultBranchWhenConfiguredRefIsAbsent(t *testing.T) {
	fixture := newEngineFixtureOnBranch(t, "master")
	resolved, err := EnsureCanonical(context.Background(), fixture.repository, fixture.canonical, Options{
		GitHubDir: fixture.githubDir, Ref: "main", Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("EnsureCanonical returned an error instead of falling back: %v", err)
	}
	if resolved.Ref != "master" || !resolved.Fallback {
		t.Fatalf("resolved = %+v, want ref=master fallback=true", resolved)
	}
}

// TestEnsureCanonicalFailsWhenNeitherConfiguredRefNorDefaultBranchResolve
// pins the floor: a repository whose origin has no resolvable ref at all
// must still fail loudly rather than silently resolving to nothing.
func TestEnsureCanonicalFailsWhenNeitherConfiguredRefNorDefaultBranchResolve(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	runEngineGit(t, root, "init", "--bare", remote)
	canonical := filepath.Join(root, "projects", "acme", "broken")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, root, "clone", remote, canonical)
	_, err := EnsureCanonical(context.Background(), Repository{Slug: "acme/broken", Path: canonical, CloneURL: remote}, canonical, Options{
		GitHubDir: filepath.Join(root, "projects"), Ref: "main", Timeout: time.Minute,
	})
	if err == nil {
		t.Fatal("a repository with no resolvable ref at all must fail, not silently resolve")
	}
	if !strings.Contains(err.Error(), "acme/broken") {
		t.Fatalf("error must name the repository; got %v", err)
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
	var events []progress.Event
	results, err := Run(context.Background(), []Repository{{Slug: "acme/retired", Archived: true}}, textHandler{}, Options{
		GitHubDir: directory, Operation: "archived-test", DryRun: true,
		Progress: func(event progress.Event) { events = append(events, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != "skipped" || results[0].Reason != "repository is archived" {
		t.Fatalf("result = %+v", results[0])
	}
	if len(events) != 2 || events[0].State != progress.Started || events[1].State != progress.Completed || events[1].Completed != 1 || events[1].Total != 1 {
		t.Fatalf("progress events = %#v", events)
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

func TestWaitAndMergeRequiresStableProducerAwareExactHeadReceipt(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "checks-state")
	policyState := filepath.Join(t.TempDir(), "policy-state")
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then
  echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then
  echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'
  exit 0
fi
if [ "$1" = pr ] && [ "$2" = checks ]; then
  count=0
  if [ -f "$WB_CHECK_STATE" ]; then count=$(cat "$WB_CHECK_STATE"); fi
  count=$((count + 1)); printf '%s' "$count" > "$WB_CHECK_STATE"
  if [ "$count" -eq 1 ]; then
    echo "no checks reported on the branch" >&2
    exit 1
  fi
  echo '[{"name":"CI","bucket":"pass","link":"https://example.test/check"}]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then
	count=0
	if [ -f "$WB_POLICY_STATE" ]; then count=$(cat "$WB_POLICY_STATE"); fi
	count=$((count + 1)); printf '%s' "$count" > "$WB_POLICY_STATE"
  echo '{"protected":true,"protection":{"required_status_checks":{"checks":[{"context":"CI","app_id":42}]}}}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main/protection/required_status_checks' ]; then
  echo '{"strict":true,"contexts":[],"checks":[{"context":"CI","app_id":42}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then
  echo '[[]]'
  exit 0
fi
if [ "$1" = pr ] && [ "$2" = merge ]; then
  count=$(cat "$WB_CHECK_STATE")
  if [ "$count" -lt 6 ]; then
    echo "merge attempted before stable exact-head reread: $count" >&2
    exit 31
  fi
  case " $* " in
    *" --match-head-commit 0123456789012345678901234567890123456789 "*) ;;
    *) echo "merge omitted exact head guard: $*" >&2; exit 32;;
  esac
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 2
`
	writeEngineFile(t, filepath.Join(bin, "gh"), script)
	if err := os.Chmod(filepath.Join(bin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WB_CHECK_STATE", state)
	t.Setenv("WB_POLICY_STATE", policyState)

	result := Result[string]{Repository: "acme/app", WorktreeDir: t.TempDir(), PR: "https://github.com/acme/app/pull/1", Ref: "main", Commit: "0123456789012345678901234567890123456789"}
	// This exercises retry semantics, not a process-start SLA. Under the full
	// suite's parallel package load, a shell process can be scheduled late
	// enough to hit a short artificial boundary before it runs at all. The
	// outer context keeps a broken fixture bounded without conflating scheduler
	// delay with the merge policy being tested.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := waitAndMerge(ctx, Options{Timeout: 30 * time.Second, CheckPollInterval: time.Millisecond}, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Merged || result.Status != "merged" || len(result.Checks) < 2 || !strings.Contains(result.Reason, "producer-aware") {
		t.Fatalf("result = %+v", result)
	}
	if count, err := os.ReadFile(state); err != nil || strings.TrimSpace(string(count)) != "7" {
		t.Fatalf("stabilized waiter did not perform the expected observed/required rereads plus the final fresh-policy receipt: count=%q err=%v", count, err)
	}
	if count, err := os.ReadFile(policyState); err != nil || strings.TrimSpace(string(count)) != "2" {
		t.Fatalf("branch policy was not cached during pending observations and refreshed before pass: count=%q err=%v", count, err)
	}
}

func TestWaitAndMergeLeavesPullRequestUnmergedWhenProtectedMergeRejectsLateTargetAdvance(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ]; then echo '{"headRefOid":"0123456789012345678901234567890123456789","baseRefName":"main"}'; exit 0; fi
if [ "$1" = pr ] && [ "$2" = checks ]; then echo '[{"name":"CI","bucket":"pass"}]'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa...0123456789012345678901234567890123456789'; then echo '{"status":"ahead","base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"merge_base_commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then echo '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then echo '{"total_count":0,"statuses":[]}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then echo '{"protected":true,"protection":{"required_status_checks":{"checks":[{"context":"CI","app_id":42}]}}}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main/protection/required_status_checks' ]; then echo '{"strict":true,"contexts":[],"checks":[{"context":"CI","app_id":42}]}'; exit 0; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then echo '[[]]'; exit 0; fi
if [ "$1" = pr ] && [ "$2" = merge ]; then echo 'base branch advanced; strict update required' >&2; exit 17; fi
echo "unexpected gh args: $*" >&2; exit 2
`
	writeEngineFile(t, filepath.Join(bin, "gh"), script)
	if err := os.Chmod(filepath.Join(bin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	result := Result[string]{Repository: "acme/app", WorktreeDir: t.TempDir(), PR: "https://github.com/acme/app/pull/1", Ref: "main", Commit: "0123456789012345678901234567890123456789"}
	err := waitAndMerge(context.Background(), Options{Timeout: 30 * time.Second, CheckPollInterval: time.Millisecond}, &result)
	if err == nil || !strings.Contains(err.Error(), "base branch advanced") {
		t.Fatalf("late target advance error = %v", err)
	}
	if result.Merged || result.Status == "merged" {
		t.Fatalf("protected merge rejection was reported as merged: %+v", result)
	}
}

type engineFixture struct {
	githubDir  string
	canonical  string
	repository Repository
}

func newEngineFixture(t *testing.T) engineFixture {
	t.Helper()
	return newEngineFixtureOnBranch(t, "main")
}

// newEngineFixtureOnBranch mirrors newEngineFixture but seeds the remote and
// canonical clone on an arbitrary default branch, so tests can pin
// EnsureCanonical's default-branch fallback (see
// TestRunFallsBackToRepositoryDefaultBranchForDownstreamWaveOperation) without
// duplicating the whole fixture.
func newEngineFixtureOnBranch(t *testing.T, branch string) engineFixture {
	t.Helper()
	root := t.TempDir()
	// Scope WB_HOME to this fixture's own root. Without this, a fresh temp
	// githubDir has no legacy .wb, so wbhome.Root falls through to the real
	// ~/.wb. Scoping it per fixture, not shared package-wide, also keeps this
	// test's worktree root unique from the other tests in this file that reuse
	// the same "dependency-test" operation name.
	t.Setenv(wbhome.EnvOverride, filepath.Join(root, ".wb"))
	seed := filepath.Join(root, "seed")
	remote := filepath.Join(root, "remote.git")
	githubDir := filepath.Join(root, "projects")
	canonical := filepath.Join(githubDir, "acme", "app")
	writeEngineFile(t, filepath.Join(seed, "dependency.txt"), "old\n")
	runEngineGit(t, seed, "init", "-b", branch)
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
