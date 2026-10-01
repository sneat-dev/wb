//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // newGitFixture configures process-wide Git and WB environment.
func TestE2ECloneMoveBlockedDestinationParentPreservesSource(t *testing.T) {
	fixture := newGitFixture(t)
	before := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	blocker := filepath.Join(t.TempDir(), "parent-file")
	if err := os.WriteFile(blocker, []byte("keep parent bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(blocker, "clone")
	if _, err := ApplyCloneMove(context.Background(), fixture.canonical, destination); err == nil || !strings.Contains(err.Error(), "prepare destination parent directory") {
		t.Fatalf("blocked destination parent error = %v", err)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != before {
		t.Fatalf("source HEAD changed from %s to %s", before, got)
	}
	if got, err := os.ReadFile(blocker); err != nil || string(got) != "keep parent bytes" {
		t.Fatalf("destination blocker changed: %q, %v", got, err)
	}
	if _, err := os.Lstat(destination); err == nil {
		t.Fatalf("destination appeared after refusal: %v", err)
	}
}

//nolint:paralleltest // newGitFixture configures process-wide Git and WB environment.
func TestE2ECloneMoveRestoresSourceWhenLinkedCheckoutCannotVerify(t *testing.T) {
	fixture := newGitFixture(t)
	linked := filepath.Join(t.TempDir(), "linked")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "feature/linked", linked)
	admin := gitTestOutput(t, linked, "rev-parse", "--absolute-git-dir")
	if err := os.WriteFile(filepath.Join(admin, "index"), []byte("invalid index"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	destination := filepath.Join(t.TempDir(), "host", "acme", "app")
	if _, err := ApplyCloneMove(context.Background(), fixture.canonical, destination); err == nil || !strings.Contains(err.Error(), "git status failed") {
		t.Fatalf("unverifiable linked checkout move = %v", err)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != before {
		t.Fatalf("rollback changed canonical HEAD from %s to %s", before, got)
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("failed move left destination clone: %v", err)
	}
	common := gitTestOutput(t, linked, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if filepath.Clean(common) != filepath.Join(fixture.canonical, ".git") {
		t.Fatalf("rollback did not restore linked pointer: %q", common)
	}
}

//nolint:paralleltest // newGitFixture configures process-wide Git and WB environment.
func TestE2EClonePlacementVerificationRefusesChangedRegisteredCheckout(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*testing.T, string)
	}{
		{"missing-checkout", "worktree path missing after move", func(t *testing.T, path string) {
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
		}},
		{"broken-git-pointer", "resolve git common directory", func(t *testing.T, path string) {
			if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: /missing/linked-admin\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"foreign-common-directory", "common directory does not match", func(t *testing.T, path string) {
			other := filepath.Join(t.TempDir(), "other-clone")
			if err := os.Mkdir(other, 0o700); err != nil {
				t.Fatal(err)
			}
			gitTest(t, other, "init", "-b", "main")
			gitTest(t, other, "config", "user.email", "wb@example.test")
			gitTest(t, other, "config", "user.name", "WB Test")
			if err := os.WriteFile(filepath.Join(other, "README.md"), []byte("other\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			gitTest(t, other, "add", "README.md")
			gitTest(t, other, "commit", "-m", "other")
			otherLinked := filepath.Join(t.TempDir(), "other-linked")
			gitTest(t, other, "worktree", "add", "-b", "other-linked", otherLinked)
			admin := gitTestOutput(t, otherLinked, "rev-parse", "--absolute-git-dir")
			if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: "+admin+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupt-private-index", "git status failed", func(t *testing.T, path string) {
			admin := gitTestOutput(t, path, "rev-parse", "--absolute-git-dir")
			if err := os.WriteFile(filepath.Join(admin, "index"), []byte("invalid index"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		//nolint:paralleltest // each fixture sets process-wide environment.
		t.Run(tc.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			linked := filepath.Join(t.TempDir(), "linked")
			gitTest(t, fixture.canonical, "worktree", "add", "-b", "feature/linked", linked)
			before := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
			tc.change(t, linked)
			if err := VerifyClonePlacement(context.Background(), fixture.canonical, []string{linked}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("changed registered checkout verification = %v, want %q", err, tc.want)
			}
			if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != before {
				t.Fatalf("verification moved canonical HEAD from %s to %s", before, got)
			}
		})
	}
}

//nolint:paralleltest // newGitFixture configures process-wide Git and WB environment.
func TestE2ECloneReconciliationReportsUnrelatedRegistryDamageWithoutRepair(t *testing.T) {
	fixture := newGitFixture(t)
	missing := filepath.Join(t.TempDir(), "missing-linked")
	unreadable := filepath.Join(t.TempDir(), "unreadable-linked")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "feature/missing", missing)
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "feature/unreadable", unreadable)
	before := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	adminPaths := []string{
		gitTestOutput(t, missing, "rev-parse", "--absolute-git-dir"),
		gitTestOutput(t, unreadable, "rev-parse", "--absolute-git-dir"),
	}
	registryBytes := make([][]byte, len(adminPaths))
	for i, path := range adminPaths {
		var err error
		registryBytes[i], err = os.ReadFile(filepath.Join(path, "gitdir"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(missing); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(unreadable, ".git")); err != nil {
		t.Fatal(err)
	}
	registryBefore := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain")
	status, informational, err := ReconcileClonePlacement(context.Background(), fixture.canonical, "", false)
	if err != nil || status != "verified" || len(informational) != 2 || informational[0] != missing || informational[1] != unreadable {
		t.Fatalf("unrelated registry damage = (%q, %#v, %v)", status, informational, err)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != before {
		t.Fatalf("read-only reconciliation moved canonical HEAD from %s to %s", before, got)
	}
	if got := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain"); got != registryBefore {
		t.Fatalf("dry-run changed worktree registry:\nbefore %q\nafter %q", registryBefore, got)
	}
	for i, path := range adminPaths {
		if got, err := os.ReadFile(filepath.Join(path, "gitdir")); err != nil || string(got) != string(registryBytes[i]) {
			t.Fatalf("dry-run changed private registration %s: %q, %v", path, got, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(unreadable, ".git")); !os.IsNotExist(err) {
		t.Fatalf("dry-run repaired unreadable pointer: %v", err)
	}
}

func TestE2ECloneMoveIntentSelectionRefusesUnreadableProjectsRoot(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "projects-file")
	if err := os.WriteFile(blocker, []byte("keep projects bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordCloneMoveRelocationIntents(filepath.Join(blocker, "missing"), nil, time.Time{}); err == nil {
		t.Fatal("intent selection accepted a non-directory projects root")
	}
	if got, err := os.ReadFile(blocker); err != nil || string(got) != "keep projects bytes" {
		t.Fatalf("projects-root blocker changed: %q, %v", got, err)
	}
}

//nolint:paralleltest // newGitFixture configures process-wide Git and WB environment.
func TestE2ECloneMoveIntentAndReceiptRefuseChangedPrivateEvidence(t *testing.T) {
	fixture := newGitFixture(t)
	linked := filepath.Join(fixture.canonical, ".worktrees", "claimed")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "claimed", linked)
	head := gitTestOutput(t, linked, "rev-parse", "HEAD")
	_, err := recordWorkLogWithHooks(fixture.home, "claimed", CreateResult{
		Repository: "acme/app", WorktreeDir: linked, Branch: "claimed", Base: "main", BaseSHA: head,
	}, WorkLogOptions{
		EffortID: "clone-intent", RunID: "run", AgentID: "codex-1", Model: "unknown",
		TaskSummary: "Bind clone migration to an immutable claim", WBSessionID: "wbs-clone-intent",
	}, workLogPublicationHooks{})
	if err != nil {
		t.Fatal(err)
	}
	relocations := filepath.Join(fixture.home, "worklogs", "clone-intent", "runs", "run", "relocations")
	if err := os.WriteFile(relocations, []byte("blocking private evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	move := CloneMoveWorktree{Source: linked, Destination: filepath.Join(t.TempDir(), "moved"), Head: head}
	if _, err := RecordCloneMoveRelocationIntents(fixture.projectsRoot, []CloneMoveWorktree{move}, time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "record clone-move relocation intent") {
		t.Fatalf("blocked relocation intent publication = %v", err)
	}
	if got, err := os.ReadFile(relocations); err != nil || string(got) != "blocking private evidence" {
		t.Fatalf("private blocker changed: %q, %v", got, err)
	}
	if err := os.Remove(relocations); err != nil {
		t.Fatal(err)
	}
	entries, err := RecordCloneMoveRelocationIntents(fixture.projectsRoot, []CloneMoveWorktree{move}, time.Now().UTC())
	if err != nil || len(entries) != 1 {
		t.Fatalf("durable relocation intent = (%#v, %v)", entries, err)
	}
	intentPath := filepath.Join(relocations, relocationIntentName(entries[0].claim.ClaimID, entries[0].intent.OperationID))
	intentBytes, err := os.ReadFile(intentPath)
	if err != nil {
		t.Fatal(err)
	}
	entries[0].intent.HeadSHA = strings.Repeat("f", 40)
	if err := FinalizeCloneMoveRelocationReceipts(entries, time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "record clone-move relocation receipt") || !strings.Contains(err.Error(), "durable intent") {
		t.Fatalf("changed in-memory intent receipt refusal = %v", err)
	}
	if got, err := os.ReadFile(intentPath); err != nil || string(got) != string(intentBytes) {
		t.Fatalf("durable intent changed after refusal: %q, %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(relocations, relocationReceiptName(entries[0].claim.ClaimID, entries[0].intent.OperationID))); !os.IsNotExist(err) {
		t.Fatalf("receipt published after failed binding: %v", err)
	}
	if got := gitTestOutput(t, linked, "rev-parse", "HEAD"); got != head {
		t.Fatalf("failed relocation evidence changed checkout HEAD from %s to %s", head, got)
	}
}
