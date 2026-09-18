package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWtLogCovRelocateValidation(t *testing.T) {
	t.Parallel()
	if _, err := Relocate(context.Background(), RelocateOptions{ProjectsRoot: t.TempDir(), Task: "../escape", To: "local"}); err == nil {
		t.Fatal("unsafe task was accepted")
	}
	if _, err := Relocate(context.Background(), RelocateOptions{ProjectsRoot: t.TempDir(), Task: "task", To: "moon"}); err == nil {
		t.Fatal("unsupported destination was accepted")
	}
	if _, err := Relocate(context.Background(), RelocateOptions{ProjectsRoot: t.TempDir(), Task: "missing-task", To: "local"}); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("missing task error = %v", err)
	}
}

func TestWtLogCovFindRelocationEntry(t *testing.T) {
	t.Parallel()
	entries := []ListResult{{WorktreeDir: "/a/b"}, {WorktreeDir: "/c/d"}}
	if entry, found := findRelocationEntry(entries, "/c/d"); !found || entry.WorktreeDir != "/c/d" {
		t.Fatalf("entry = %#v/%t", entry, found)
	}
	if entry, found := findRelocationEntry(entries, "/c/d/../d"); !found || entry.WorktreeDir != "/c/d" {
		t.Fatalf("cleaned entry = %#v/%t", entry, found)
	}
	if _, found := findRelocationEntry(entries, "/missing"); found {
		t.Fatal("missing entry was reported found")
	}
}

func TestWtLogCovRelocationEligibility(t *testing.T) {
	t.Parallel()
	if eligible, reason := relocationEligibility(ListResult{Clean: true}); !eligible || reason != "" {
		t.Fatalf("clean entry = %t/%q", eligible, reason)
	}
	if eligible, reason := relocationEligibility(ListResult{Clean: true, External: true}); eligible || !strings.Contains(reason, "adopted external worktree") {
		t.Fatalf("external entry = %t/%q", eligible, reason)
	}
	if eligible, reason := relocationEligibility(ListResult{Clean: true, Locked: true, Task: "t", LockOwner: LockOwnerLive, LockOwnerPID: 7}); eligible || !strings.Contains(reason, "still running") {
		t.Fatalf("locked entry = %t/%q", eligible, reason)
	}
	if eligible, reason := relocationEligibility(ListResult{Clean: true, OwnerState: "active"}); eligible || !strings.Contains(reason, "active owner") {
		t.Fatalf("owned entry = %t/%q", eligible, reason)
	}
	if eligible, reason := relocationEligibility(ListResult{Clean: false}); eligible || reason != "worktree has local changes" {
		t.Fatalf("dirty entry = %t/%q", eligible, reason)
	}
}

// wtLogCovRelocationClaim returns a claim and one valid relocation record pair
// for direct journal tests.
func wtLogCovRelocationClaim(t *testing.T) (workLogClaim, string, string) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source")
	destination := filepath.Join(t.TempDir(), "destination")
	claim := workLogClaim{Version: 1, EffortID: "effort", RunID: "run", ClaimID: strings.Repeat("a", 64),
		Task: "task", Repository: "acme/app", Branch: "wb/x", Worktree: source}
	return claim, source, destination
}

func wtLogCovWriteRelocationFile(t *testing.T, run *os.File, name string, record workLogRelocationIntent) {
	t.Helper()
	directory, err := openPrivateChild(run, "relocations", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	if err := writeJSONAtomicAt(directory, name, record, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWtLogCovOpenRelocationJournalFailures(t *testing.T) {
	t.Parallel()
	claim, source, destination := wtLogCovRelocationClaim(t)
	base := workLogRelocationIntent{Version: 1, Type: workLogRelocationIntentType, OperationID: "op-1",
		ClaimID: claim.ClaimID, Task: claim.Task, Repository: claim.Repository, Branch: claim.Branch,
		HeadSHA: "head", Source: source, Destination: destination, To: "local", At: time.Now().UTC()}

	t.Run("filename mismatch", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		run, runPath, err := openWorkLogRun(home, claim.EffortID, claim.RunID, true)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = run.Close() }()
		wtLogCovWriteRelocationFile(t, run, relocationIntentName(claim.ClaimID, "op-other"), base)
		if _, err := openRelocationJournal(run, runPath, claim); err == nil {
			t.Fatal("misnamed relocation intent was accepted")
		}
	})

	t.Run("invalid record", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		run, runPath, err := openWorkLogRun(home, claim.EffortID, claim.RunID, true)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = run.Close() }()
		invalid := base
		invalid.Version = 2
		wtLogCovWriteRelocationFile(t, run, relocationIntentName(claim.ClaimID, base.OperationID), invalid)
		if _, err := openRelocationJournal(run, runPath, claim); err == nil {
			t.Fatal("invalid relocation record was accepted")
		}
	})

	t.Run("orphan receipt", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		run, runPath, err := openWorkLogRun(home, claim.EffortID, claim.RunID, true)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = run.Close() }()
		receipt := base
		receipt.Type = workLogRelocationType
		wtLogCovWriteRelocationFile(t, run, relocationReceiptName(claim.ClaimID, receipt.OperationID), receipt)
		if _, err := openRelocationJournal(run, runPath, claim); err == nil {
			t.Fatal("receipt without an intent was accepted")
		}
	})

	t.Run("unrelated and invalid json skipped", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		run, runPath, err := openWorkLogRun(home, claim.EffortID, claim.RunID, true)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = run.Close() }()
		directory, err := openPrivateChild(run, "relocations", true)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeBytesAtomicAt(directory, "unrelated.json", []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = directory.Close()
		journal, err := openRelocationJournal(run, runPath, claim)
		if err != nil {
			t.Fatal(err)
		}
		if len(journal.intents) != 0 || len(journal.receipts) != 0 {
			t.Fatalf("unrelated files entered the journal: %#v", journal)
		}
	})
}

