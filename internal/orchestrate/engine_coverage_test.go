package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/quality"
)

func TestOrchCovNormalizeRejectsIncoherentLifecycleOptions(t *testing.T) {
	t.Parallel()
	valid := Options{GitHubDir: t.TempDir(), Operation: "deps-bump", Ref: "main"}
	for _, test := range []struct {
		name    string
		mutate  func(*Options)
		wantIn  string
		cleared bool
	}{
		{name: "no github dir", mutate: func(o *Options) { o.GitHubDir = "  " }, wantIn: "GitHub directory is required"},
		{name: "no operation", mutate: func(o *Options) { o.Operation = "" }, wantIn: "operation identity is required"},
		{name: "zero parallelism", mutate: func(o *Options) { o.Parallel = -1 }, wantIn: "parallelism must be at least 1"},
		{name: "negative retry", mutate: func(o *Options) { o.Retry = -1 }, wantIn: "retry count must not be negative"},
		{name: "negative timeout", mutate: func(o *Options) { o.Timeout = -time.Second }, wantIn: "timeout must not be negative"},
		{name: "wait with merge", mutate: func(o *Options) { o.Merge, o.WaitForPRChecks = true, true }, wantIn: "wait-only PR checks cannot be combined with --merge"},
		{name: "wait without pr", mutate: func(o *Options) { o.WaitForPRChecks = true }, wantIn: "wait-only PR checks require pull-request publication"},
		{name: "dry run with commit", mutate: func(o *Options) { o.DryRun, o.Commit = true, true }, wantIn: "--dry-run cannot be combined"},
		{name: "dry run with resume", mutate: func(o *Options) { o.DryRun, o.Resume = true, true }, wantIn: "--dry-run cannot be combined"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			options := valid
			test.mutate(&options)
			if _, err := Normalize(options); err == nil || !strings.Contains(err.Error(), test.wantIn) {
				t.Fatalf("Normalize error = %v, want %q", err, test.wantIn)
			}
		})
	}
}

func TestOrchCovNormalizeAppliesCumulativePublicationImplications(t *testing.T) {
	t.Parallel()
	options, err := Normalize(Options{GitHubDir: t.TempDir(), Operation: "deps-bump", Merge: true})
	if err != nil {
		t.Fatal(err)
	}
	if !options.PR || !options.Push || !options.Commit {
		t.Fatalf("merge did not imply pr/push/commit: %+v", options)
	}
	if options.Branch != "wb/deps-bump" || options.Ref != "main" || options.Parallel != 1 || options.Model != "unknown" {
		t.Fatalf("derived defaults = %+v", options)
	}
	if options.Prompt == "" || !strings.Contains(options.Prompt, "deps-bump") {
		t.Fatalf("derived prompt = %q", options.Prompt)
	}
	verified, err := Normalize(Options{GitHubDir: t.TempDir(), Operation: "deps-bump", Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(verified.Checks) != 3 {
		t.Fatalf("verify did not imply the default checks: %+v", verified.Checks)
	}
	// A dry run rejects nothing on its own.
	if _, err := Normalize(Options{GitHubDir: t.TempDir(), Operation: "deps-bump", DryRun: true}); err != nil {
		t.Fatalf("dry run rejected: %v", err)
	}
}

func TestOrchCovSplitRepositoryRequiresOwnerAndName(t *testing.T) {
	t.Parallel()
	if owner, name, err := splitRepository("acme/app"); err != nil || owner != "acme" || name != "app" {
		t.Fatalf("splitRepository = %q/%q/%v", owner, name, err)
	}
	for _, slug := range []string{"", "acme", "acme/", "/app", "acme/app/extra"} {
		if _, _, err := splitRepository(slug); err == nil {
			t.Fatalf("splitRepository(%q) accepted an invalid slug", slug)
		}
	}
}

func TestOrchCovWorktreeEffortSegmentSanitizesAndPrefixes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "acme-app", want: "acme-app"},
		{input: "acme/co ext", want: "acme-co-ext"},
		{input: "  ", want: ""},
		{input: ".-_", want: ""},
		{input: "-lead", want: "lead"},
		// Every non-alphanumeric rune is replaced by "-", and the result is
		// trimmed of ".-_", so a surviving segment always starts with an
		// alphanumeric character.
		{input: "é", want: ""},
		{input: "éa", want: "a"},
		{input: "acme corp/ñ", want: "acme-corp"},
	} {
		if got := worktreeEffortSegment(test.input); got != test.want {
			t.Fatalf("worktreeEffortSegment(%q) = %q, want %q", test.input, got, test.want)
		}
	}
	if got := worktreeEffortID("deps-bump", "acme", "app"); got != "deps-bump.acme-app" {
		t.Fatalf("worktreeEffortID = %q", got)
	}
	if got := worktreeEffortID("deps-bump", " ", " "); got != "deps-bump.repository" {
		t.Fatalf("worktreeEffortID for an unsanitizable owner = %q", got)
	}
	if !isASCIIAlphanumeric('a') || !isASCIIAlphanumeric('Z') || !isASCIIAlphanumeric('7') {
		t.Fatal("alphanumerics were rejected")
	}
	if isASCIIAlphanumeric('-') || isASCIIAlphanumeric('é') {
		t.Fatal("a non-alphanumeric byte was accepted")
	}
}

