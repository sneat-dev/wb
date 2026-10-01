package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktreeretire"
)

func TestRetirementOptionAndArchivePathBoundaries(t *testing.T) {
	t.Parallel()

	options, err := normalizeRetireOptions(RetireOptions{Task: "retire-task"})
	if err != nil || options.Preserve != "branch" || options.Now == nil {
		t.Fatalf("normalized defaults = %#v, %v", options, err)
	}
	if tagged, err := normalizeRetireOptions(RetireOptions{Task: "retire-task", Repository: "acme/app", Preserve: "tag"}); err != nil || tagged.Preserve != "tag" {
		t.Fatalf("normalized tag options = %#v, %v", tagged, err)
	}
	for _, tc := range []struct {
		name    string
		options RetireOptions
		want    string
	}{
		{name: "preserve", options: RetireOptions{Task: "retire-task", Preserve: "commit"}, want: "unsupported --preserve"},
		{name: "task", options: RetireOptions{Task: "../task"}, want: "invalid retirement task"},
		{name: "repository", options: RetireOptions{Task: "retire-task", Repository: "invalid"}, want: "repository"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := normalizeRetireOptions(tc.options); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("normalize error = %v, want %q", err, tc.want)
			}
			if _, err := Retire(context.Background(), tc.options); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Retire error = %v, want %q", err, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		path string
		want bool
	}{
		{path: "run.json", want: true},
		{path: "original-prompt.json", want: true},
		{path: "original-prompt.txt", want: true},
		{path: "migration.json", want: true},
		{path: "claims/claim.json", want: true},
		{path: "terminals/claim.json", want: true},
		{path: "cleanups/claim.json", want: true},
		{path: "corrections/claim/event.json", want: true},
		{path: "dirty-discard/claim/evidence.json", want: true},
		{path: "reports/report.md", want: true},
		{path: "claims/other.json", want: false},
		{path: "terminals/other.json", want: false},
		{path: "cleanups/other.json", want: false},
		{path: "corrections/other/event.json", want: false},
		{path: "dirty-discard/other/evidence.json", want: false},
		{path: "reports/other.md", want: false},
		{path: "unrelated/file.json", want: false},
	} {
		if got := worktreeretire.ArchiveIncludesRunPath("claim", "report.md", tc.path); got != tc.want {
			t.Errorf("include %q = %t, want %t", tc.path, got, tc.want)
		}
	}
}