func TestWtLogCovRelocationResolutionBranches(t *testing.T) {
	t.Parallel()
	claim, source, destination := wtLogCovRelocationClaim(t)
	home := t.TempDir()
	run, _, err := openWorkLogRun(home, claim.EffortID, claim.RunID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Close() }()

	// No receipts yet: no resolution.
	resolution, err := latestRelocationResolution(home, claim, destination)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.receipt != nil || resolution.worktree != filepath.Clean(claim.Worktree) {
		t.Fatalf("empty resolution = %#v", resolution)
	}

	// A worktree relocation receipt does not change repository identity.
	intent := workLogRelocationIntent{Version: 1, Type: workLogRelocationIntentType, OperationID: "op-worktree",
		ClaimID: claim.ClaimID, Task: claim.Task, Repository: claim.Repository, Branch: claim.Branch,
		HeadSHA: "head", Source: source, Destination: destination, To: "local", At: time.Now().UTC()}
	wtLogCovWriteRelocationFile(t, run, relocationIntentName(claim.ClaimID, intent.OperationID), intent)
	receipt := intent
	receipt.Type = workLogRelocationType
	wtLogCovWriteRelocationFile(t, run, relocationReceiptName(claim.ClaimID, receipt.OperationID), receipt)
	resolution, err = latestRelocationResolution(home, claim, destination)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.receipt == nil || resolution.repository != "acme/app" || resolution.worktree != filepath.Clean(destination) {
		t.Fatalf("worktree resolution = %#v", resolution)
	}
	pending, _, err := pendingRelocationIntent(home, claim, destination, claim.Branch, "head")
	if err != nil || pending != nil {
		t.Fatalf("completed intent reported pending: %#v/%v", pending, err)
	}

	// A repository relocation receipt changes repository identity.
	repositoryHome := t.TempDir()
	repositoryRun, repositoryPath, err := openWorkLogRun(repositoryHome, claim.EffortID, claim.RunID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repositoryRun.Close() }()
	repositoryIntent := intent
	repositoryIntent.OperationID = "op-repository"
	repositoryIntent.To = "repository"
	repositoryIntent.SourceRepository, repositoryIntent.DestinationRepository = "acme/app", "acme/dest"
	repositoryIntent.RemoteURL = "https://github.com/acme/dest.git"
	wtLogCovWriteRelocationFile(t, repositoryRun, relocationIntentName(claim.ClaimID, repositoryIntent.OperationID), repositoryIntent)
	repositoryReceipt := repositoryIntent
	repositoryReceipt.Type = workLogRelocationType
	repositoryReceipt.At = time.Now().UTC().Add(time.Second)
	wtLogCovWriteRelocationFile(t, repositoryRun, relocationReceiptName(claim.ClaimID, repositoryReceipt.OperationID), repositoryReceipt)
	resolved, err := latestRelocationResolution(repositoryHome, claim, destination)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.repository != "acme/dest" || resolved.worktree != filepath.Clean(destination) {
		t.Fatalf("repository resolution = %#v", resolved)
	}
	pendingIntent, path, err := pendingRelocationIntent(repositoryHome, claim, destination, claim.Branch, "head")
	if err != nil || pendingIntent != nil || path != "" {
		t.Fatalf("completed repository intent reported pending: %#v/%q/%v", pendingIntent, path, err)
	}

	// A receipt whose source does not continue the immutable claim path is refused.
	brokenHome := t.TempDir()
	brokenRun, brokenPath, err := openWorkLogRun(brokenHome, claim.EffortID, claim.RunID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = brokenRun.Close() }()
	brokenIntent := intent
	brokenIntent.OperationID = "op-broken"
	brokenIntent.Source = filepath.Join(t.TempDir(), "elsewhere")
	brokenIntent.Destination = destination
	wtLogCovWriteRelocationFile(t, brokenRun, relocationIntentName(claim.ClaimID, brokenIntent.OperationID), brokenIntent)
	brokenReceipt := brokenIntent
	brokenReceipt.Type = workLogRelocationType
	wtLogCovWriteRelocationFile(t, brokenRun, relocationReceiptName(claim.ClaimID, brokenReceipt.OperationID), brokenReceipt)
	if _, err := latestRelocationResolution(brokenHome, claim, destination); err == nil {
		t.Fatal("receipt continuing a different path was accepted")
	}
	_ = repositoryPath
	_ = brokenPath
}