func TestOrchCovParseLsRemoteSymrefReadsTheDefaultBranch(t *testing.T) {
	t.Parallel()
	output := "ref: refs/heads/master\tHEAD\n0123456789abcdef0123456789abcdef01234567\tHEAD\n"
	ref, err := parseLsRemoteSymref(output)
	if err != nil || ref != "master" {
		t.Fatalf("parseLsRemoteSymref = %q, %v", ref, err)
	}
	for _, test := range []struct {
		name   string
		output string
	}{
		{name: "empty"},
		{name: "no symref line", output: "0123456789abcdef0123456789abcdef01234567\tHEAD\n"},
		{name: "symref without a target", output: "ref:   \n"},
		{name: "symref outside refs/heads", output: "ref: refs/tags/v1\tHEAD\n"},
	} {
		if _, err := parseLsRemoteSymref(test.output); err == nil {
			t.Fatalf("%s was accepted as a default branch", test.name)
		}
	}
}

func TestOrchCovReadOriginHeadSymrefRefusesAnUnexpectedRef(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	runEngineGit(t, dir, "init", "-b", "main")
	if _, err := readOriginHeadSymref(context.Background(), dir, Options{Timeout: time.Minute}); err == nil {
		t.Fatal("a repository with no origin/HEAD symref was accepted")
	}
}

func TestOrchCovRunParallelRunsEveryIndexOnce(t *testing.T) {
	t.Parallel()
	runParallel(0, 4, func(int) { t.Fatal("no index should run") })
	seen := make([]int, 4)
	runParallel(4, 8, func(index int) { seen[index]++ })
	for index, count := range seen {
		if count != 1 {
			t.Fatalf("index %d ran %d times", index, count)
		}
	}
}

func TestOrchCovGitHubChecksPollIntervalUsesTheConfiguredOverride(t *testing.T) {
	t.Parallel()
	if got := githubChecksPollInterval(Options{}); got != DefaultCheckPollInterval {
		t.Fatalf("default poll interval = %s", got)
	}
	if got := githubChecksPollInterval(Options{CheckPollInterval: time.Second}); got != time.Second {
		t.Fatalf("configured poll interval = %s", got)
	}
}

// orchCovStageHandler returns a handler whose Inspect verdict is fixed and
// whose later stages can be made to fail.
type orchCovStageHandler struct {
	assessment Assessment[string]
	inspectErr error
	applyErr   error
}

func (handler orchCovStageHandler) Inspect(context.Context, string, string, Repository) (Assessment[string], error) {
	return handler.assessment, handler.inspectErr
}

func (handler orchCovStageHandler) Apply(_ context.Context, worktree string, _ Repository) (string, error) {
	if handler.applyErr != nil {
		return "", handler.applyErr
	}
	path := filepath.Join(worktree, "dependency.txt")
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	updated := strings.ReplaceAll(string(contents), "old", "new")
	return updated, os.WriteFile(path, []byte(updated), 0o644)
}

func (handler orchCovStageHandler) ValidatePublishable(context.Context, string, Repository) error {
	return nil
}

func (handler orchCovStageHandler) CommitMessage(Repository) string {
	return "chore: update dependency"
}

func (handler orchCovStageHandler) PullRequest(Repository) (string, string) {
	return "Update dependency", "Automated test update."
}

// orchCovIdleHandler applies nothing, so the operation produces no change.
type orchCovIdleHandler struct{ textHandler }

func (orchCovIdleHandler) Apply(_ context.Context, worktree string, _ Repository) (string, error) {
	return "", nil
}

