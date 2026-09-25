package archiveprune

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/testenv"
)

func TestRPCovCleanReportsAnUnscannableProjectsRoot(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Clean(context.Background(), Options{ProjectsRoot: blocker}); err == nil {
		t.Fatal("Clean accepted a projects root it cannot scan")
	}
}

func TestRPCovCleanReportsProgressForEveryRepository(t *testing.T) {
	isolateWBHome(t)
	f := newFixture(t, "acme", "widgets")
	f.archived()

	var progress bytes.Buffer
	outcome, err := Clean(context.Background(), Options{ProjectsRoot: f.projectsRoot, Progress: &progress})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 {
		t.Fatalf("results = %+v", outcome.Results)
	}
	if want := "[1/1] acme/widgets\n"; progress.String() != want {
		t.Fatalf("progress = %q, want %q", progress.String(), want)
	}
}

func TestRPCovEvaluateFailsClosedWhenAnyCheckCannotBeCompleted(t *testing.T) {
	t.Run("skip-sync marker unreadable", func(t *testing.T) {
		isolateWBHome(t)
		f := newFixture(t, "acme", "widgets")
		f.archived()
		notARepo := t.TempDir()
		result := Evaluate(context.Background(), f.projectsRoot, discover.Repo{Org: "acme", Name: "widgets", Path: notARepo})
		if result.Eligible || !strings.Contains(result.Reason, "could not read wb.skip-sync marker") {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("git status unreadable", func(t *testing.T) {
		isolateWBHome(t)
		f := newFixture(t, "acme", "widgets")
		f.archived()
		if err := os.WriteFile(filepath.Join(f.canonical, ".git", "index"), []byte("not an index\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		result := Evaluate(context.Background(), f.projectsRoot, discover.Repo{Org: "acme", Name: "widgets", Path: f.canonical})
		if result.Eligible || !strings.Contains(result.Reason, "could not read git status") {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("untracked path cannot be itemized", func(t *testing.T) {
		isolateWBHome(t)
		f := newFixture(t, "acme", "widgets")
		f.archived()
		if err := os.Symlink("README.md", filepath.Join(f.canonical, "linked")); err != nil {
			t.Fatal(err)
		}
		result := Evaluate(context.Background(), f.projectsRoot, discover.Repo{Org: "acme", Name: "widgets", Path: f.canonical})
		if result.Eligible || !strings.Contains(result.Reason, "could not safely itemize untracked paths") {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("remote branches unreadable", func(t *testing.T) {
		isolateWBHome(t)
		f := newFixture(t, "acme", "widgets")
		f.archived()
		rpCovInstallGitFailShim(t, "*refname:short*")
		result := Evaluate(context.Background(), f.projectsRoot, discover.Repo{Org: "acme", Name: "widgets", Path: f.canonical})
		if result.Eligible || !strings.Contains(result.Reason, "could not resolve remote branches") {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("remote tags unreadable", func(t *testing.T) {
		isolateWBHome(t)
		f := newFixture(t, "acme", "widgets")
		f.archived()
		rpCovInstallGitFailShim(t, "*--tags*")
		result := Evaluate(context.Background(), f.projectsRoot, discover.Repo{Org: "acme", Name: "widgets", Path: f.canonical})
		if result.Eligible || !strings.Contains(result.Reason, "could not resolve remote tags") {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("linked worktrees unreadable", func(t *testing.T) {
		isolateWBHome(t)
		f := newFixture(t, "acme", "widgets")
		f.archived()
		rpCovInstallGitFailShim(t, "*\"worktree list\"*")
		result := Evaluate(context.Background(), f.projectsRoot, discover.Repo{Org: "acme", Name: "widgets", Path: f.canonical})
		if result.Eligible || !strings.Contains(result.Reason, "could not read linked worktrees") {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("work log claims unreadable", func(t *testing.T) {
		f := newFixture(t, "acme", "widgets")
		f.archived()
		// The claim scan reads the fixture root's own state home now, so an
		// unreadable claim is planted there rather than in an ambient WB_HOME.
		claimsDir := filepath.Join(f.projectsRoot, ".wb", "worklogs", "task", "runs", "run-1", "claims")
		mustMkdirAll(t, claimsDir)
		mustWriteFile(t, filepath.Join(claimsDir, "claim.json"), "{")
		result := Evaluate(context.Background(), f.projectsRoot, discover.Repo{Org: "acme", Name: "widgets", Path: f.canonical})
		if result.Eligible || !strings.Contains(result.Reason, "could not read WB Work Log claims") {
			t.Fatalf("result = %+v", result)
		}
	})
}

func TestRPCovWorkingTreeBlockersNamesEveryDirtyShape(t *testing.T) {
	t.Parallel()
	status := gitops.RepoStatus{
		Modified:   []string{"a.txt"},
		Untracked:  []string{"b.txt"},
		Conflicted: []string{"c.txt"},
	}
	blockers := workingTreeBlockers(status, true)
	if len(blockers) != 3 {
		t.Fatalf("blockers = %v, want modified, untracked and conflicted", blockers)
	}
	withoutUntracked := workingTreeBlockers(status, false)
	if len(withoutUntracked) != 2 {
		t.Fatalf("blockers = %v, want untracked excluded when not requested", withoutUntracked)
	}
	for _, blocker := range withoutUntracked {
		if strings.Contains(blocker, "untracked") {
			t.Fatalf("untracked blocker leaked into the excludeUntracked result: %v", withoutUntracked)
		}
	}
}

func TestRPCovUnpushedBranchBlockersFallBackToTheGenericCommitCount(t *testing.T) {
	t.Parallel()
	blockers := unpushedBranchBlockers(gitops.RepoStatus{Unpushed: []string{"abc1234 wip", "def5678 more"}})
	if len(blockers) != 1 || blockers[0] != "2 unpushed commits" {
		t.Fatalf("blockers = %v, want the generic unattributed-commit refusal", blockers)
	}
	if blockers := unpushedBranchBlockers(gitops.RepoStatus{}); blockers != nil {
		t.Fatalf("blockers = %v, want nil for a clean status", blockers)
	}
}

func TestRPCovRunGitReportsExitAndLaunchFailures(t *testing.T) {
	runnertest.AllowRealProcess(t)
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q", "-b", "main")
	if _, err := runGit(context.Background(), realRunner(), dir, "rev-parse", "--verify", "does-not-exist"); err == nil ||
		!strings.Contains(err.Error(), "rev-parse") {
		t.Fatalf("exit error = %v, want git's own refusal quoted", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runGit(cancelled, realRunner(), dir, "status"); err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("cancellation error = %v, want the launch failure", err)
	}
}

func TestRPCovRefHelpersReportGitFailures(t *testing.T) {
	runnertest.AllowRealProcess(t)
	notARepo := t.TempDir()
	if _, err := localOnlyBranches(context.Background(), realRunner(), notARepo); err == nil {
		t.Fatal("localOnlyBranches accepted a directory that is not a repository")
	}
	if _, err := unpushedTagNames(context.Background(), realRunner(), notARepo); err == nil {
		t.Fatal("unpushedTagNames accepted a directory that is not a repository")
	}
	if _, err := remoteRefNames(context.Background(), realRunner(), notARepo, "--heads", "refs/heads/"); err == nil {
		t.Fatal("remoteRefNames accepted a directory that is not a repository")
	}
	if _, err := linkedWorktreePaths(context.Background(), realRunner(), notARepo); err == nil {
		t.Fatal("linkedWorktreePaths accepted a directory that is not a repository")
	}
}

func TestRPCovLocalOnlyBranchesReportsAMissingRemote(t *testing.T) {
	runnertest.AllowRealProcess(t)
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q", "-b", "main")
	mustWriteFile(t, filepath.Join(dir, "f.txt"), "v1\n")
	run(t, dir, "git", "add", "-A")
	run(t, dir, "git", "commit", "-qm", "v1")

	if _, err := localOnlyBranches(context.Background(), realRunner(), dir); err == nil {
		t.Fatal("localOnlyBranches succeeded for a repository with no origin remote")
	}
}

func TestRPCovRemoteRefNamesSkipsMalformedLinesAndStripsThePeelSuffix(t *testing.T) {
	runnertest.AllowRealProcess(t)
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q", "-b", "main")
	payload := strings.Join([]string{
		"garbage-with-one-field",
		strings.Repeat("a", 40) + "\trefs/heads/main",
		strings.Repeat("b", 40) + "\trefs/heads/feature",
		strings.Repeat("c", 40) + "\trefs/tags/v1^{}",
	}, "\n") + "\n"
	rpCovInstallGitOutputShim(t, "*ls-remote*", payload)

	names, err := remoteRefNames(context.Background(), realRunner(), dir, "--heads", "refs/heads/")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 3 || !names["main"] || !names["feature"] || !names["refs/tags/v1"] {
		t.Fatalf("names = %v, want the two branches and the de-peeled tag with the malformed line skipped", names)
	}
	if names[""] {
		t.Fatal("a malformed ls-remote line produced an empty ref name")
	}
}

func TestRPCovNonTerminalClaimsReportsEveryUnreadableInputShape(t *testing.T) {
	t.Parallel()
	// claimFor plants a claim in the state home that derives from the projects
	// root the scan is given.
	claimFor := func(root, claimID, repository, lifecycle string) {
		t.Helper()
		dir := filepath.Join(root, ".wb", "worklogs", "task", "runs", "run-1", "claims")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(map[string]any{
			"claim_id": claimID, "repository": repository, "task": "task", "worktree": "/gone", "lifecycle": lifecycle,
		})
		if err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, filepath.Join(dir, claimID+".json"), string(raw))
	}

	t.Run("unresolvable projects root", func(t *testing.T) {
		t.Parallel()
		blocker := filepath.Join(t.TempDir(), "regular-file")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := nonTerminalClaims(filepath.Join(blocker, "projects"), "acme/widgets"); err == nil {
			t.Fatal("nonTerminalClaims accepted an unresolvable projects root")
		}
	})

	t.Run("malformed glob", func(t *testing.T) {
		t.Parallel()
		// A projects root containing a glob metacharacter makes the claim
		// pattern under its state home malformed.
		root := filepath.Join(t.TempDir(), "projects[")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := nonTerminalClaims(root, "acme/widgets"); err == nil {
			t.Fatal("nonTerminalClaims accepted a projects root whose glob pattern is malformed")
		}
	})

	t.Run("claim path is not a file", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		dir := filepath.Join(root, ".wb", "worklogs", "task", "runs", "run-1", "claims", "claim.json")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := nonTerminalClaims(root, "acme/widgets"); err == nil || !strings.Contains(err.Error(), "read claim") {
			t.Fatalf("error = %v, want the unreadable-claim refusal", err)
		}
	})

	t.Run("claim is not json", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		dir := filepath.Join(root, ".wb", "worklogs", "task", "runs", "run-1", "claims")
		mustMkdirAll(t, dir)
		mustWriteFile(t, filepath.Join(dir, "claim.json"), "{")
		if _, err := nonTerminalClaims(root, "acme/widgets"); err == nil || !strings.Contains(err.Error(), "parse claim") {
			t.Fatalf("error = %v, want the unparseable-claim refusal", err)
		}
	})

	t.Run("claim for another repository is ignored", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		claimFor(root, "claim", "other/repo", "active")
		blockers, err := nonTerminalClaims(root, "acme/widgets")
		if err != nil || len(blockers) != 0 {
			t.Fatalf("blockers = %v, err=%v, want a claim for another repository ignored", blockers, err)
		}
	})

	t.Run("claim without an id", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		dir := filepath.Join(root, ".wb", "worklogs", "task", "runs", "run-1", "claims")
		mustMkdirAll(t, dir)
		mustWriteFile(t, filepath.Join(dir, "anonymous.json"),
			`{"repository":"acme/widgets","task":"task","worktree":"/gone","lifecycle":"active"}`)
		if _, err := nonTerminalClaims(root, "acme/widgets"); err == nil || !strings.Contains(err.Error(), "has no claim_id") {
			t.Fatalf("error = %v, want the missing-claim-id refusal", err)
		}
	})

	t.Run("terminal seal is unreadable", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		claimFor(root, "claim-1", "acme/widgets", "active")
		terminalPath := filepath.Join(root, ".wb", "worklogs", "task", "runs", "run-1", "terminals", "claim-1.json")
		if err := os.MkdirAll(terminalPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := nonTerminalClaims(root, "acme/widgets"); err == nil || !strings.Contains(err.Error(), "read terminal seal") {
			t.Fatalf("error = %v, want the unreadable-terminal refusal", err)
		}
	})
}

func TestRPCovPlanUntrackedRejectsMissingRootsAndTraversalShapes(t *testing.T) {
	t.Parallel()
	if _, err := planUntracked(filepath.Join(t.TempDir(), "missing"), []string{"x"}); err == nil ||
		!strings.Contains(err.Error(), "open clone root") {
		t.Fatalf("missing root error = %v", err)
	}

	roots, err := untrackedRoots([]string{"a", "a/b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 || roots[0] != "a" || roots[1] != "c" {
		t.Fatalf("roots = %v, want nested paths collapsed into their parent", roots)
	}

	for _, raw := range []string{"", "  ", ".", "/absolute"} {
		if _, err := safeRelativePath(raw); err == nil {
			t.Errorf("safeRelativePath(%q) accepted a non-relative clone path", raw)
		}
	}
	relative, err := safeRelativePath(" dir/sub/ ")
	if err != nil || relative != "dir/sub" {
		t.Fatalf("safeRelativePath = %q, err=%v", relative, err)
	}
}

func TestRPCovPlanUntrackedRejectsExcessiveDepthAndBrokenParentPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "plain.txt"), "x\n")
	rootFile, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rootFile.Close() })

	var entries []UntrackedEntry
	if err := collectPathAt(rootFile, root, "plain.txt", &entries, untrackedMaxDepth+1); err == nil ||
		!strings.Contains(err.Error(), "nests deeper") {
		t.Fatalf("depth error = %v", err)
	}
	if err := collectPathAt(rootFile, root, "missing/child.txt", &entries, 0); err == nil ||
		!strings.Contains(err.Error(), "inspect untracked parent") {
		t.Fatalf("missing parent error = %v", err)
	}
	mustWriteFile(t, filepath.Join(root, "file.txt"), "x\n")
	if err := collectPathAt(rootFile, root, "file.txt/child.txt", &entries, 0); err == nil ||
		!strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("non-directory parent error = %v", err)
	}
	if err := collectEntryAt(rootFile, root, "gone.txt", "gone.txt", &entries, 0); err == nil ||
		!strings.Contains(err.Error(), "inspect untracked path") {
		t.Fatalf("missing entry error = %v", err)
	}
}

func TestRPCovOpenParentAndDirectoryListingReportClosedDescriptors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "file.txt"), "x\n")
	closedRoot, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := closedRoot.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openParentAt(closedRoot, root, "file.txt"); err == nil {
		t.Fatal("openParentAt accepted a closed root descriptor")
	}

	closedDirectory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := closedDirectory.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := directoryNames(closedDirectory, root); err == nil {
		t.Fatal("directoryNames accepted a closed descriptor")
	}
}

func TestRPCovDeleteExactUntrackedRefusesEmptyAndUnreadablePlans(t *testing.T) {
	t.Parallel()
	if err := deleteExactUntracked(context.Background(), t.TempDir(), nil); err == nil ||
		!strings.Contains(err.Error(), "empty plan") {
		t.Fatalf("empty-plan error = %v", err)
	}

	repo := t.TempDir()
	run(t, repo, "git", "init", "-q", "-b", "main")
	mustWriteFile(t, filepath.Join(repo, "tracked.txt"), "v1\n")
	run(t, repo, "git", "add", "-A")
	run(t, repo, "git", "commit", "-qm", "v1")
	planned := []UntrackedEntry{{Path: "untracked.txt", Kind: "file", Size: 1}}

	corrupt := t.TempDir()
	run(t, corrupt, "git", "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(corrupt, ".git", "index"), []byte("not an index\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := deleteExactUntracked(context.Background(), corrupt, planned); err == nil ||
		!strings.Contains(err.Error(), "cannot reread git status") {
		t.Fatalf("status error = %v", err)
	}

	// An untracked symlink is reported by git but refused by the planner, so
	// the revalidation fails closed before anything is deleted.
	if err := os.Symlink("tracked.txt", filepath.Join(repo, "untracked.txt")); err != nil {
		t.Fatal(err)
	}
	if err := deleteExactUntracked(context.Background(), repo, planned); err == nil ||
		!strings.Contains(err.Error(), "cannot reread paths") {
		t.Fatalf("re-plan error = %v", err)
	}
}

func TestRPCovRemoveExactPathAtRefusesUnsafeEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "file.txt"), "content\n")
	rootFile, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rootFile.Close() })

	planned, err := planUntracked(root, []string{"file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	manifest := map[string]UntrackedEntry{}
	for _, entry := range planned {
		manifest[entry.Path] = entry
	}

	if err := removeExactPathAt(rootFile, root, "file.txt", manifest, untrackedMaxDepth+1); err == nil ||
		!strings.Contains(err.Error(), "nests too deeply") {
		t.Fatalf("depth error = %v", err)
	}
	if err := removeExactPathAt(rootFile, root, "missing/child.txt", manifest, 0); err == nil {
		t.Fatal("removeExactPathAt accepted a path whose parent is absent")
	}
	if err := removeExactPathAt(rootFile, root, "unlisted.txt", manifest, 0); err == nil ||
		!strings.Contains(err.Error(), "was not in the authorised manifest") {
		t.Fatalf("unlisted error = %v", err)
	}
	inManifestButGone := map[string]UntrackedEntry{"gone.txt": {Path: "gone.txt", Kind: "file"}}
	if err := removeExactPathAt(rootFile, root, "gone.txt", inManifestButGone, 0); err == nil ||
		!strings.Contains(err.Error(), "inspect gone.txt") {
		t.Fatalf("missing-entry error = %v", err)
	}

	changed := map[string]UntrackedEntry{"file.txt": {Path: "file.txt", Kind: "file", Size: planned[0].Size + 1,
		device: planned[0].device, inode: planned[0].inode, mode: planned[0].mode, hash: planned[0].hash}}
	if err := removeExactPathAt(rootFile, root, "file.txt", changed, 0); err == nil ||
		!strings.Contains(err.Error(), "changed while deleting") {
		t.Fatalf("changed-entry error = %v", err)
	}

	wrongHash := map[string]UntrackedEntry{"file.txt": planned[0]}
	entry := wrongHash["file.txt"]
	entry.hash = strings.Repeat("0", len(entry.hash))
	wrongHash["file.txt"] = entry
	if err := removeExactPathAt(rootFile, root, "file.txt", wrongHash, 0); err == nil ||
		!strings.Contains(err.Error(), "changed while deleting") {
		t.Fatalf("hash error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "file.txt")); err != nil {
		t.Fatalf("a refused removal deleted the file anyway: %v", err)
	}
}

func TestRPCovRemoveExactPathAtRefusesUndeclaredAndChangedDirectoryChildren(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "dir", "known.txt"), "known\n")
	mustWriteFile(t, filepath.Join(root, "dir", "extra.txt"), "extra\n")
	rootFile, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rootFile.Close() })

	planned, err := planUntracked(root, []string{"dir"})
	if err != nil {
		t.Fatal(err)
	}
	full := map[string]UntrackedEntry{}
	for _, entry := range planned {
		full[entry.Path] = entry
	}
	if _, ok := full["dir/extra.txt"]; !ok {
		t.Fatalf("fixture did not itemize both children: %+v", planned)
	}

	missingChild := map[string]UntrackedEntry{"dir": full["dir"], "dir/known.txt": full["dir/known.txt"]}
	if err := removeExactPathAt(rootFile, root, "dir", missingChild, 0); err == nil ||
		!strings.Contains(err.Error(), "additional path dir/extra.txt appeared") {
		t.Fatalf("additional-path error = %v", err)
	}

	// The first child is refused because its recorded content hash no longer
	// matches; that child error must be propagated out of the directory walk
	// rather than being swallowed.
	changedChild := map[string]UntrackedEntry{}
	for key, value := range full {
		changedChild[key] = value
	}
	entry := changedChild["dir/extra.txt"]
	entry.hash = strings.Repeat("0", len(entry.hash))
	changedChild["dir/extra.txt"] = entry
	if err := removeExactPathAt(rootFile, root, "dir", changedChild, 0); err == nil ||
		!strings.Contains(err.Error(), "changed while deleting") {
		t.Fatalf("child error = %v", err)
	}
}

func TestRPCovWriteArchiveCleanReceiptReportsUnusableHomesAndTargets(t *testing.T) {
	t.Parallel()
	if _, err := writeArchiveCleanReceipt(rpCovUnusableProjectsRoot(t), archiveCleanReceipt{Repository: "acme/widgets"}); err == nil {
		t.Fatal("writeArchiveCleanReceipt accepted an unusable projects root")
	}

	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, ".wb"))
	mustWriteFile(t, filepath.Join(root, ".wb", "reports"), "a regular file where the reports directory belongs\n")
	if _, err := writeArchiveCleanReceipt(root, archiveCleanReceipt{Repository: "acme/widgets"}); err == nil {
		t.Fatal("writeArchiveCleanReceipt accepted a receipt directory that is a file")
	}

	missingParent := filepath.Join(t.TempDir(), "missing", "receipt.json")
	if err := overwriteArchiveCleanReceipt(missingParent, archiveCleanReceipt{Repository: "acme/widgets"}); err == nil ||
		!strings.Contains(err.Error(), "create archive-clean receipt") {
		t.Fatalf("staging error = %v", err)
	}

	occupied := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.MkdirAll(filepath.Join(occupied, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := overwriteArchiveCleanReceipt(occupied, archiveCleanReceipt{Repository: "acme/widgets"}); err == nil ||
		!strings.Contains(err.Error(), "publish archive-clean receipt") {
		t.Fatalf("publish error = %v", err)
	}
}

// rpCovUnusableProjectsRoot returns a projects root beneath a regular file so
// path resolution fails rather than returning a usable state home. The state
// home derives from the projects root now, so callers pass the returned root
// explicitly instead of setting the retired WB_HOME.
func rpCovUnusableProjectsRoot(t *testing.T) string {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(blocker, "projects")
}

func rpCovShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// rpCovInstallGitFailShim prepends a git shim that fails any invocation whose
// argv matches pattern and delegates everything else to the real git binary.
func rpCovInstallGitFailShim(t *testing.T, pattern string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n  " + pattern + ") echo 'shim: refused' >&2; exit 128;;\nesac\n" +
		"exec " + rpCovShellQuote(realGit) + " \"$@\"\n"
	if err := testenv.WriteExecutableFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// rpCovInstallGitOutputShim prepends a git shim that answers matching argv
// with payload and delegates everything else to the real git binary.
func rpCovInstallGitOutputShim(t *testing.T, pattern, payload string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.txt")
	if err := os.WriteFile(payloadPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$*\" in\n  " + pattern + ") cat " + rpCovShellQuote(payloadPath) + "; exit 0;;\nesac\n" +
		"exec " + rpCovShellQuote(realGit) + " \"$@\"\n"
	if err := testenv.WriteExecutableFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