func TestWtLogCovCorroborateRepositoryRelocation(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", "https://github.com/acme/dest.git")
	if err := corroborateRepositoryRelocation(context.Background(), fixture.canonical, "acme/dest"); err != nil {
		t.Fatalf("matching origin rejected: %v", err)
	}
	if err := corroborateRepositoryRelocation(context.Background(), fixture.canonical, "acme/other"); err == nil {
		t.Fatal("mismatched origin accepted")
	}
	if err := corroborateRepositoryRelocation(context.Background(), t.TempDir(), "acme/dest"); err == nil {
		t.Fatal("non-repository path accepted")
	}
}

func TestWtLogCovRelocateRepositoryValidation(t *testing.T) {
	t.Parallel()
	cases := map[string]RepositoryRelocateOptions{
		"identical":    {SourceRepository: "acme/app", DestinationRepository: "acme/app", RemoteURL: "https://github.com/acme/app.git", DefaultBranch: "main"},
		"bad source":   {SourceRepository: "nope", DestinationRepository: "acme/dest", RemoteURL: "https://github.com/acme/dest.git", DefaultBranch: "main"},
		"bad dest":     {SourceRepository: "acme/app", DestinationRepository: "nope", RemoteURL: "https://github.com/acme/dest.git", DefaultBranch: "main"},
		"remote drift": {SourceRepository: "acme/app", DestinationRepository: "acme/dest", RemoteURL: "https://github.com/acme/other.git", DefaultBranch: "main"},
		"bad remote":   {SourceRepository: "acme/app", DestinationRepository: "acme/dest", RemoteURL: "not a url", DefaultBranch: "main"},
		"bad branch":   {SourceRepository: "acme/app", DestinationRepository: "acme/dest", RemoteURL: "https://github.com/acme/dest.git", DefaultBranch: "bad branch name"},
	}
	for name, options := range cases {
		options.ProjectsRoot = t.TempDir()
		if _, err := RelocateRepository(context.Background(), options); err == nil {
			t.Errorf("invalid repository relocation %q was accepted", name)
		}
	}
	options := RepositoryRelocateOptions{ProjectsRoot: t.TempDir(), SourceRepository: "acme/app",
		DestinationRepository: "acme/dest", RemoteURL: "https://github.com/acme/dest.git", DefaultBranch: "main"}
	if _, err := RelocateRepository(context.Background(), options); err == nil || !strings.Contains(err.Error(), "open source canonical repository") {
		t.Fatalf("missing source error = %v", err)
	}
}

