package fleetsync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/testenv"
)

func TestRPCovStatusStringNamesEveryConstant(t *testing.T) {
	for status, want := range map[Status]string{
		Cloned:                     "cloned",
		Pulled:                     "pulled",
		SkippedDirty:               "skipped (dirty)",
		RemovedArchived:            "removed archived",
		KeptArchived:               "kept archived",
		AbsentArchived:             "archived, absent",
		NoOp:                       "noop",
		Failed:                     "failed",
		SkippedIgnored:             "skipped (ignored)",
		EmptyRemote:                "empty remote",
		Diverged:                   "diverged",
		NoUpstream:                 "no upstream",
		Unpushed:                   "unpushed commits",
		ArchivedUnlandable:         "archived, holds unpushed commits",
		RepositoryTransferred:      "repository transferred",
		RepositoryTransferRequired: "repository transfer required",
	} {
		if got := status.String(); got != want {
			t.Errorf("Status(%d).String() = %q, want %q", int(status), got, want)
		}
	}
	if got := Status(99).String(); got != "unknown" {
		t.Errorf("unknown Status.String() = %q, want %q", got, "unknown")
	}
}

func TestRPCovPullSummaryDescribesEveryPullState(t *testing.T) {
	for _, test := range []struct {
		name   string
		result Result
		want   string
	}{
		{"planned", Result{PullPlanned: true}, "planned (dry-run)"},
		{"updated", Result{PullAttempted: true, PullSucceeded: true, Updated: true}, "updated from remote"},
		{"current", Result{PullAttempted: true, PullSucceeded: true}, "already current"},
		{"failed", Result{PullAttempted: true}, "failed"},
		{"unrelated", Result{Status: Cloned}, ""},
	} {
		if got := test.result.PullSummary(); got != test.want {
			t.Errorf("%s: PullSummary() = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestRPCovSyncReportsTransferPreparationFailure(t *testing.T) {
	repo := discover.Repo{Org: "newco", Name: "renamed", Remote: true, TransferFrom: "oldco/app", TransferError: "repository transfer lookup failed"}
	res := Sync(context.Background(), repo, t.TempDir(), false, false)
	if res.Status != Failed || res.Err == nil || !strings.Contains(res.Err.Error(), "transfer lookup failed") {
		t.Fatalf("result = %+v, want a Failed transfer-preparation result", res)
	}
}

func TestRPCovSyncReportsUnidentifiableDestinationRemote(t *testing.T) {
	repo := discover.Repo{Org: "newco", Name: "renamed", Remote: true, TransferFrom: "oldco/app", CloneURL: "not a remote url"}
	res := Sync(context.Background(), repo, t.TempDir(), false, false)
	if res.Status != Failed || res.Err == nil || !strings.Contains(res.Err.Error(), "destination remote") {
		t.Fatalf("result = %+v, want a Failed relocation with a remote-identity error", res)
	}
}

// rpCovTransferFixture builds a local, network-free repository rename: a bare
// remote published at oldco/app.git that is renamed to newco/renamed.git while
// the canonical clone still records the old URL, exactly the shape
// discover.Repo.TransferFrom describes.
func rpCovTransferFixture(t *testing.T) (discover.Repo, string, string, string) {
	t.Helper()
	t.Setenv("WB_HOME", t.TempDir())
	remotesRoot := rpCovResolvedTempDir(t)
	oldRemote := filepath.Join(remotesRoot, "oldco", "app.git")
	newRemote := filepath.Join(remotesRoot, "newco", "renamed.git")

	seed := filepath.Join(rpCovResolvedTempDir(t), "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "init", "-q", "-b", "main")
	write(t, seed, "f.txt", "v1\n")
	git(t, seed, "add", "-A")
	git(t, seed, "commit", "-qm", "v1")
	if err := os.MkdirAll(filepath.Dir(oldRemote), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, remotesRoot, "clone", "-q", "--bare", seed, oldRemote)
	testenv.ConfigureGitAutoMaintenanceOff(t, oldRemote)

	projectsRoot := rpCovResolvedTempDir(t)
	sourceDir := filepath.Join(projectsRoot, "oldco", "app")
	if err := os.MkdirAll(filepath.Dir(sourceDir), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, filepath.Dir(sourceDir), "clone", "-q", oldRemote, sourceDir)

	if err := os.MkdirAll(filepath.Dir(newRemote), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldRemote, newRemote); err != nil {
		t.Fatal(err)
	}

	repo := discover.Repo{
		Org: "newco", Name: "renamed", Path: sourceDir, Remote: true,
		TransferFrom: "oldco/app", CloneURL: newRemote, DefaultBranch: "main",
	}
	return repo, projectsRoot, sourceDir, filepath.Join(projectsRoot, "newco", "renamed")
}

// rpCovResolvedTempDir returns a symlink-free temporary directory: the
// relocation machinery refuses to operate on symlinked canonical paths, and on
// macOS /var is a symlink to /private/var.
func rpCovResolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRPCovSyncTransferRefusesDirtySourceCheckout(t *testing.T) {
	repo, projectsRoot, sourceDir, destinationDir := rpCovTransferFixture(t)
	write(t, sourceDir, "f.txt", "uncommitted local change\n")

	res := Sync(context.Background(), repo, projectsRoot, false, false)

	if res.Status != RepositoryTransferRequired || res.Err != nil {
		t.Fatalf("result = %+v, want RepositoryTransferRequired without a fault", res)
	}
	if !strings.Contains(res.Reason, "local changes") {
		t.Fatalf("reason = %q, want the dirty-worktree refusal from RelocateRepository", res.Reason)
	}
	if _, err := os.Stat(sourceDir); err != nil {
		t.Fatalf("dirty source clone was moved anyway: %v", err)
	}
	if _, err := os.Stat(destinationDir); !os.IsNotExist(err) {
		t.Fatalf("dirty source clone reached the destination: %v", err)
	}
}

func TestRPCovSyncTransferDryRunReportsThePlannedMove(t *testing.T) {
	repo, projectsRoot, sourceDir, destinationDir := rpCovTransferFixture(t)

	res := Sync(context.Background(), repo, projectsRoot, true, false)

	if res.Status != RepositoryTransferRequired || res.Err != nil {
		t.Fatalf("result = %+v, want the dry-run transfer requirement", res)
	}
	for _, want := range []string{"dry-run: move " + sourceDir, "update origin fetch URL", "verify origin/main exact SHA"} {
		if !strings.Contains(res.Reason, want) {
			t.Errorf("dry-run reason missing %q: %q", want, res.Reason)
		}
	}
	if res.RepositoryRelocation == nil || !res.RepositoryRelocation.Eligible || res.RepositoryRelocation.Applied {
		t.Fatalf("dry-run relocation = %+v", res.RepositoryRelocation)
	}
	if _, err := os.Stat(sourceDir); err != nil {
		t.Fatalf("dry run moved the source clone: %v", err)
	}
	if _, err := os.Stat(destinationDir); !os.IsNotExist(err) {
		t.Fatalf("dry run created the destination: %v", err)
	}
}

func TestRPCovSyncTransferDryRunExplainsQuarantiningADisposableDestination(t *testing.T) {
	repo, projectsRoot, _, destinationDir := rpCovTransferFixture(t)
	if err := os.MkdirAll(filepath.Dir(destinationDir), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, filepath.Dir(destinationDir), "clone", "-q", repo.CloneURL, destinationDir)

	res := Sync(context.Background(), repo, projectsRoot, true, false)

	if res.Status != RepositoryTransferRequired || res.Err != nil {
		t.Fatalf("result = %+v, want the dry-run transfer requirement", res)
	}
	if !strings.Contains(res.Reason, "temporarily quarantine the verified disposable destination clone") {
		t.Fatalf("dry-run reason does not explain the quarantined destination: %q", res.Reason)
	}
	if res.RepositoryRelocation == nil || res.RepositoryRelocation.RetiredDestinationDir == "" {
		t.Fatalf("relocation plan = %+v, want a quarantine path", res.RepositoryRelocation)
	}
}

func TestRPCovSyncTransferAppliesTheManagedRelocation(t *testing.T) {
	repo, projectsRoot, sourceDir, destinationDir := rpCovTransferFixture(t)

	res := Sync(context.Background(), repo, projectsRoot, false, false)

	if res.Status != RepositoryTransferred || res.Err != nil {
		t.Fatalf("result = %+v, want a completed repository transfer", res)
	}
	if res.Repo.Path != destinationDir {
		t.Fatalf("result path = %q, want the relocated destination %q", res.Repo.Path, destinationDir)
	}
	if res.HeadSHA == "" {
		t.Fatal("relocated result has no HEAD stamped; a report could not detect drift afterwards")
	}
	if _, err := os.Stat(sourceDir); !os.IsNotExist(err) {
		t.Fatalf("source clone remains after a completed transfer: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destinationDir, "f.txt")); err != nil {
		t.Fatalf("relocated clone is not intact at the destination: %v", err)
	}
	url, err := gitops.ConfiguredOriginURL(destinationDir)
	if err != nil || url != repo.CloneURL {
		t.Fatalf("relocated origin fetch URL = %q, err=%v, want %q", url, err, repo.CloneURL)
	}
}

func TestRPCovSyncArchivedPruneWithNoCloneReportsAbsent(t *testing.T) {
	installArchivedFakeGh(t)
	repo := discover.Repo{Org: "acme", Name: "widgets", Remote: true, Archived: true}
	res := Sync(context.Background(), repo, t.TempDir(), false, true)
	if res.Status != AbsentArchived {
		t.Fatalf("Status = %v (err=%v), want AbsentArchived", res.Status, res.Err)
	}
}

func TestRPCovSyncCloneFailureIsReportedAsFailed(t *testing.T) {
	repo := discover.Repo{
		Org: "acme", Name: "widgets", Remote: true,
		CloneURL: filepath.Join(t.TempDir(), "no-such-remote.git"),
	}
	res := Sync(context.Background(), repo, t.TempDir(), false, false)
	if res.Status != Failed || res.Err == nil {
		t.Fatalf("result = %+v, want a Failed clone", res)
	}
	if _, err := os.Stat(filepath.Join(res.Repo.Path, ".git")); err == nil && res.Repo.Path != "" {
		t.Fatalf("failed clone left a checkout at %s", res.Repo.Path)
	}
}

func TestRPCovSyncWorkingTreeInspectionFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "f.txt", "v1\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "v1")
	// A corrupt index makes `git status` fail while `git config` (the
	// skip-sync marker probe that runs first) still succeeds.
	if err := os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("not an index\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repo := discover.Repo{Org: "acme", Name: "widgets", Path: dir, Remote: true}
	res := Sync(context.Background(), repo, "", false, false)

	if res.Status != Failed || res.Err == nil {
		t.Fatalf("result = %+v, want a Failed working-tree inspection", res)
	}
	if !strings.Contains(res.Err.Error(), "index") {
		t.Fatalf("error = %v, want git's index failure reproduced", res.Err)
	}
}

func TestRPCovSplitAttentionKeepsUnclassifiedAttentionAsDefect(t *testing.T) {
	// needsAttention only selects the five defect statuses or a benign
	// archived-not-pruned result, so this defensive arm is not reachable
	// through IssuesMarkdown; it must still keep an unexpected shape visible
	// rather than silently dropping it.
	defects, informational := splitAttention([]Result{
		{Repo: discover.Repo{Org: "o", Name: "dirty"}, Status: SkippedDirty, ArchivedNotPruned: true},
		{Repo: discover.Repo{Org: "o", Name: "fine"}, Status: Pulled, ArchivedNotPruned: true},
	})
	if len(defects) != 1 || defects[0].Repo.Name != "dirty" {
		t.Fatalf("defects = %+v, want the unclassified result kept visible", defects)
	}
	if len(informational) != 1 || informational[0].Repo.Name != "fine" {
		t.Fatalf("informational = %+v, want only the benign archived result", informational)
	}
}

func TestRPCovIssuesMarkdownRendersTransferRequiredEntry(t *testing.T) {
	results := []Result{{
		Repo:    discover.Repo{Org: "newco", Name: "renamed", Path: "/p/newco/renamed", TransferFrom: "oldco/app"},
		Status:  RepositoryTransferRequired,
		Reason:  "the canonical clone remains at its old path",
		HeadSHA: "abc123",
	}}
	got := IssuesMarkdown(testMeta(), results)
	for _, want := range []string{
		"### newco/renamed — repository transfer required",
		"- **Transfer:** `oldco/app` → `newco/renamed`",
		"the canonical clone remains at its old path",
		"git -C /p/newco/renamed remote get-url --all origin",
		"git -C /p/newco/renamed worktree list --porcelain",
		"Re-run `wb sync` after resolving the reported refusal",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q\n---\n%s", want, got)
		}
	}
}

func TestRPCovInspectCommandsIncludeTheDefaultStatusForm(t *testing.T) {
	commands := inspectCommands(Result{Repo: discover.Repo{Org: "o", Name: "r", Path: "/p/o/r"}, Status: Cloned})
	if len(commands) != 1 || commands[0] != "git -C /p/o/r status -sb" {
		t.Fatalf("default inspect commands = %v, want the plain status probe", commands)
	}
}

func TestRPCovResolveOptionsCoverTransferredAndDefaultShapes(t *testing.T) {
	transfer := resolveOptions(Result{Status: RepositoryTransferRequired, Repo: discover.Repo{Org: "o", Name: "r", Path: "/p/o/r"}})
	if len(transfer) != 1 || !strings.Contains(transfer[0], "managed repository relocation") {
		t.Fatalf("transfer resolve options = %v", transfer)
	}
	fallback := resolveOptions(Result{Status: Cloned, Repo: discover.Repo{Org: "o", Name: "r", Path: "/p/o/r"}})
	if len(fallback) != 1 || !strings.Contains(fallback[0], "wb worktree create") {
		t.Fatalf("default resolve options = %v", fallback)
	}
}

func TestRPCovRunMetaCompleteRequiresAnUnscopedFinishedRealRun(t *testing.T) {
	if !(RunMeta{}).Complete() {
		t.Fatal("a plain finished run must be able to speak for the fleet")
	}
	for _, meta := range []RunMeta{
		{Owners: []string{"acme"}},
		{Filter: "api"},
		{DryRun: true},
		{Discovered: 3, Scanned: 2},
	} {
		if meta.Complete() {
			t.Errorf("meta %+v reported Complete", meta)
		}
	}
}

func TestRPCovSummaryGroupByLabelReportsAMissingLabel(t *testing.T) {
	if _, ok := SummaryGroupByLabel(Summary(nil), "No such group"); ok {
		t.Fatal("SummaryGroupByLabel found a label that does not exist")
	}
}

func TestRPCovWriteRemovalReceiptReportsAnUnusableProjectsRoot(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("regular file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The receipt's state home derives from the projects root now, so an
	// unusable projects root is passed instead of an unusable WB_HOME.
	if _, err := writeRemovalReceipt(filepath.Join(blocker, "projects"), RemovalReceipt{Repository: "o/r", CreatedAt: time.Now().UTC()}); err == nil {
		t.Fatal("writeRemovalReceipt accepted a projects root it cannot create")
	}
}

func TestRPCovWriteRemovalReceiptReportsAnUnwritableReceiptDirectory(t *testing.T) {
	home := receiptHome(t)
	directory := filepath.Join(home, "reports", "sync-prune-archived")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(directory, 0o700) }()

	if _, err := writeRemovalReceipt("/p", RemovalReceipt{Repository: "o/r", CreatedAt: time.Now().UTC()}); err == nil {
		t.Fatal("writeRemovalReceipt reported success into a directory it cannot write")
	}
}

func TestRPCovOverwriteRemovalReceiptReportsMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "receipt.json")
	err := overwriteRemovalReceipt(path, RemovalReceipt{Repository: "o/r"})
	if err == nil || !strings.Contains(err.Error(), "stage prune receipt") {
		t.Fatalf("error = %v, want the staging refusal", err)
	}
}

func TestRPCovOverwriteRemovalReceiptReportsRenameRefusal(t *testing.T) {
	// The target is a non-empty directory, so the atomic rename cannot replace
	// it: the receipt must be reported as unwritten rather than half-published.
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "occupied"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := overwriteRemovalReceipt(path, RemovalReceipt{Repository: "o/r"})
	if err == nil || !strings.Contains(err.Error(), "replace") {
		t.Fatalf("error = %v, want the replacement refusal", err)
	}
}

func TestRPCovRemovalReceiptNameIsStablePerRepositoryAndTime(t *testing.T) {
	created := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	receipt := RemovalReceipt{Repository: "owner/old-repo", CreatedAt: created}
	first := removalReceiptName(receipt)
	if first != removalReceiptName(receipt) {
		t.Fatalf("removalReceiptName is not deterministic: %q", first)
	}
	if !strings.Contains(first, "owner--old-repo") || !strings.HasPrefix(first, "20260902T120000.000000000Z-") {
		t.Fatalf("removalReceiptName = %q", first)
	}
}