func TestRetirementEntrySelectionBoundaries(t *testing.T) {
	t.Parallel()

	attached := ListResult{Repository: "acme/app", Branch: "source"}
	entry, resume, err := selectRetirementEntry(ListOutcome{Results: []ListResult{attached}}, RetireOptions{Task: "retire-task"})
	if err != nil || resume || entry.Repository != attached.Repository {
		t.Fatalf("selected entry = %#v, resume=%t, err=%v", entry, resume, err)
	}
	for _, tc := range []struct {
		name      string
		inventory ListOutcome
		options   RetireOptions
		want      string
		resume    bool
	}{
		{name: "diagnostic", inventory: ListOutcome{Diagnostics: []ListDiagnostic{{Message: "malformed"}}}, want: "malformed candidate"},
		{name: "missing dry run", want: "exactly one"},
		{name: "missing apply resumes", options: RetireOptions{Apply: true}, resume: true},
		{name: "ambiguous", inventory: ListOutcome{Results: []ListResult{attached, attached}}, want: "found 2"},
		{name: "repository filter", inventory: ListOutcome{Results: []ListResult{attached}}, options: RetireOptions{Repository: "acme/other"}, want: "found 0"},
		{name: "external", inventory: ListOutcome{Results: []ListResult{{Repository: "acme/app", Branch: "source", External: true}}}, want: "managed attached branch"},
		{name: "detached", inventory: ListOutcome{Results: []ListResult{{Repository: "acme/app", Branch: "source", Detached: true}}}, want: "managed attached branch"},
		{name: "branchless", inventory: ListOutcome{Results: []ListResult{{Repository: "acme/app"}}}, want: "managed attached branch"},
		{name: "locked", inventory: ListOutcome{Results: []ListResult{{Repository: "acme/app", Branch: "source", Locked: true}}}, options: RetireOptions{Task: "retire-task"}, want: "competing lifecycle lock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, resume, err := selectRetirementEntry(tc.inventory, tc.options)
			if tc.resume {
				if err != nil || !resume {
					t.Fatalf("resume selection = %t, %v", resume, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || resume {
				t.Fatalf("selection = resume %t, error %v; want %q", resume, err, tc.want)
			}
		})
	}
}

func TestRetirementArchiveTargetBoundaries(t *testing.T) {
	t.Parallel()

	if err := validateRetiredArchiveRepositoryName(" backstage-retired"); err == nil {
		t.Fatal("archive repository with surrounding whitespace was accepted")
	}
	if err := validateRetiredArchiveRepositoryName("backstage-retired"); err != nil {
		t.Fatalf("valid archive repository rejected: %v", err)
	}
	if _, err := ResolveRetiredArchiveTarget("../acme"); err == nil {
		t.Fatal("invalid archive organization was accepted")
	}
	if _, err := PlanRetiredArchivePreflight(context.Background(), "invalid", nil); err == nil {
		t.Fatal("invalid source repository was accepted")
	}

	for _, tc := range []struct {
		name       string
		inspection RetiredArchiveInspection
		err        error
		want       string
	}{
		{name: "missing", inspection: RetiredArchiveInspection{}, want: "missing"},
		{name: "unavailable", err: errors.New("offline"), want: "unavailable"},
		{name: "public", inspection: RetiredArchiveInspection{Exists: true, Repository: "acme/backstage-retired"}, want: "public"},
		{name: "wrong identity", inspection: RetiredArchiveInspection{Exists: true, Private: true, Repository: "other/backstage-retired"}, want: "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan, err := PlanRetiredArchivePreflight(context.Background(), "acme/app", func(context.Context, string) (RetiredArchiveInspection, error) {
				return tc.inspection, tc.err
			})
			if err != nil || plan.Outcome != "refused" || !strings.Contains(plan.Refusal, tc.want) {
				t.Fatalf("archive plan = %#v, %v; want refusal %q", plan, err, tc.want)
			}
		})
	}
	plan, err := PlanRetiredArchivePreflight(context.Background(), "acme/app", func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
		return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
	})
	if err != nil || plan.Outcome != "planned" || plan.ArchiveRepository != "acme/backstage-retired" {
		t.Fatalf("planned archive = %#v, %v", plan, err)
	}
}