func TestWtLogCovExactOriginURLsAndWorktreeRegistry(t *testing.T) {
	fixture := newGitFixture(t)
	for _, push := range []bool{false, true} {
		urls, err := exactOriginURLs(context.Background(), fixture.canonical, push)
		if err != nil || len(urls) != 1 || urls[0] != fixture.remote {
			t.Fatalf("origin push=%t urls=%v err=%v", push, urls, err)
		}
	}
	if _, err := exactOriginURLs(context.Background(), t.TempDir(), false); err == nil {
		t.Fatal("origin URL of a non-repository was read")
	}

	entries, err := repositoryRelocateWorktrees(context.Background(), fixture.canonical, fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].source != fixture.canonical || entries[0].head == "" {
		t.Fatalf("worktree registry = %#v", entries)
	}
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "relocate-cov", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err = repositoryRelocateWorktrees(context.Background(), fixture.canonical, filepath.Join(t.TempDir(), "moved"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].source != fixture.canonical {
		t.Fatalf("worktree registry with a linked checkout = %#v", entries)
	}
	mapped := false
	for _, entry := range entries {
		if entry.source == created[0].WorktreeDir {
			mapped = entry.destination != entry.source && strings.HasSuffix(entry.destination, filepath.Base(entry.source))
		}
	}
	if !mapped {
		t.Fatalf("nested worktree was not remapped: %#v", entries)
	}
	if _, err := repositoryRelocateWorktrees(context.Background(), t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("worktree registry of a non-repository was read")
	}
}

func TestWtLogCovRemoteDefaultHead(t *testing.T) {
	fixture := newGitFixture(t)
	head, err := remoteDefaultHead(context.Background(), fixture.canonical, fixture.remote, "main")
	if err != nil {
		t.Fatal(err)
	}
	if head != gitTestOutput(t, fixture.canonical, "rev-parse", "origin/main") {
		t.Fatalf("remote default head = %q", head)
	}
	if _, err := remoteDefaultHead(context.Background(), fixture.canonical, fixture.remote, "trunk"); err == nil {
		t.Fatal("wrong default branch was accepted")
	}
	if _, err := remoteDefaultHead(context.Background(), fixture.canonical, filepath.Join(t.TempDir(), "missing.git"), "main"); err == nil {
		t.Fatal("missing remote was accepted")
	}
}

func TestWtLogCovDisposableDestinationReason(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	remote := filepath.Join(root, "remotes", "newco", "renamed.git")
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, root, "init", "--bare", "--initial-branch=main", remote)
	writer := filepath.Join(root, "writer")
	gitTest(t, root, "clone", remote, writer)
	configureGitUser(t, writer)
	if err := os.WriteFile(filepath.Join(writer, "README.md"), []byte("# dest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, writer, "add", "README.md")
	gitTest(t, writer, "commit", "-m", "initial")
	gitTest(t, writer, "push", "origin", "main")

	destination := filepath.Join(root, "projects", "newco", "renamed")
	gitTest(t, root, "clone", remote, destination)
	expectedHead := gitTestOutput(t, destination, "rev-parse", "HEAD")
	options := RepositoryRelocateOptions{DestinationRepository: "newco/renamed", DefaultBranch: "main"}
	if reason := disposableDestinationReason(context.Background(), destination, options, expectedHead); reason != "" {
		t.Fatalf("clean disposable destination rejected: %q", reason)
	}
	if reason := disposableDestinationReason(context.Background(), destination, options, strings.Repeat("a", 40)); reason == "" {
		t.Fatal("destination with the wrong HEAD was accepted")
	}
	if reason := disposableDestinationReason(context.Background(), filepath.Join(root, "missing"), options, expectedHead); reason == "" {
		t.Fatal("missing destination was accepted")
	}
	if err := os.WriteFile(filepath.Join(destination, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if reason := disposableDestinationReason(context.Background(), destination, options, expectedHead); reason == "" {
		t.Fatal("dirty destination was accepted")
	}
	if err := os.Remove(filepath.Join(destination, "dirty.txt")); err != nil {
		t.Fatal(err)
	}
	otherRepo := RepositoryRelocateOptions{DestinationRepository: "newco/other", DefaultBranch: "main"}
	if reason := disposableDestinationReason(context.Background(), destination, otherRepo, expectedHead); reason == "" {
		t.Fatal("destination with a different repository identity was accepted")
	}
	if reason := disposableDestinationReason(context.Background(), destination, RepositoryRelocateOptions{DestinationRepository: "newco/renamed", DefaultBranch: "trunk"}, expectedHead); reason == "" {
		t.Fatal("destination on a different branch was accepted")
	}
}

func TestWtLogCovFinalizeRepositoryTransferWorkLogsRefusals(t *testing.T) {
	fixture := newGitFixture(t)
	if _, err := FinalizeRepositoryTransferWorkLogs(context.Background(), RepositoryRelocateOptions{
		ProjectsRoot: fixture.projectsRoot, DestinationRepository: "nope",
	}); err == nil {
		t.Fatal("invalid destination repository was accepted")
	}
	// The source canonical has no Work Log projection, so there is nothing to
	// complete and no error.
	receipts, err := FinalizeRepositoryTransferWorkLogs(context.Background(), RepositoryRelocateOptions{
		ProjectsRoot: fixture.projectsRoot, DestinationRepository: "acme/app",
	})
	if err != nil || len(receipts) != 0 {
		t.Fatalf("source canonical finalization = %v/%v", receipts, err)
	}
	// A destination path that is not a Git repository cannot be inventoried.
	if err := os.MkdirAll(filepath.Join(fixture.projectsRoot, "newco", "dest"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := FinalizeRepositoryTransferWorkLogs(context.Background(), RepositoryRelocateOptions{
		ProjectsRoot: fixture.projectsRoot, DestinationRepository: "newco/dest",
	}); err == nil {
		t.Fatal("non-repository destination was accepted")
	}
}
