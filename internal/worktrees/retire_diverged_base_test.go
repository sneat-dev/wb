package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktreeclaims"
	"github.com/sneat-dev/wb/internal/worktreeretire"
)

// Deliberately reproduce an imported lane whose manifest pinned a newer main
// before its checkout was moved onto an older integration history.
func divergedRetirementFixture(t *testing.T) (*gitFixture, CreateResult, RetireOptions, string) {
	t.Helper()
	f := newGitFixture(t)
	older := gitTestOutput(t, f.canonical, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(f.canonical, "main-only.txt"), []byte("original base history\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, f.canonical, "add", "main-only.txt")
	gitTest(t, f.canonical, "commit", "-m", "advance main before lane creation")
	gitTest(t, f.canonical, "push", "origin", "main")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: f.projectsRoot, Operation: "diverged-preserve", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := created[0]
	gitTest(t, c.WorktreeDir, "reset", "--hard", older)
	if err := os.WriteFile(filepath.Join(c.WorktreeDir, "lane-only.txt"), []byte("unlanded source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, c.WorktreeDir, "add", "lane-only.txt")
	gitTest(t, c.WorktreeDir, "commit", "-m", "work on older integration")
	gitTest(t, c.WorktreeDir, "push", "-u", "origin", c.Branch)
	archive := filepath.Join(t.TempDir(), "archive.git")
	gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
	opts := RetireOptions{
		ProjectsRoot: f.projectsRoot, Task: "diverged-preserve", ArchiveRemote: archive,
		RemoteOwnership: retireAllowRemoteOwner,
		Inspect: func(_ context.Context, repo string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repo}, nil
		},
		OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
	}
	return f, c, opts, gitTestOutput(t, c.WorktreeDir, "rev-parse", "HEAD")
}

//nolint:paralleltest // The Git fixture configures process-local identity and paths.
func TestPreservationRetirementBacksUpDivergedBaseWithoutRewritingClaim(t *testing.T) {
	f, c, opts, head := divergedRetirementFixture(t)
	ctx := context.Background()
	projection, err := readWorkLogProjectionForReadOnlyClaim(c.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(f.home, "worklogs", projection.EffortID, "runs", projection.RunID, "claims", projection.ClaimID+".json")
	before, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	var claim workLogClaim
	if err := json.Unmarshal(before, &claim); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := activeWorkLogClaim(f.home, c.WorktreeDir); err == nil {
		t.Fatal("ordinary active claim proof accepted a diverged base")
	}
	planned, err := Retire(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if planned.Phase != "planned" || planned.SourceSHA != head {
		t.Fatalf("plan = %+v", planned)
	}
	if got := gitTestOutput(t, c.WorktreeDir, "rev-parse", "HEAD"); got != head {
		t.Fatal("dry run moved source")
	}
	if _, _, _, err := activeWorkLogClaim(f.home, c.WorktreeDir); err == nil {
		t.Fatal("dry run made bad claim usable")
	}
	opts.Apply = true
	// A failure after publishing both backups must leave the original ref and
	// checkout in place, and the same command must finish safely on retry.
	stop := errors.New("stop after both backup receipts")
	opts.afterPhase = func(phase string) error {
		if phase == "archive_published" {
			return stop
		}
		return nil
	}
	interrupted, err := Retire(ctx, opts)
	if !errors.Is(err, stop) {
		t.Fatalf("interruption: %+v %v", interrupted, err)
	}
	if got := gitTestOutput(t, f.canonical, "ls-remote", "origin", "refs/heads/"+c.Branch); !strings.HasPrefix(got, head+"\t") {
		t.Fatal("source deleted before final receipt checks")
	}
	opts.afterPhase = nil
	retired, err := Retire(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if retired.Phase != "complete" {
		t.Fatalf("retired: %+v", retired)
	}
	after, err := os.ReadFile(claimPath)
	if err != nil || string(before) != string(after) {
		t.Fatalf("immutable claim changed: %v", err)
	}
	if _, err := os.Stat(c.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("checkout remains: %v", err)
	}
	gitTest(t, f.canonical, "fetch", opts.ArchiveRemote, "refs/heads/"+retired.ArchiveRef)
	body, err := worktreeretire.GitBytes(ctx, f.canonical, "show", "FETCH_HEAD:history.bundle")
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "history.bundle")
	if err := os.WriteFile(bundle, body, 0o600); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored.git")
	gitTest(t, t.TempDir(), "clone", "--bare", bundle, restored)
	for ref, want := range map[string]string{"preserved-source": head, "preserved-base": claim.BaseSHA} {
		if got := gitTestOutput(t, restored, "rev-parse", "refs/heads/"+ref); got != want {
			t.Fatalf("restored %s = %s, want %s", ref, got, want)
		}
	}
	if got := gitTestOutput(t, restored, "show", "preserved-source:lane-only.txt"); got != "unlanded source" {
		t.Fatal("source backup lost content")
	}
	if got := gitTestOutput(t, restored, "show", "preserved-base:main-only.txt"); got != "original base history" {
		t.Fatal("base backup lost content")
	}
}

//nolint:paralleltest // The Git fixture configures process-local identity and paths.
func TestPreservationRetirementRejectsOtherClaimFailures(t *testing.T) {
	f, c, opts, head := divergedRetirementFixture(t)
	p, err := readWorkLogProjectionForReadOnlyClaim(c.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	claim, _, err := retireReadClaim(f.home, c.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	// An expected-head mismatch is never a base-only exception.
	if err := corroborateRetirementClaim(f.home, c.WorktreeDir, claim.BaseSHA, p, claim); err == nil {
		t.Fatal("wrong head accepted")
	}
	// A renamed live branch is likewise not preservation authority.
	gitTest(t, c.WorktreeDir, "branch", "-m", "unclaimed-name")
	opts.Apply = true
	if _, err := Retire(context.Background(), opts); err == nil {
		t.Fatal("renamed branch was retired")
	}
	if got := gitTestOutput(t, c.WorktreeDir, "rev-parse", "HEAD"); got != head {
		t.Fatal("failed preservation moved source")
	}
	// A real Git error remains an error, even if it arose in the base proof.
	gitTest(t, c.WorktreeDir, "branch", "-m", claim.Branch)
	// Delete only the disposable fixture's loose base object; the lane is an
	// independent child of the older commit and therefore remains readable.
	object := filepath.Join(f.canonical, ".git", "objects", claim.BaseSHA[:2], claim.BaseSHA[2:])
	if err := os.Remove(object); err != nil {
		t.Fatal(err)
	}
	if err := corroborateRetirementClaim(f.home, c.WorktreeDir, head, p, claim); err == nil {
		t.Fatal("missing base object accepted as divergence")
	}
	if _, err := Retire(context.Background(), opts); err == nil {
		t.Fatal("missing base object was retired")
	}
}

//nolint:paralleltest // The Git fixture configures process-local identity and paths.
func TestPreservationResumesAnArchivePublishedBeforeHistoryBundles(t *testing.T) {
	f, c, opts, _ := divergedRetirementFixture(t)
	ctx := context.Background()
	// Make this an ordinary ancestry-valid lane, as required by the legacy path.
	gitTest(t, c.WorktreeDir, "merge", "--no-edit", "origin/main")
	gitTest(t, c.WorktreeDir, "push", "origin", c.Branch)
	opts.Apply = true
	stop := errors.New("old process stopped before archive receipt")
	opts.afterPhase = func(phase string) error {
		if phase == "source_published" {
			return stop
		}
		return nil
	}
	interrupted, err := Retire(ctx, opts)
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	if err := sealRetirementWorkLog(f.home, c.WorktreeDir, interrupted.SourceSHA); err != nil {
		t.Fatal(err)
	}
	receipt := worktreeretire.Receipt{Task: interrupted.Task, Repository: interrupted.Repository, Branch: interrupted.Branch, Preserve: interrupted.Preserve, SourceSHA: interrupted.SourceSHA, RetiredRef: interrupted.RetiredRef, Worktree: interrupted.Worktree, Canonical: interrupted.Canonical, ArchiveRef: interrupted.ArchiveRef, ClaimID: interrupted.ClaimID, EffortID: interrupted.EffortID, RunID: interrupted.RunID}
	oldPorts := retireArchivePorts()
	readClaim := oldPorts.ReadClaim
	oldPorts.ReadClaim = func(home, worktree string) (worktreeretire.Claim, error) {
		claim, err := readClaim(home, worktree)
		claim.BaseSHA = ""
		return claim, err
	}
	if err := worktreeretire.PublishArchive(ctx, f.home, opts.ArchiveRemote, &receipt, oldPorts); err != nil {
		t.Fatal(err)
	}
	oldSHA := receipt.ArchiveSHA
	opts.afterPhase = nil
	retired, err := Retire(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if retired.Phase != "complete" || retired.ArchiveSHA != oldSHA {
		t.Fatalf("legacy archive changed: %+v", retired)
	}
}

//nolint:paralleltest // The Git fixture configures process-local identity and paths.
func TestPreservationProofRefusesUnavailableOrChangedAuthority(t *testing.T) {
	f, c, _, head := divergedRetirementFixture(t)
	p, err := readWorkLogProjectionForReadOnlyClaim(c.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := corroborateRetirementProjection(filepath.Join(t.TempDir(), "missing"), c.WorktreeDir, p); err == nil {
		t.Fatal("missing run accepted")
	}
	wrong := p
	wrong.ClaimID = strings.Repeat("a", 64)
	if err := corroborateRetirementProjection(f.home, c.WorktreeDir, wrong); err == nil {
		t.Fatal("missing claim accepted")
	}
	if err := corroborateRetirementProjection(f.home, filepath.Join(t.TempDir(), "missing"), p); err == nil {
		t.Fatal("missing checkout accepted")
	}
	if preservableBaseDiagnostic(f.home, ListDiagnostic{DiscoveryScope: "selected-proof", Path: filepath.Join(t.TempDir(), "missing")}) {
		t.Fatal("missing projection was a recoverable diagnostic")
	}
	cause := errors.New("git failure")
	wrapped := &claimBaseAncestryError{base: head, cause: cause}
	if !errors.Is(wrapped, cause) {
		t.Fatal("ancestry failure lost its Git error")
	}
	// A non-retirement terminal cannot acquire the preservation exception.
	ports := defaultRecycleSealPorts()
	ports.corroborate = corroborateRetirementClaim
	if err := ports.sealWorkLogForRecycleWithEvidence(f.home, c.WorktreeDir, head, "removed", worktreeclaims.TerminalEvidence{}); err != nil {
		t.Fatal(err)
	}
	if _, err := readRetirementTerminal(f.home, c.WorktreeDir); err == nil {
		t.Fatal("non-retirement seal accepted")
	}
}

func TestPreservationDiagnosticsKeepsUnrelatedFailures(t *testing.T) {
	t.Parallel()
	original := ListDiagnostic{DiscoveryScope: "selected-proof", Path: filepath.Join(t.TempDir(), "missing"), Message: "unreadable claim"}
	kept := preservationDiagnostics(t.TempDir(), []ListDiagnostic{original})
	if len(kept) != 1 || kept[0] != original {
		t.Fatalf("unrelated failure hidden: %+v", kept)
	}
}