func TestRetirementReceiptAndCaptureFailureBoundaries(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	worktree := t.TempDir()
	if _, _, err := retireReadClaim(home, worktree); err == nil {
		t.Fatal("missing Work Log projection was accepted")
	}
	result := RetireResult{
		Task: "retire-task", Repository: "acme/app", Branch: "source",
		Worktree: worktree, EffortID: "effort", RunID: "run", ClaimID: strings.Repeat("a", 64),
	}
	if err := retireValidateRemovedClaim(home, result); err == nil {
		t.Fatal("missing immutable Work Log claim was accepted")
	}
	if err := retirePublishArchive(context.Background(), home, "archive", &result); err == nil {
		t.Fatal("archive publication without a Work Log claim was accepted")
	}
	if _, err := retireResumeRemoved(context.Background(), home, RetireOptions{Task: result.Task}); err == nil {
		t.Fatal("removed checkout without a receipt was accepted")
	}

	receipt := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(receipt, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRetireReport(receipt); err == nil {
		t.Fatal("malformed retirement receipt was accepted")
	}
	if _, err := worktreeretire.CaptureFileInjected(filepath.Join(home, "missing", "source"), filepath.Join(home, "capture"), nil); err == nil {
		t.Fatal("missing capture source was accepted")
	}
}

func TestRetirementResumeReceiptCorroboration(t *testing.T) {
	t.Parallel()
	remoteSHA := strings.Repeat("a", 40)
	planned := RetireResult{
		Task: "retire-task", Repository: "acme/app", ArchiveRepository: "acme/backstage-retired",
		Worktree: "/worktree", Canonical: "/canonical", WorktreesRoot: "/worktrees", Branch: "source",
		ClaimID: "claim", EffortID: "effort", RunID: "run",
	}
	prior := planned
	prior.OriginalRemoteSHA = remoteSHA
	prior.Phase = "committed"
	if err := corroborateRetireResumeReceipt(prior, planned, "branch", remoteSHA); err != nil {
		t.Fatal(err)
	}

	deleteIntent := prior
	deleteIntent.DeleteIntentSHA = remoteSHA
	if err := corroborateRetireResumeReceipt(deleteIntent, planned, "branch", ""); err != nil {
		t.Fatalf("durable delete intent was rejected: %v", err)
	}

	deleted := prior
	deleted.Phase = "original_deleted"
	if err := corroborateRetireResumeReceipt(deleted, planned, "branch", ""); err != nil {
		t.Fatalf("completed remote deletion was rejected: %v", err)
	}
	if err := corroborateRetireResumeReceipt(deleted, planned, "branch", remoteSHA); err == nil {
		t.Fatal("completed deletion with a restored remote branch was accepted")
	}

	conflicting := prior
	conflicting.ClaimID = "other"
	if err := corroborateRetireResumeReceipt(conflicting, planned, "branch", remoteSHA); err == nil {
		t.Fatal("conflicting retirement receipt was accepted")
	}
}

func TestRetirementGitFailureBoundaries(t *testing.T) {
	t.Parallel()

	const canonical = "/fixture/canonical"
	sha := strings.Repeat("a", 40)
	other := strings.Repeat("b", 40)
	result := RetireResult{Canonical: canonical, Branch: "source", RetiredRef: "retired/source", SourceSHA: sha, OriginalRemoteSHA: sha}
	ref := worktreeretire.SourceRef(result)
	ctx := lifecycleGitContext(t, canonical, lifecycleGitReply{operation: "ls-remote", output: other + "\t" + ref + "\n"})
	if err := worktreeretire.PublishSource(ctx, &result, retireTransactionPorts()); err == nil || !strings.Contains(err.Error(), "conflicting commit") {
		t.Fatalf("conflicting source publication error = %v", err)
	}
	ctx = lifecycleGitContext(t, canonical,
		lifecycleGitReply{operation: "ls-remote", output: sha + "\t" + ref + "\n"},
		lifecycleGitReply{operation: "ls-remote", output: other + "\t" + ref + "\n"},
	)
	if err := worktreeretire.PublishSource(ctx, &result, retireTransactionPorts()); err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("changed source verification error = %v", err)
	}

	branchRef := "refs/heads/" + result.Branch
	ctx = lifecycleGitContext(t, canonical, lifecycleGitReply{operation: "ls-remote", output: sha + "\t" + branchRef + "\n"})
	if err := worktreeretire.DeleteOriginal(ctx, &result, nil, retireTransactionPorts()); err == nil || !strings.Contains(err.Error(), "no durable exact-SHA intent") {
		t.Fatalf("deletion without durable intent error = %v", err)
	}
	withIntent := result
	withIntent.DeleteIntentSHA = sha
	ctx = lifecycleGitContext(t, canonical, lifecycleGitReply{operation: "ls-remote", err: errors.New("branch unavailable")})
	if err := worktreeretire.DeleteOriginal(ctx, &withIntent, nil, retireTransactionPorts()); err == nil || !strings.Contains(err.Error(), "branch unavailable") {
		t.Fatalf("failed source observation error = %v", err)
	}
	ctx = lifecycleGitContext(t, canonical,
		lifecycleGitReply{operation: "ls-remote", output: sha + "\t" + branchRef + "\n"},
		lifecycleGitReply{operation: "ls-remote", err: errors.New("proof unavailable")},
	)
	if err := worktreeretire.DeleteOriginal(ctx, &withIntent, nil, retireTransactionPorts()); err == nil || !strings.Contains(err.Error(), "proof unavailable") {
		t.Fatalf("failed proof observation error = %v", err)
	}

	ctx = lifecycleGitContext(t, "/fixture/archive", lifecycleGitReply{operation: "fetch", err: errors.New("archive unavailable")})
	if err := worktreeretire.VerifyArchive(ctx, "/fixture/archive", "remote", "refs/heads/archive", sha, retireArchiveManifest{}, retireArchivePorts()); err == nil || !strings.Contains(err.Error(), "archive unavailable") {
		t.Fatalf("archive verification error = %v", err)
	}
	ctx = lifecycleGitContext(t, "/fixture/archive",
		lifecycleGitReply{operation: "fetch"},
		lifecycleGitReply{operation: "rev-parse", output: sha + "\n"},
		lifecycleGitReply{operation: "cat-file", output: "not-a-size\n"},
	)
	if err := worktreeretire.VerifyArchive(ctx, "/fixture/archive", "remote", "refs/heads/archive", sha, retireArchiveManifest{}, retireArchivePorts()); err == nil || !strings.Contains(err.Error(), "verification limit") {
		t.Fatalf("invalid archive manifest size error = %v", err)
	}

	if committed, err := retireCommitSource(context.Background(), canonical, &cleanupWorktreeHandle{}, "message", nil); err == nil || committed {
		t.Fatalf("invalid held checkout commit = (%t, %v)", committed, err)
	}
	if err := retireRemoveLocal(context.Background(), &cleanupTaskHandle{}, ListResult{}, &result, nil); err == nil {
		t.Fatal("local removal without a held task was accepted")
	}
}

