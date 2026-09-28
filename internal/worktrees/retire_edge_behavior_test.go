package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

func expectRetireRemoteRef(fake *runnertest.Fake, directory, remote, ref, response string) {
	fake.ExpectArgv([]string{"git", "-C", directory, "ls-remote", remote, ref}, runner.Result{CombinedOutput: response}, nil)
}

func TestRetireRemoteObservationRejectsMalformedAndFailedQueries(t *testing.T) {
	t.Parallel()
	const directory = "/fixture/canonical"
	const ref = "refs/heads/task"
	for _, tc := range []struct {
		name, output, want string
		fail               bool
	}{
		{name: "absent"},
		{name: "valid", output: strings.Repeat("a", 40) + "\t" + ref + "\n"},
		{name: "wrong ref", output: strings.Repeat("a", 40) + "\trefs/heads/other\n", want: "invalid remote ref observation"},
		{name: "malformed SHA", output: "not-a-sha\t" + ref + "\n", want: "invalid remote ref observation"},
		{name: "query fails", fail: true, want: "inspect remote ref"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			expectRetireRemoteRef(fake, directory, "origin", ref, tc.output)
			if tc.fail {
				fake.FailCall(1, errors.New("remote unavailable"))
			}
			got, err := retireRemoteSHA(withGitRunner(context.Background(), fake), directory, "origin", ref)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) || got != "" {
					t.Fatalf("remote SHA = (%q, %v), want %q", got, err, tc.want)
				}
				return
			}
			if err != nil || got != strings.TrimSpace(strings.Split(tc.output, "\t")[0]) {
				t.Fatalf("remote SHA = (%q, %v), output %q", got, err, tc.output)
			}
		})
	}
}

