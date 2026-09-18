package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/sneat-dev/wb/internal/canonicalrescue"
	"github.com/sneat-dev/wb/internal/testenv"
)

func hkCovRepoWithHook(t *testing.T, repo, hook, script string) {
	t.Helper()
	dir := filepath.Join(repo, ".wb")
	mustMkdirAll(t, dir)
	mustWriteExecutable(t, filepath.Join(dir, hook+".sh"), script)
	hkCovRepoConfig(t, repo, "version: 1\nhooks:\n  "+hook+":\n    template: "+hook+".sh\n")
}

// hkCovFakeGH writes a fake gh into a temp directory and prepends it to PATH.
func hkCovFakeGH(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	mustWriteExecutable(t, filepath.Join(dir, "gh"), "#!/bin/sh\n"+body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestHkCovRunRejectsBadInvocations(t *testing.T) {
	isolateEnvironment(t)
	repo := initRepo(t)

	result, err := Run(RunOptions{RepoPath: repo, Hook: "Bad_Name"})
	if err == nil || !strings.Contains(err.Error(), "invalid hook name") || result.ExitCode != 2 {
		t.Fatalf("Run(invalid name) = %#v, %v", result, err)
	}

	if _, err := Run(RunOptions{RepoPath: t.TempDir(), Hook: "pre-commit"}); err == nil {
		t.Fatal("Run(non-repo) should fail")
	}

	isolateConfig(t)
	if _, err := Run(RunOptions{RepoPath: repo, Hook: "commit-msg"}); err == nil || !strings.Contains(err.Error(), "disabled or not configured") {
		t.Fatalf("Run(unconfigured hook) error = %v", err)
	}
}

func TestHkCovRunUsesProcessStreamsWhenUnset(t *testing.T) {
	testenv.Isolate(t)
	repo := initRepo(t)
	isolateConfig(t)
	hkCovRepoWithHook(t, repo, "pre-commit", "#!/bin/sh\nexit 0\n")

	result, err := Run(RunOptions{RepoPath: repo, Hook: "pre-commit", Stdin: strings.NewReader(""), Stderr: nil})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || len(result.Blocks) != 1 || result.Blocks[0].ID != "base/pre-commit" {
		t.Fatalf("Run = %#v", result)
	}
}

func TestHkCovRunReportsLayoutFailures(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	hkCovRepoWithHook(t, repo, "pre-commit", "#!/bin/sh\nexit 0\n")

	// An unusable projects root fails before any block runs.
	blocker := filepath.Join(t.TempDir(), "regular-file")
	mustWrite(t, blocker, "not a directory\n")
	t.Setenv("WB_PROJECTS_ROOT", filepath.Join(blocker, "projects"))
	if _, err := Run(RunOptions{RepoPath: repo, Hook: "pre-commit"}); err == nil || !strings.Contains(err.Error(), "resolve hook runtime layout") {
		t.Fatalf("Run with an unusable projects root error = %v", err)
	}

	// A runtime root occupied by a regular file cannot be prepared.
	isolateConfig(t)
	policy, err := LoadPolicy(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	layout, err := ResolveExecutionLayout(policy.RepoRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	mustMkdirAll(t, filepath.Dir(layout.Root))
	mustWrite(t, layout.Root, "occupied\n")
	if _, err := Run(RunOptions{RepoPath: repo, Hook: "pre-commit"}); err == nil || !strings.Contains(err.Error(), "prepare hook runtime layout") {
		t.Fatalf("Run with an occupied runtime root error = %v", err)
	}
}

func TestHkCovRunPrePushAttestation(t *testing.T) {
	testenv.Isolate(t)
	repo := initRepo(t)
	isolateConfig(t)

	// An incomplete attestation is a hard error.
	t.Setenv(canonicalrescue.PushBranchEnv, "rescue/incomplete")
	t.Setenv(canonicalrescue.PushCommitEnv, "")
	result, err := Run(RunOptions{RepoPath: repo, Hook: "pre-push", Stdin: strings.NewReader("")})
	if err == nil || !strings.Contains(err.Error(), "incomplete canonical rescue push attestation") || result.ExitCode != 1 {
		t.Fatalf("Run(incomplete attestation) = %#v, %v", result, err)
	}

	// A complete but unverifiable attestation fails the rescue block.
	t.Setenv(canonicalrescue.PushCommitEnv, "0123456789012345678901234567890123456789")
	result, err = Run(RunOptions{RepoPath: repo, Hook: "pre-push", Stdin: strings.NewReader("")})
	if err == nil || !strings.Contains(err.Error(), "verify canonical rescue push") {
		t.Fatalf("Run(unverifiable attestation) error = %v", err)
	}
	if result.ExitCode != 1 || len(result.Blocks) != 1 || result.Blocks[0].ID != "worktree/rescue-pre-push" {
		t.Fatalf("Run(unverifiable attestation) = %#v", result)
	}
}

func TestHkCovRunReportsUnreadablePrePushInput(t *testing.T) {
	testenv.Isolate(t)
	repo := initRepo(t)
	isolateConfig(t)
	failure := errors.New("simulated stdin failure")
	_, err := Run(RunOptions{RepoPath: repo, Hook: "pre-push", Stdin: iotest.ErrReader(failure)})
	if err == nil || !strings.Contains(err.Error(), "read pre-push input") {
		t.Fatalf("Run(unreadable stdin) error = %v", err)
	}
}

// TestHkCovRunAnnotatesPushEventsWithRefAndTier asserts the recorded event
// names the pushed ref and the tier the hook acted on, through the real Run
// path rather than a direct annotatePushEvent call.
func TestHkCovRunAnnotatesPushEventsWithRefAndTier(t *testing.T) {
	testenv.Isolate(t)
	repo := initRepo(t)
	isolateConfig(t)
	metricsPath := filepath.Join(os.Getenv("XDG_STATE_HOME"), "wb", "hook-events.jsonl")
	sha := strings.Repeat("a", 40)
	zero := strings.Repeat("0", 40)

	tagRef := "refs/tags/v1.0.0"
	if _, err := Run(RunOptions{
		RepoPath: repo, Hook: "pre-push",
		Stdin: strings.NewReader("refs/heads/main " + sha + " " + tagRef + " " + zero + "\n"),
	}); err != nil {
		t.Fatal(err)
	}
	checkpointRef := CheckpointRefPrefix + "task/1"
	if _, err := Run(RunOptions{
		RepoPath: repo, Hook: "pre-push",
		Stdin: strings.NewReader("refs/heads/main " + sha + " " + checkpointRef + " " + zero + "\n"),
	}); err != nil {
		t.Fatal(err)
	}

	events, err := ReadEvents(metricsPath)
	if err != nil {
		t.Fatal(err)
	}
	var pushEvents []Event
	for _, event := range events {
		if event.Action == "push-attempt" {
			pushEvents = append(pushEvents, event)
		}
	}
	if len(pushEvents) != 2 {
		t.Fatalf("push events = %#v, want two", pushEvents)
	}
	if pushEvents[0].Tier == nil || *pushEvents[0].Tier != int(TierPublication) || pushEvents[0].Ref != tagRef {
		t.Fatalf("tag push event = %#v, want tier %d and ref %s", pushEvents[0], TierPublication, tagRef)
	}
	if pushEvents[1].Tier == nil || *pushEvents[1].Tier != int(TierSkip) || pushEvents[1].Ref != checkpointRef {
		t.Fatalf("checkpoint push event = %#v, want tier %d and ref %s", pushEvents[1], TierSkip, checkpointRef)
	}
}

func TestHkCovAnnotatePushEventEarlyReturns(t *testing.T) {
	isolateConfig(t)
	repo := initRepo(t)
	policy, err := LoadPolicy(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	event := Event{}
	annotatePushEvent(&event, RunOptions{Hook: "pre-commit"}, policy, []byte("refs/heads/main a b c\n"))
	if event.Tier != nil || event.Ref != "" {
		t.Fatalf("annotatePushEvent(non-push) mutated the event: %#v", event)
	}
	annotatePushEvent(&event, RunOptions{Hook: "pre-push"}, policy, nil)
	if event.Tier != nil || event.Ref != "" {
		t.Fatalf("annotatePushEvent(no refs) mutated the event: %#v", event)
	}
	annotatePushEvent(&event, RunOptions{Hook: "pre-push"}, policy, []byte("only-one-field\n"))
	if event.Tier != nil || event.Ref != "" {
		t.Fatalf("annotatePushEvent(malformed refs) mutated the event: %#v", event)
	}
}

func TestHkCovRunTemplateErrorBranches(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	policy, err := LoadPolicy(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	layout := ExecutionLayout{Root: t.TempDir()}

	exitCode, err := runTemplate(policy, HookBlock{
		ID: "base/pre-commit", Profile: "base",
		Hook: ResolvedHook{Name: "pre-commit", Template: "builtin:not-a-template", Builtin: true},
	}, RunOptions{Hook: "pre-commit", Args: nil}, eventContext{}, layout)
	if err == nil || !strings.Contains(err.Error(), "unknown built-in template") || exitCode != 2 {
		t.Fatalf("runTemplate(unknown builtin) = %d, %v", exitCode, err)
	}

	// A missing temporary directory makes creating the generated script fail.
	missingTmp := filepath.Join(t.TempDir(), "missing")
	t.Setenv("TMPDIR", missingTmp)
	exitCode, err = runTemplate(policy, HookBlock{
		Hook: ResolvedHook{Name: "pre-commit", Template: BuiltinPreCommit, Builtin: true},
	}, RunOptions{Hook: "pre-commit"}, eventContext{}, layout)
	if err == nil || exitCode != 2 {
		t.Fatalf("runTemplate(missing TMPDIR) = %d, %v", exitCode, err)
	}
	t.Setenv("TMPDIR", t.TempDir())

	// A script that cannot even start is not an exit-status failure.
	broken := policy
	broken.RepoRoot = filepath.Join(t.TempDir(), "missing-repo")
	exitCode, err = runTemplate(broken, HookBlock{
		ID: "base", Profile: "base",
		Hook: ResolvedHook{Name: "pre-commit", Template: BuiltinPreCommit, Builtin: true},
	}, RunOptions{Hook: "pre-commit"}, eventContext{}, layout)
	if err == nil || exitCode != 2 || !strings.Contains(err.Error(), "run base template") {
		t.Fatalf("runTemplate(unstartable) = %d, %v", exitCode, err)
	}
}

// TestHkCovRunVerifiesARealCanonicalRescueAttestation builds the exact state a
// canonical rescue leaves behind — a dirty canonical clone plus the capture
// branch and commit — and proves the pre-push hook accepts it and reports the
// rescue block as a success.
func TestHkCovRunVerifiesARealCanonicalRescueAttestation(t *testing.T) {
	testenv.Isolate(t)
	projectsRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(projectsRoot, "acme", "widget")
	mustMkdirAll(t, repo)
	git(t, repo, "init", "-b", "main")
	git(t, repo, "config", "user.name", "WB Tests")
	git(t, repo, "config", "user.email", "wb-tests@example.invalid")
	mustWrite(t, filepath.Join(repo, "README.md"), "test\n")
	git(t, repo, "add", "README.md")
	git(t, repo, "commit", "-m", "initial")
	// Uncommitted work is what a rescue exists to preserve.
	mustWrite(t, filepath.Join(repo, "in-flight.txt"), "uncommitted\n")
	isolateConfig(t)

	report, err := canonicalrescue.Inspect(context.Background(), repo, canonicalrescue.Options{
		ProjectsRoot: projectsRoot, Branch: "rescue/hk-cov",
	})
	if err != nil {
		t.Fatal(err)
	}
	captured, err := canonicalrescue.Capture(context.Background(), report)
	if err != nil {
		t.Fatal(err)
	}
	if captured.RescueCommit == "" {
		t.Fatal("Capture did not record a rescue commit")
	}
	t.Setenv(canonicalrescue.PushBranchEnv, captured.RescueBranch)
	t.Setenv(canonicalrescue.PushCommitEnv, captured.RescueCommit)

	ref := "refs/heads/" + captured.RescueBranch
	line := ref + " " + captured.RescueCommit + " " + ref + " " + captured.RescueCommit + "\n"
	result, err := Run(RunOptions{
		RepoPath: repo, Hook: "pre-push", ProjectsRoot: projectsRoot,
		Stdin: strings.NewReader(line),
	})
	if err != nil {
		t.Fatalf("Run(valid attestation) error = %v", err)
	}
	if result.ExitCode != 0 || len(result.Blocks) != 1 || result.Blocks[0].ID != "worktree/rescue-pre-push" || result.Blocks[0].ExitCode != 0 {
		t.Fatalf("Run(valid attestation) = %#v", result)
	}
}

func TestHkCovParseRefUpdatesScannerError(t *testing.T) {
	t.Parallel()
	failure := errors.New("ref stream failed")
	if _, err := ParseRefUpdates(iotest.ErrReader(failure)); err == nil || !strings.Contains(err.Error(), "read pushed-ref list") {
		t.Fatalf("ParseRefUpdates(erroring reader) error = %v", err)
	}
}

func TestHkCovClassifyPushTierUnrecognizedNamespace(t *testing.T) {
	t.Parallel()
	updates := []RefUpdate{{
		LocalRef: "refs/notes/commits", LocalSHA: strings.Repeat("a", 40),
		RemoteRef: "refs/notes/commits", RemoteSHA: zeroOID40,
	}}
	classification := ClassifyPushTier(updates, "main", nil)
	if classification.Tier != TierPublication || !strings.Contains(classification.Reason, "unrecognized ref namespace") {
		t.Fatalf("classification = %#v, want the full tier for an unknown namespace", classification)
	}
	if !classification.IsPublication() || !classification.RunLint() || classification.ExitCode() != 2 {
		t.Fatalf("classification helpers = %#v", classification)
	}
}

func TestHkCovClassifyPendingPushRejectsMalformedInput(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)
	if _, err := ClassifyPendingPush(strings.NewReader("only two fields\n"), repo); err == nil || !strings.Contains(err.Error(), "malformed pushed-ref line") {
		t.Fatalf("ClassifyPendingPush(malformed) error = %v", err)
	}

	sha := strings.Repeat("a", 40)
	zero := strings.Repeat("0", 40)
	classification, err := ClassifyPendingPush(strings.NewReader("refs/heads/main "+sha+" refs/tags/v2 "+zero+"\n"), repo)
	if err != nil {
		t.Fatal(err)
	}
	if classification.Tier != TierPublication || !strings.Contains(classification.Reason, "is a tag") {
		t.Fatalf("classification = %#v, want a publication push for a tag", classification)
	}
}

func TestHkCovDetectDefaultBranchLocalState(t *testing.T) {
	repo := initRepo(t)
	t.Setenv(DefaultBranchEnv, "")

	if got := detectDefaultBranch(repo); got != "" {
		t.Fatalf("detectDefaultBranch(no remote refs) = %q, want empty", got)
	}

	// The recorded origin/HEAD symref wins.
	git(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	git(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	if got := detectDefaultBranch(repo); got != "main" {
		t.Fatalf("detectDefaultBranch(origin/HEAD) = %q, want main", got)
	}

	// Without origin/HEAD the conventional remote-tracking ref is used.
	git(t, repo, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	git(t, repo, "update-ref", "-d", "refs/remotes/origin/main")
	git(t, repo, "update-ref", "refs/remotes/origin/master", "HEAD")
	if got := detectDefaultBranch(repo); got != "master" {
		t.Fatalf("detectDefaultBranch(fallback) = %q, want master", got)
	}
}

func TestHkCovDefaultPRStatusCachePath(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	if got, want := defaultPRStatusCachePath(), filepath.Join(state, "wb", "pr-status-cache.json"); got != want {
		t.Fatalf("defaultPRStatusCachePath() = %q, want %q", got, want)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	if got, want := defaultPRStatusCachePath(), filepath.Join(".wb", "pr-status-cache.json"); got != want {
		t.Fatalf("defaultPRStatusCachePath(no home) = %q, want %q", got, want)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := defaultPRStatusCachePath(), filepath.Join(home, ".local", "state", "wb", "pr-status-cache.json"); got != want {
		t.Fatalf("defaultPRStatusCachePath(home) = %q, want %q", got, want)
	}
}

func TestHkCovCachedGHPRLookupDefaultsAndBlankInputs(t *testing.T) {
	t.Parallel()
	lookup := &CachedGHPRLookup{}
	if lookup.ttl() != defaultPRStatusCacheTTL {
		t.Fatalf("ttl() = %v, want %v", lookup.ttl(), defaultPRStatusCacheTTL)
	}
	if lookup.timeout() != defaultPRStatusLookupTimeout {
		t.Fatalf("timeout() = %v, want %v", lookup.timeout(), defaultPRStatusLookupTimeout)
	}
	lookup.TTL = time.Minute
	lookup.Timeout = time.Second
	if lookup.ttl() != time.Minute || lookup.timeout() != time.Second {
		t.Fatalf("configured ttl/timeout = %v/%v", lookup.ttl(), lookup.timeout())
	}

	if open, known := lookup.OpenPullRequest("feature"); open || known {
		t.Fatalf("OpenPullRequest(blank slug) = %v, %v; want false, false", open, known)
	}
	lookup.RepoSlug = "acme/widget"
	if open, known := lookup.OpenPullRequest("   "); open || known {
		t.Fatalf("OpenPullRequest(blank branch) = %v, %v; want false, false", open, known)
	}
}

func TestHkCovCachedGHPRLookupUsesTheDefaultRunner(t *testing.T) {
	repo := initRepo(t)
	isolateEnvironment(t)
	hkCovFakeGH(t, "printf '[{\"number\": 7}]'\n")
	cachePath := filepath.Join(t.TempDir(), "pr-status-cache.json")
	lookup := &CachedGHPRLookup{
		RepoRoot: repo, RepoSlug: "acme/widget", CachePath: cachePath,
		TTL: time.Minute, Timeout: 5 * time.Second,
	}
	open, known := lookup.OpenPullRequest("feature")
	if !open || !known {
		t.Fatalf("OpenPullRequest = %v, %v; want a known open pull request", open, known)
	}
	cache := loadPRStatusCache(cachePath)
	if entry, found := cache["acme/widget#feature"]; !found || !entry.Open {
		t.Fatalf("cache = %#v, want a positive entry", cache)
	}
}

func TestHkCovCachedGHPRLookupRejectsBadJSON(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)
	lookup := &CachedGHPRLookup{
		RepoRoot: repo, RepoSlug: "acme/widget",
		CachePath: filepath.Join(t.TempDir(), "cache.json"),
		TTL:       time.Minute, Timeout: time.Second,
		RunGH: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("this is not json"), nil
		},
	}
	if open, known := lookup.OpenPullRequest("feature"); open || known {
		t.Fatalf("OpenPullRequest(bad JSON) = %v, %v; want false, false", open, known)
	}
}

func TestHkCovRunGHExecutesTheFakeBinary(t *testing.T) {
	isolateEnvironment(t)
	hkCovFakeGH(t, "printf 'gh-ok'\n")
	output, err := runGH(context.Background(), t.TempDir(), "api", "repos/acme/widget/pulls")
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "gh-ok" {
		t.Fatalf("runGH output = %q, want gh-ok", output)
	}
}

func TestHkCovPRStatusCacheLoadAndSave(t *testing.T) {
	t.Parallel()
	if cache := loadPRStatusCache("   "); len(cache) != 0 {
		t.Fatalf("loadPRStatusCache(blank) = %#v, want empty", cache)
	}
	path := filepath.Join(t.TempDir(), "cache.json")
	mustWrite(t, path, "not json at all")
	if cache := loadPRStatusCache(path); len(cache) != 0 {
		t.Fatalf("loadPRStatusCache(corrupt) = %#v, want empty", cache)
	}
	mustWrite(t, path, "null")
	if cache := loadPRStatusCache(path); cache == nil || len(cache) != 0 {
		t.Fatalf("loadPRStatusCache(null) = %#v, want an empty non-nil map", cache)
	}

	savePRStatusCache("  ", map[string]prStatusCacheEntry{"acme/widget#feature": {Open: true}})

	blocker := filepath.Join(t.TempDir(), "regular-file")
	mustWrite(t, blocker, "not a directory\n")
	savePRStatusCache(filepath.Join(blocker, "cache.json"), map[string]prStatusCacheEntry{"acme/widget#feature": {Open: true}})

	saved := filepath.Join(t.TempDir(), "nested", "cache.json")
	savePRStatusCache(saved, map[string]prStatusCacheEntry{"acme/widget#feature": {Open: true, CheckedAt: time.Now().UTC()}})
	cache := loadPRStatusCache(saved)
	if entry, found := cache["acme/widget#feature"]; !found || !entry.Open {
		t.Fatalf("saved cache = %#v, want the positive entry round-tripped", cache)
	}
	raw, err := os.ReadFile(saved)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]prStatusCacheEntry
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("saved cache is not JSON: %v", err)
	}

	if os.Geteuid() != 0 {
		locked := t.TempDir()
		if err := os.Chmod(locked, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
		savePRStatusCache(filepath.Join(locked, "cache.json"), map[string]prStatusCacheEntry{"acme/widget#feature": {Open: true}})
		if _, statErr := os.Stat(filepath.Join(locked, "cache.json")); !os.IsNotExist(statErr) {
			t.Fatalf("a read-only directory should not receive a cache file: %v", statErr)
		}
	}
}

func TestHkCovPRHeadQualifier(t *testing.T) {
	t.Parallel()
	if got := prHeadQualifier("acme/widget", "feature"); got != "acme:feature" {
		t.Fatalf("prHeadQualifier = %q, want acme:feature", got)
	}
	if got := prHeadQualifier("widget", "feature"); got != "widget:feature" {
		t.Fatalf("prHeadQualifier(no owner) = %q, want widget:feature", got)
	}
}