func TestRetirementArchiveManifestBoundaries(t *testing.T) {
	t.Parallel()

	expected := retireArchiveManifest{
		Version: 1, Repository: "acme/app", Branch: "source", Preserve: "branch",
		SourceSHA: strings.Repeat("a", 40), RetiredRef: "retired/source", ClaimID: "claim",
		Files: map[string]string{"b.txt": "digest-b", "a.txt": "digest-a"},
	}
	actual := expected
	actual.Files = map[string]string{"a.txt": "digest-a", "b.txt": "digest-b"}
	paths, err := worktreeretire.ValidateArchiveManifest(expected, actual)
	if err != nil || strings.Join(paths, ",") != "a.txt,b.txt" {
		t.Fatalf("validated manifest paths = %v, %v", paths, err)
	}
	if err := worktreeretire.ValidateArchiveTree(expected.Files, paths, []string{"a.txt", "b.txt", "retirement.json"}); err != nil {
		t.Fatalf("valid archive tree rejected: %v", err)
	}

	changedIdentity := actual
	changedIdentity.Repository = "acme/other"
	if _, err := worktreeretire.ValidateArchiveManifest(expected, changedIdentity); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("changed manifest identity error = %v", err)
	}
	changedDigest := actual
	changedDigest.Files = map[string]string{"a.txt": "changed", "b.txt": "digest-b"}
	if _, err := worktreeretire.ValidateArchiveManifest(expected, changedDigest); err == nil || !strings.Contains(err.Error(), "file mismatch") {
		t.Fatalf("changed manifest digest error = %v", err)
	}
	if err := worktreeretire.ValidateArchiveTree(expected.Files, paths, []string{"a.txt", "retirement.json"}); err == nil || !strings.Contains(err.Error(), "unlisted files") {
		t.Fatalf("short archive tree error = %v", err)
	}
	if err := worktreeretire.ValidateArchiveTree(expected.Files, paths, []string{"a.txt", "extra.txt", "retirement.json"}); err == nil || !strings.Contains(err.Error(), "unlisted file extra.txt") {
		t.Fatalf("extra archive tree error = %v", err)
	}
}