func TestRetirePublicationRejectsConflictingReceiptsWithoutPushing(t *testing.T) {
	t.Parallel()
	const canonical = "/fixture/canonical"
	sha := strings.Repeat("a", 40)
	other := strings.Repeat("b", 40)
	result := RetireResult{Canonical: canonical, RetiredRef: "retired/task", SourceSHA: sha, ArchiveRef: "archive/task", ArchiveSHA: other}
	t.Run("source conflict", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		expectRetireRemoteRef(fake, canonical, "origin", retireSourceRef(result), other+"\t"+retireSourceRef(result)+"\n")
		attempt := result
		if err := retirePublishSource(withGitRunner(context.Background(), fake), &attempt); err == nil || !strings.Contains(err.Error(), "conflicting commit") {
			t.Fatalf("conflicting source receipt error = %v", err)
		}
		if attempt.Phase != "" || fake.CallCount() != 1 {
			t.Fatalf("conflict changed phase or issued another Git call: %+v, calls=%d", attempt, fake.CallCount())
		}
	})
	t.Run("source observation fails", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		expectRetireRemoteRef(fake, canonical, "origin", retireSourceRef(result), "")
		fake.FailCall(1, errors.New("source remote unavailable"))
		attempt := result
		if err := retirePublishSource(withGitRunner(context.Background(), fake), &attempt); err == nil || !strings.Contains(err.Error(), "source remote unavailable") {
			t.Fatalf("source observation error = %v", err)
		}
	})
	t.Run("missing canonical cannot publish", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		expectRetireRemoteRef(fake, canonical, "origin", retireSourceRef(result), "")
		attempt := result
		if err := retirePublishSource(withGitRunner(context.Background(), fake), &attempt); err == nil {
			t.Fatal("retired source published without a canonical repository")
		}
		if fake.CallCount() != 1 {
			t.Fatalf("missing canonical issued %d Git calls", fake.CallCount())
		}
	})
	t.Run("retired source changed", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		expectRetireRemoteRef(fake, canonical, "origin", retireSourceRef(result), other+"\t"+retireSourceRef(result)+"\n")
		if err := retireVerifyReceipts(withGitRunner(context.Background(), fake), canonical, "archive", result); err == nil || !strings.Contains(err.Error(), "retired source receipt changed") {
			t.Fatalf("changed retired source error = %v", err)
		}
	})
	t.Run("private archive changed", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		expectRetireRemoteRef(fake, canonical, "origin", retireSourceRef(result), sha+"\t"+retireSourceRef(result)+"\n")
		archiveRef := "refs/heads/" + result.ArchiveRef
		expectRetireRemoteRef(fake, canonical, "archive", archiveRef, sha+"\t"+archiveRef+"\n")
		if err := retireVerifyReceipts(withGitRunner(context.Background(), fake), canonical, "archive", result); err == nil || !strings.Contains(err.Error(), "private archive receipt changed") {
			t.Fatalf("changed archive receipt error = %v", err)
		}
	})
	t.Run("deletion lacks durable intent", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		attempt := result
		attempt.Branch = "task"
		attempt.OriginalRemoteSHA = sha
		branchRef := "refs/heads/" + attempt.Branch
		expectRetireRemoteRef(fake, canonical, "origin", branchRef, sha+"\t"+branchRef+"\n")
		if err := retireDeleteOriginal(withGitRunner(context.Background(), fake), &attempt, nil); err == nil || !strings.Contains(err.Error(), "no durable exact-SHA intent") {
			t.Fatalf("missing deletion intent error = %v", err)
		}
		if fake.CallCount() != 1 {
			t.Fatalf("deletion without intent issued %d Git calls", fake.CallCount())
		}
	})
	t.Run("changed proof refuses deletion", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		attempt := result
		attempt.Branch = "task"
		attempt.OriginalRemoteSHA, attempt.DeleteIntentSHA = sha, sha
		branchRef := "refs/heads/" + attempt.Branch
		proofRef := retireDeletionProofRef(attempt)
		expectRetireRemoteRef(fake, canonical, "origin", branchRef, sha+"\t"+branchRef+"\n")
		expectRetireRemoteRef(fake, canonical, "origin", proofRef, other+"\t"+proofRef+"\n")
		if err := retireDeleteOriginal(withGitRunner(context.Background(), fake), &attempt, nil); err == nil || !strings.Contains(err.Error(), "changed before exact-lease deletion") {
			t.Fatalf("changed deletion proof error = %v", err)
		}
		if fake.CallCount() != 2 {
			t.Fatalf("changed proof issued %d Git calls", fake.CallCount())
		}
	})
	t.Run("missing canonical refuses atomic delete", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		attempt := result
		attempt.Branch = "task"
		attempt.OriginalRemoteSHA, attempt.DeleteIntentSHA = sha, sha
		branchRef := "refs/heads/" + attempt.Branch
		expectRetireRemoteRef(fake, canonical, "origin", branchRef, sha+"\t"+branchRef+"\n")
		expectRetireRemoteRef(fake, canonical, "origin", retireDeletionProofRef(attempt), "")
		if err := retireDeleteOriginal(withGitRunner(context.Background(), fake), &attempt, nil); err == nil {
			t.Fatal("atomic deletion accepted a missing canonical repository")
		}
		if fake.CallCount() != 2 {
			t.Fatalf("missing canonical issued %d Git calls", fake.CallCount())
		}
	})
	t.Run("never-published original needs no deletion proof", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		attempt := result
		attempt.Branch = "task"
		branchRef := "refs/heads/" + attempt.Branch
		proofRef := retireDeletionProofRef(attempt)
		expectRetireRemoteRef(fake, canonical, "origin", branchRef, "")
		expectRetireRemoteRef(fake, canonical, "origin", proofRef, "")
		expectRetireRemoteRef(fake, canonical, "origin", branchRef, "")
		if err := retireDeleteOriginal(withGitRunner(context.Background(), fake), &attempt, nil); err != nil || attempt.Phase != "original_deleted" {
			t.Fatalf("never-published original deletion = (%+v, %v)", attempt, err)
		}
	})
	t.Run("original ref reappears during verification", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		attempt := result
		attempt.Branch = "task"
		branchRef := "refs/heads/" + attempt.Branch
		expectRetireRemoteRef(fake, canonical, "origin", branchRef, "")
		expectRetireRemoteRef(fake, canonical, "origin", retireDeletionProofRef(attempt), "")
		expectRetireRemoteRef(fake, canonical, "origin", branchRef, sha+"\t"+branchRef+"\n")
		if err := retireDeleteOriginal(withGitRunner(context.Background(), fake), &attempt, nil); err == nil || !strings.Contains(err.Error(), "deletion verification failed") {
			t.Fatalf("reappeared original ref error = %v", err)
		}
	})
	t.Run("atomic proof changes during verification", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		attempt := result
		attempt.Branch = "task"
		attempt.OriginalRemoteSHA, attempt.DeleteIntentSHA = sha, sha
		branchRef := "refs/heads/" + attempt.Branch
		proofRef := retireDeletionProofRef(attempt)
		expectRetireRemoteRef(fake, canonical, "origin", branchRef, "")
		expectRetireRemoteRef(fake, canonical, "origin", proofRef, sha+"\t"+proofRef+"\n")
		expectRetireRemoteRef(fake, canonical, "origin", branchRef, "")
		expectRetireRemoteRef(fake, canonical, "origin", proofRef, other+"\t"+proofRef+"\n")
		if err := retireDeleteOriginal(withGitRunner(context.Background(), fake), &attempt, nil); err == nil || !strings.Contains(err.Error(), "proof verification failed") {
			t.Fatalf("changed atomic proof error = %v", err)
		}
	})
}