func TestOrchCovProcessRepositorySkipsEveryUnapplicableVerdict(t *testing.T) {
	for _, test := range []struct {
		name       string
		assessment Assessment[string]
		wantReason string
	}{
		{
			name:       "not applicable",
			assessment: Assessment[string]{Applicable: false, Reason: "no dependency manifest"},
			wantReason: "no dependency manifest",
		},
		{
			name:       "already current",
			assessment: Assessment[string]{Applicable: true, NeedsChange: false, Reason: "already current"},
			wantReason: "already current",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newEngineFixture(t)
			results, err := Run(context.Background(), []Repository{fixture.repository},
				orchCovStageHandler{assessment: test.assessment}, fixture.options())
			if err != nil {
				t.Fatal(err)
			}
			if results[0].Status != "skipped" || results[0].Reason != test.wantReason {
				t.Fatalf("result = %+v", results[0])
			}
		})
	}
}

func TestOrchCovProcessRepositoryFailsTheStageThatFailed(t *testing.T) {
	t.Run("inspect", func(t *testing.T) {
		fixture := newEngineFixture(t)
		_, err := Run(context.Background(), []Repository{fixture.repository},
			orchCovStageHandler{inspectErr: errors.New("inspect exploded")}, fixture.options())
		if err == nil || !strings.Contains(err.Error(), "inspect exploded") {
			t.Fatalf("inspect error = %v", err)
		}
	})
	t.Run("apply", func(t *testing.T) {
		fixture := newEngineFixture(t)
		results, err := Run(context.Background(), []Repository{fixture.repository},
			orchCovStageHandler{assessment: Assessment[string]{Applicable: true, NeedsChange: true}, applyErr: errors.New("apply exploded")}, fixture.options())
		if err == nil || results[0].Status != "failed" || !strings.Contains(results[0].Reason, "apply exploded") {
			t.Fatalf("apply result = %+v, error = %v", results[0], err)
		}
	})
}

func TestOrchCovProcessRepositorySkipsAnOperationThatChangedNothing(t *testing.T) {
	fixture := newEngineFixture(t)
	options := fixture.options()
	options.Commit = true
	results, err := Run(context.Background(), []Repository{fixture.repository}, orchCovIdleHandler{}, options)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != "skipped" || results[0].Reason != "mutation produced no file change" {
		t.Fatalf("result = %+v", results[0])
	}
}

func TestOrchCovProcessRepositoryLeavesUncommittedChangesInTheWorktree(t *testing.T) {
	fixture := newEngineFixture(t)
	options := fixture.options()
	options.Verify = true
	results, err := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, options)
	if err != nil {
		t.Fatal(err)
	}
	result := results[0]
	if result.Status != "changed" || result.Reason != "verified changes remain in the local operation worktree" {
		t.Fatalf("result = %+v", result)
	}
}

// orchCovBrokenGoHandler applies a Go module that cannot build, so the local
// verification stage has something real to fail on.
type orchCovBrokenGoHandler struct {
	textHandler
	write func(worktree string) error
}

func (handler orchCovBrokenGoHandler) Apply(_ context.Context, worktree string, _ Repository) (string, error) {
	if err := handler.write(worktree); err != nil {
		return "", err
	}
	return "broken", nil
}

func TestOrchCovProcessRepositoryFailsLocalVerification(t *testing.T) {
	fixture := newEngineFixture(t)
	options := fixture.options()
	options.Commit = true
	options.Verify = true
	options.Checks = []quality.Check{quality.CheckBuild}
	handler := orchCovBrokenGoHandler{write: func(worktree string) error {
		writeEngineFile(t, filepath.Join(worktree, "go.mod"), "module example.test/broken\n\ngo 1.24\n")
		writeEngineFile(t, filepath.Join(worktree, "app.go"), "package broken\n\nfunc Broken() { this is not go }\n")
		return nil
	}}
	results, err := Run(context.Background(), []Repository{fixture.repository}, handler, options)
	if err == nil || results[0].Status != "failed" || !strings.Contains(results[0].Reason, "local verification failed") {
		t.Fatalf("result = %+v, error = %v", results[0], err)
	}
	if read := mustReadEngineFile(t, filepath.Join(results[0].WorktreeDir, "app.go")); !strings.Contains(read, "this is not go") {
		t.Fatalf("the failing candidate was rewritten: %q", read)
	}
}

