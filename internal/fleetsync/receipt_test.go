package fleetsync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/gitops"
)

func receiptHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_HOME", home)
	return home
}

func TestWriteRemovalReceiptRecordsWhatWasRemoved(t *testing.T) {
	home := receiptHome(t)
	created := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	path, err := writeRemovalReceipt("/p", RemovalReceipt{
		SchemaVersion: removalReceiptSchemaVersion,
		Phase:         PhasePlanned,
		Repository:    "owner/old-repo",
		ClonePath:     "/p/owner/old-repo",
		HeadSHA:       "deadbeef",
		Reason:        "archived and clean",
		CreatedAt:     created,
	})
	if err != nil {
		t.Fatalf("write receipt: %v", err)
	}
	if want := filepath.Join(home, "reports", "sync-prune-archived"); !strings.HasPrefix(path, want) {
		t.Errorf("receipt at %s, want under %s", path, want)
	}
	if !strings.Contains(filepath.Base(path), "owner--old-repo") {
		t.Errorf("receipt name must identify the repository: %s", filepath.Base(path))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("receipt mode = %04o, want 0600", info.Mode().Perm())
	}
	var back RemovalReceipt
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("receipt is not valid JSON: %v", err)
	}
	if back.HeadSHA != "deadbeef" || back.Repository != "owner/old-repo" || back.Phase != PhasePlanned {
		t.Errorf("receipt round-trip lost data: %+v", back)
	}
}

func TestRemovalReceiptPhaseUpdateRewritesTheSameFile(t *testing.T) {
	home := receiptHome(t)
	created := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	receipt := RemovalReceipt{
		SchemaVersion: removalReceiptSchemaVersion, Phase: PhasePlanned,
		Repository: "owner/r", ClonePath: "/p/owner/r", CreatedAt: created,
	}
	path, err := writeRemovalReceipt("/p", receipt)
	if err != nil {
		t.Fatal(err)
	}
	removed := created.Add(time.Second)
	receipt.Phase, receipt.RemovedAt = PhaseRemoved, &removed
	if err := overwriteRemovalReceipt(path, receipt); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, "reports", "sync-prune-archived"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("phase update produced %d files, want 1 rewritten in place", len(entries))
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"phase": "removed"`) {
		t.Errorf("phase not updated:\n%s", raw)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".prune-receipt-") {
			t.Errorf("temporary receipt left behind: %s", e.Name())
		}
	}
}

// TestSyncPruneWritesAReceiptBeforeRemoving is the point of the whole file:
// wb sync --prune-archived used to delete a canonical clone and leave only a
// terminal line behind.
func TestSyncPruneWritesAReceiptBeforeRemoving(t *testing.T) {
	installArchivedFakeGh(t)
	home := receiptHome(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "f.txt", "v1\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "v1")
	remote := t.TempDir()
	git(t, remote, "init", "-q", "--bare", "-b", "main")
	git(t, dir, "remote", "add", "origin", remote)
	git(t, dir, "push", "-q", "origin", "main")
	head, err := gitops.HeadSHA(dir)
	if err != nil {
		t.Fatal(err)
	}

	repo := discover.Repo{Org: "acme", Name: "widgets", Path: dir, Remote: true, Archived: true}
	res := Sync(context.Background(), repo, "", false, true)

	if res.Status != RemovedArchived {
		t.Fatalf("Status = %v, want RemovedArchived (err=%v)", res.Status, res.Err)
	}
	if res.ReceiptPath == "" {
		t.Fatal("a removal with no receipt path is a removal with no evidence")
	}
	raw, readErr := os.ReadFile(res.ReceiptPath)
	if readErr != nil {
		t.Fatalf("receipt unreadable: %v", readErr)
	}
	var receipt RemovalReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatalf("receipt is not valid JSON: %v", err)
	}
	if receipt.Phase != PhaseRemoved {
		t.Errorf("phase = %q, want %q", receipt.Phase, PhaseRemoved)
	}
	if receipt.HeadSHA != head {
		t.Errorf("HeadSHA = %q, want the commit the clone stood at (%q) — without it the deletion cannot be undone", receipt.HeadSHA, head)
	}
	if receipt.Repository != "acme/widgets" || receipt.ClonePath != dir {
		t.Errorf("receipt does not identify what was removed: %+v", receipt)
	}
	if receipt.Reason == "" {
		t.Error("receipt must record why the clone was eligible")
	}
	if receipt.RemovedAt == nil {
		t.Error("a removed receipt must record when")
	}
	if !strings.HasPrefix(res.ReceiptPath, filepath.Join(home, "reports", "sync-prune-archived")) {
		t.Errorf("receipt written outside the reports directory: %s", res.ReceiptPath)
	}
}

// TestSyncPruneRefusesToRemoveWhenTheReceiptCannotBeWritten pins the safety
// rule: no evidence, no deletion. This is the one WB operation that cannot be
// undone from local state.
func TestSyncPruneRefusesToRemoveWhenTheReceiptCannotBeWritten(t *testing.T) {
	installArchivedFakeGh(t)
	home := receiptHome(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "f.txt", "v1\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "v1")
	remote := t.TempDir()
	git(t, remote, "init", "-q", "--bare", "-b", "main")
	git(t, dir, "remote", "add", "origin", remote)
	git(t, dir, "push", "-q", "origin", "main")

	// A plain file where the receipt directory must go: MkdirAll fails, so the
	// receipt cannot be written.
	if err := os.MkdirAll(filepath.Join(home, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "reports", "sync-prune-archived"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}

	repo := discover.Repo{Org: "acme", Name: "widgets", Path: dir, Remote: true, Archived: true}
	res := Sync(context.Background(), repo, "", false, true)

	if res.Status != Failed {
		t.Fatalf("Status = %v, want Failed: an unwritable receipt must block the deletion", res.Status)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the clone was removed despite having no receipt: %v", err)
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "receipt could not be written") {
		t.Errorf("error must explain why nothing was removed: %v", res.Err)
	}
}