func TestRetireCaptureRefusesUnsafeSourcesAndDestinations(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	if err := os.WriteFile(source, []byte("private data"), 0o600); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, source, destination, want string
	}{
		{name: "missing source parent", source: filepath.Join(root, "missing", "file"), destination: filepath.Join(root, "out1"), want: "no such file"},
		{name: "missing source leaf", source: filepath.Join(root, "missing-file"), destination: filepath.Join(root, "out1b"), want: "no such file"},
		{name: "nonregular source", source: root, destination: filepath.Join(root, "out2"), want: "nonregular file"},
		{name: "destination parent is file", source: source, destination: filepath.Join(blocked, "out3"), want: "not a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if digest, err := retireCaptureFileInjected(tc.source, tc.destination, nil); err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) || digest != "" {
				t.Fatalf("capture = (%q, %v), want %q", digest, err, tc.want)
			}
		})
	}
	t.Run("tree symlink", func(t *testing.T) {
		t.Parallel()
		tree := filepath.Join(root, "tree")
		if err := os.Mkdir(tree, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(source, filepath.Join(tree, "linked")); err != nil {
			t.Fatal(err)
		}
		hashes := map[string]string{}
		if err := retireCaptureTree(tree, filepath.Join(root, "archive"), func(string) bool { return true }, hashes, "tree"); err == nil || !strings.Contains(err.Error(), "refuses symlink") {
			t.Fatalf("symlink tree capture error = %v", err)
		}
		if len(hashes) != 0 {
			t.Fatalf("symlink contributed archive hashes: %+v", hashes)
		}
	})
	t.Run("missing tree", func(t *testing.T) {
		t.Parallel()
		if err := retireCaptureTree(filepath.Join(root, "missing-tree"), filepath.Join(root, "archive"), func(string) bool { return true }, map[string]string{}, "tree"); err == nil {
			t.Fatal("missing tree accepted")
		}
	})
	t.Run("tree destination is obstructed", func(t *testing.T) {
		t.Parallel()
		tree := filepath.Join(root, "plain-tree")
		if err := os.Mkdir(tree, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tree, "entry"), []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := retireCaptureTree(tree, blocked, func(string) bool { return true }, map[string]string{}, "tree"); err == nil {
			t.Fatal("obstructed archive destination was accepted")
		}
	})
	t.Run("tree changes during traversal", func(t *testing.T) {
		t.Parallel()
		tree := filepath.Join(root, "changing-tree")
		later := filepath.Join(tree, "z-later")
		if err := os.MkdirAll(later, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tree, "a-first"), []byte("first"), 0o600); err != nil {
			t.Fatal(err)
		}
		var removalErr error
		err := retireCaptureTree(tree, filepath.Join(root, "changing-archive"), func(path string) bool {
			if path == "a-first" {
				removalErr = os.Remove(later)
			}
			return false
		}, map[string]string{}, "tree")
		if removalErr != nil {
			t.Fatal(removalErr)
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("tree drift error = %v, want missing later directory", err)
		}
	})
}