func TestOrchCovProcessRepositoryPushesAVerifiedCommit(t *testing.T) {
	fixture := newEngineFixture(t)
	options := fixture.options()
	options.Push = true
	options.Verify = true
	results, err := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, options)
	if err != nil {
		t.Fatal(err)
	}
	result := results[0]
	if result.Status != "pushed" || !result.Pushed || result.Commit == "" {
		t.Fatalf("result = %+v", result)
	}
	if result.Reason != "verified commit pushed to the operation branch" {
		t.Fatalf("push reason = %q", result.Reason)
	}
	published := strings.TrimSpace(runEngineGit(t, fixture.canonical, "--git-dir="+fixture.repository.CloneURL, "rev-parse", "refs/heads/"+options.Branch))
	if published != result.Commit {
		t.Fatalf("published head = %q, want %q", published, result.Commit)
	}
}

func TestOrchCovOpenPullRequestReusesOrCreatesExactlyOne(t *testing.T) {
	const script = `#!/bin/sh
S="$ORCHCOV_GH_STATE"
if [ "$1" = pr ] && [ "$2" = list ]; then cat "$S/list"; exit 0; fi
if [ "$1" = pr ] && [ "$2" = create ]; then cat "$S/create"; exit "$(cat "$S/create-exit")"; fi
echo "unexpected gh args: $*" >&2
exit 30
`
	options := Options{Timeout: time.Minute}

	t.Run("reuses the open pull request", func(t *testing.T) {
		state := orchCovScriptState(t, script)
		state.answer(t, "list", "https://example.test/acme/app/pull/41\n")
		state.answer(t, "create", "")
		state.answer(t, "create-exit", "0")
		url, err := openPullRequest(context.Background(), t.TempDir(), "wb/deps", "main", "title", "body", options)
		if err != nil || url != "https://example.test/acme/app/pull/41" {
			t.Fatalf("openPullRequest = %q, %v", url, err)
		}
	})
	t.Run("creates one", func(t *testing.T) {
		state := orchCovScriptState(t, script)
		state.answer(t, "list", "\n")
		state.answer(t, "create", "Creating pull request...\nhttps://example.test/acme/app/pull/42\n")
		state.answer(t, "create-exit", "0")
		url, err := openPullRequest(context.Background(), t.TempDir(), "wb/deps", "main", "title", "body", options)
		if err != nil || url != "https://example.test/acme/app/pull/42" {
			t.Fatalf("openPullRequest = %q, %v", url, err)
		}
	})
	t.Run("creation failed", func(t *testing.T) {
		state := orchCovScriptState(t, script)
		state.answer(t, "list", "\n")
		state.answer(t, "create", "gh: validation failed\n")
		state.answer(t, "create-exit", "1")
		if _, err := openPullRequest(context.Background(), t.TempDir(), "wb/deps", "main", "title", "body", options); err == nil {
			t.Fatal("a failed gh pr create was accepted")
		}
	})
	t.Run("no url", func(t *testing.T) {
		state := orchCovScriptState(t, script)
		state.answer(t, "list", "\n")
		state.answer(t, "create", "\n")
		state.answer(t, "create-exit", "0")
		if _, err := openPullRequest(context.Background(), t.TempDir(), "wb/deps", "main", "title", "body", options); err == nil ||
			!strings.Contains(err.Error(), "no pull request URL") {
			t.Fatalf("missing URL error = %v", err)
		}
	})
}

func TestOrchCovChangedFilesAndBranchAheadReportGitFailures(t *testing.T) {
	dir := t.TempDir()
	options := Options{Timeout: time.Minute}
	if _, err := changedFiles(context.Background(), dir, options); err == nil {
		t.Fatal("changedFiles accepted a non-repository")
	}
	if _, err := branchAhead(context.Background(), dir, "origin/main", options); err == nil {
		t.Fatal("branchAhead accepted a non-repository")
	}
}

func TestOrchCovChangedFilesNamesEveryModifiedPath(t *testing.T) {
	dir := t.TempDir()
	runEngineGit(t, dir, "init", "-b", "main")
	writeEngineFile(t, filepath.Join(dir, "kept.txt"), "contents\n")
	writeEngineFile(t, filepath.Join(dir, "removed.txt"), "contents\n")
	runEngineGit(t, dir, "add", "-A")
	runEngineGit(t, dir, "-c", "user.name=WB Test", "-c", "user.email=wb@example.test", "commit", "-m", "initial")
	writeEngineFile(t, filepath.Join(dir, "kept.txt"), "changed\n")
	if err := os.Remove(filepath.Join(dir, "removed.txt")); err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, filepath.Join(dir, "added file.txt"), "added\n")

	files, err := changedFiles(context.Background(), dir, Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(files, ",")
	for _, want := range []string{"kept.txt", "removed.txt", "added file.txt"} {
		if !strings.Contains(got, want) {
			t.Fatalf("changed file inventory %v is missing %q", files, want)
		}
	}
}
