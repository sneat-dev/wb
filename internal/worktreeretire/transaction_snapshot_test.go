package worktreeretire

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktreebranches"
)

func snapshotTestReceipt(t *testing.T, root string) Transaction {
	t.Helper()
	result := Transaction{
		Version: 1, Task: "task", Repository: "acme/app", Branch: "topic",
		SourceSHA: strings.Repeat("a", 40), Phase: "complete",
	}
	result.RetiredRef = worktreebranches.RetiredBranchDestination(time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), result.Branch, result.SourceSHA)
	result.ArchiveRef = ArchiveRef(result)
	result.ReportPath = ReportPath(root, result)
	if err := WriteReport(result); err != nil {
		t.Fatal(err)
	}
	return result
}

// TestReadReportSnapshotBindsDigestToTheDecodedBytes proves that the digest
// returned with a decoded receipt is exactly the SHA-256 of the bytes that
// produced that transaction, so evidence cannot bind one file's digest while
// validating another file's contents.
func TestReadReportSnapshotBindsDigestToTheDecodedBytes(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	receipt := snapshotTestReceipt(t, root)
	body, err := os.ReadFile(receipt.ReportPath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	snapshot, err := ReadReportSnapshot(receipt.ReportPath)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("snapshot digest = %s, want sha256 of the exact decoded bytes", snapshot.SHA256)
	}
	if snapshot.Transaction.SourceSHA != receipt.SourceSHA || snapshot.Transaction.Phase != "complete" {
		t.Fatalf("snapshot transaction = %+v", snapshot.Transaction)
	}
}

// TestReadReportSnapshotRefusesSymlinkAndOversizeReplacements keeps the
// bounded, no-follow guarantees the old reader had: a symlinked receipt is
// never followed and an oversize replacement is refused before decode.
func TestReadReportSnapshotRefusesSymlinkAndOversizeReplacements(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	receipt := snapshotTestReceipt(t, root)
	alias := filepath.Join(root, "alias.json")
	if err := os.Symlink(receipt.ReportPath, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReportSnapshot(alias); err == nil {
		t.Fatal("symlinked receipt snapshot was followed")
	}
	oversize := filepath.Join(root, "oversize.json")
	if err := os.WriteFile(oversize, make([]byte, maxReportBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReportSnapshot(oversize); err == nil || !strings.Contains(err.Error(), "invalid retirement receipt file") {
		t.Fatalf("oversize receipt snapshot = %v", err)
	}
	parentLinked := filepath.Join(root, "linked-parent")
	if err := os.Symlink(filepath.Dir(receipt.ReportPath), parentLinked); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReportSnapshot(filepath.Join(parentLinked, filepath.Base(receipt.ReportPath))); err == nil {
		t.Fatal("receipt snapshot followed a symlinked parent directory")
	}
}