func TestRetireSmallPolicyAndFilesystemErrors(t *testing.T) {
	t.Parallel()
	if got := retirePreserveMode(RetireResult{Preserve: "tag"}); got != "tag" {
		t.Fatalf("preserve mode = %q", got)
	}
	if got := retireArchiveManifestPreserve(retireArchiveManifest{Preserve: "tag"}); got != "tag" {
		t.Fatalf("manifest preserve mode = %q", got)
	}
	if got := retireArchiveManifestPreserve(retireArchiveManifest{}); got != "branch" {
		t.Fatalf("default manifest preserve mode = %q", got)
	}
	inspect := func(context.Context, string) (RetiredArchiveInspection, error) {
		return RetiredArchiveInspection{}, errors.New("inspector failed")
	}
	if err := retireCheckPrivateArchive(context.Background(), RetireResult{Repository: "acme/app"}, inspect); err == nil {
		t.Fatal("archive inspector failure was accepted")
	}
	if err := retireCheckPrivateArchive(context.Background(), RetireResult{Repository: "invalid-repository"}, inspect); err == nil || !strings.Contains(err.Error(), "invalid source repository") {
		t.Fatalf("invalid repository preflight error = %v", err)
	}
	root := t.TempDir()
	if err := retireCheckIgnored(context.Background(), root); err == nil {
		t.Fatal("nonrepository ignored-file query was accepted")
	}
	if err := retireCheckUntrackedPath(root, filepath.Join("missing", "file")); err == nil {
		t.Fatal("missing untracked parent was accepted")
	}
	if err := retireCheckUntrackedPath(root, "missing"); err == nil {
		t.Fatal("missing untracked leaf was accepted")
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := retireCheckUntrackedPath(root, "directory"); err == nil || !strings.Contains(err.Error(), "nonregular") {
		t.Fatalf("untracked directory error = %v", err)
	}
	if _, err := retireGitBytes(context.Background(), root, "show", "missing"); err == nil || !strings.Contains(err.Error(), "read archive Git object") {
		t.Fatalf("missing Git object bytes error = %v", err)
	}
	if _, err := retireGitObjectSHA(context.Background(), root, "missing"); err == nil {
		t.Fatal("missing Git object hash succeeded")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := retireGitObjectSHA(cancelled, root, "missing"); err == nil {
		t.Fatal("cancelled Git object hash started")
	}
	if err := writeRetireReportInjected(RetireResult{ReportPath: filepath.Join(root, "directory", "report.json"), IntentAt: time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)}, nil); err == nil {
		t.Fatal("out-of-range receipt time was encoded")
	}
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeRetireReportInjected(RetireResult{ReportPath: filepath.Join(blocked, "report.json")}, nil); err == nil {
		t.Fatal("report path beneath file was accepted")
	}
	if _, err := readRetireReport(filepath.Join(root, "missing-reports", "report.json")); err == nil {
		t.Fatal("receipt beneath missing parent was accepted")
	}
}

func TestRetireClaimReadersRequirePrivateAuthority(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	worktree := t.TempDir()
	if _, _, err := retireReadClaim(home, worktree); err == nil {
		t.Fatal("checkout without Work Log projection was accepted")
	}
	result := RetireResult{EffortID: "effort", RunID: "run", ClaimID: strings.Repeat("b", 64), Worktree: worktree}
	if err := retireValidateRemovedClaim(home, result); err == nil {
		t.Fatal("removed checkout without private claim was accepted")
	}
	result.Repository, result.Branch = "acme/app", "task"
	if err := retirePublishArchive(context.Background(), home, "private-archive", &result); err == nil {
		t.Fatal("private archive publication without a Work Log claim was accepted")
	}
}

func TestRetireCommitSourceRefusesChangedHeldCheckoutBeforeGit(t *testing.T) {
	t.Parallel()
	parentPath := t.TempDir()
	worktreePath := filepath.Join(parentPath, "checkout")
	if err := os.Mkdir(worktreePath, 0o700); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	worktree, err := os.Open(worktreePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worktree.Close() })
	held := &cleanupWorktreeHandle{parentPath: parentPath, parent: parent, worktreePath: worktreePath, worktree: worktree}
	if err := os.Rename(worktreePath, filepath.Join(parentPath, "moved")); err != nil {
		t.Fatal(err)
	}
	prepared := false
	committed, err := retireCommitSource(context.Background(), parentPath, held, "message", func(string, string) error {
		prepared = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "path changed") || committed || prepared {
		t.Fatalf("changed held checkout = (committed %t, prepared %t, error %v)", committed, prepared, err)
	}
}

func TestRetireArchiveVerificationRefusesUnfetchedOrChangedCommit(t *testing.T) {
	t.Parallel()
	const working = "/fixture/archive-staging"
	const ref = "refs/heads/retired/task"
	sha := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name, observed, want string
		fail                 bool
	}{
		{name: "fetch fails", fail: true, want: "archive unavailable"},
		{name: "commit changed", observed: strings.Repeat("b", 40), want: "private archive commit changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			fake.ExpectArgv([]string{"git", "-C", working, "fetch", "--no-tags", "private", ref}, runner.Result{}, nil)
			if tc.fail {
				fake.FailCall(1, errors.New("archive unavailable"))
			} else {
				fake.ExpectArgv([]string{"git", "-C", working, "rev-parse", "FETCH_HEAD"}, runner.Result{CombinedOutput: tc.observed + "\n"}, nil)
			}
			err := retireVerifyArchive(withGitRunner(context.Background(), fake), working, "private", ref, sha, retireArchiveManifest{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("archive verification error = %v, want %q", err, tc.want)
			}
		})
	}
}