//nolint:paralleltest // setUpShellRetirementFixture isolates process-wide WB home variables.
func TestRetirementShellStructuralBoundaries(t *testing.T) {
	projectsRoot, worktreesRoot := setUpShellRetirementFixture(t)
	missingOwner := filepath.Join(worktreesRoot, "missing-owner")
	if empty, err := ownerDirectoryIsProvablyEmpty(missingOwner); err == nil || empty {
		t.Fatalf("missing owner namespace = %t, %v", empty, err)
	}
	ownerFile := filepath.Join(worktreesRoot, "owner-file")
	if err := os.WriteFile(ownerFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if empty, err := ownerDirectoryIsProvablyEmpty(ownerFile); err != nil || empty {
		t.Fatalf("owner file = %t, %v", empty, err)
	}
	if err := os.Remove(ownerFile); err != nil {
		t.Fatal(err)
	}
	ownerWithFile := filepath.Join(worktreesRoot, "owner-with-file")
	if err := os.Mkdir(ownerWithFile, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownerWithFile, "repository"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if empty, err := ownerDirectoryIsProvablyEmpty(ownerWithFile); err != nil || empty {
		t.Fatalf("owner namespace with file = %t, %v", empty, err)
	}
	if err := os.RemoveAll(ownerWithFile); err != nil {
		t.Fatal(err)
	}

	retiredLockTask := filepath.Join(worktreesRoot, "retired-lock-directory")
	if err := os.MkdirAll(filepath.Join(retiredLockTask, ".wb-retired-lock-invalid"), 0o700); err != nil {
		t.Fatal(err)
	}
	if eligible, reason := taskShellIsEmpty(retiredLockTask, "retired-lock-directory", false); eligible || !strings.Contains(reason, "unexpected retired-lock entry") {
		t.Fatalf("retired lock directory = %t, %q", eligible, reason)
	}
	if err := os.RemoveAll(retiredLockTask); err != nil {
		t.Fatal(err)
	}
	fileTask := filepath.Join(worktreesRoot, "ordinary-file")
	if err := os.Mkdir(fileTask, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fileTask, "evidence"), []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	if eligible, reason := taskShellIsEmpty(fileTask, "ordinary-file", false); eligible || !strings.Contains(reason, "unexpected non-directory entry") {
		t.Fatalf("ordinary task file = %t, %q", eligible, reason)
	}
	if err := os.RemoveAll(fileTask); err != nil {
		t.Fatal(err)
	}

	taskPath := filepath.Join(worktreesRoot, "retire-shell")
	if eligible, _ := taskShellIsEmpty(taskPath, "retire-shell", false); eligible {
		t.Fatal("missing task shell was eligible")
	}
	if err := os.WriteFile(taskPath, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if eligible, _ := taskShellIsEmpty(taskPath, "retire-shell", false); eligible {
		t.Fatal("regular file task shell was eligible")
	}
	if err := os.Remove(taskPath); err != nil {
		t.Fatal(err)
	}
	repositoryPath := filepath.Join(taskPath, "acme", "app")
	if err := os.MkdirAll(repositoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if empty, err := ownerDirectoryIsProvablyEmpty(filepath.Join(taskPath, "acme")); err != nil || !empty {
		t.Fatalf("empty owner namespace = %t, %v", empty, err)
	}
	if err := os.WriteFile(filepath.Join(repositoryPath, ".git"), []byte("gitdir: elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if empty, err := ownerDirectoryIsProvablyEmpty(filepath.Join(taskPath, "acme")); err != nil || empty {
		t.Fatalf("nonempty owner namespace = %t, %v", empty, err)
	}
	if eligible, _ := taskShellIsEmpty(taskPath, "retire-shell", false); eligible {
		t.Fatal("task shell containing a checkout marker was eligible")
	}
	if err := os.Remove(filepath.Join(repositoryPath, ".git")); err != nil {
		t.Fatal(err)
	}

	result := inspectTaskShell(worktreesRoot, "retire-shell")
	if !result.Eligible {
		t.Fatalf("empty shell was not eligible: %#v", result)
	}
	applyTaskShellRetirement(&result)
	if !result.Applied || result.Error != "" {
		t.Fatalf("empty shell retirement = %#v", result)
	}
	if _, err := os.Stat(taskPath); !os.IsNotExist(err) {
		t.Fatalf("retired task shell remains: %v", err)
	}

	missing := RetiredShell{WorktreesRoot: worktreesRoot, Task: "missing", Path: filepath.Join(worktreesRoot, "missing")}
	applyTaskShellRetirement(&missing)
	if missing.Error == "" {
		t.Fatalf("missing task shell retirement = %#v", missing)
	}

	outcome, err := RetireTaskShells(context.Background(), RetireShellsOptions{ProjectsRoot: projectsRoot})
	if err != nil || len(outcome.Results) != 0 {
		t.Fatalf("empty shell sweep = %#v, %v", outcome, err)
	}
	foreignFile := filepath.Join(worktreesRoot, "foreign-file")
	if err := os.WriteFile(foreignFile, []byte("ignore"), 0o600); err != nil {
		t.Fatal(err)
	}
	outcome, err = RetireTaskShells(context.Background(), RetireShellsOptions{ProjectsRoot: projectsRoot})
	if err != nil || len(outcome.Results) != 0 {
		t.Fatalf("sweep with foreign file = %#v, %v", outcome, err)
	}
	if err := os.Remove(foreignFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(worktreesRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(worktreesRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RetireTaskShells(context.Background(), RetireShellsOptions{ProjectsRoot: projectsRoot}); err == nil || !strings.Contains(err.Error(), "read worktrees root") {
		t.Fatalf("non-directory worktrees root error = %v", err)
	}
}
