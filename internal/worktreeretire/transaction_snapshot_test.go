package worktreeretire

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
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

// TestReadReportSnapshotRejectsGrowthDuringRead proves the receipt bound is
// enforced on the actual read, not only on the stat: a file admitted as a small
// regular file that grows past the cap before the bounded read must be refused
// before JSON decoding, validation or digest acceptance.
func TestReadReportSnapshotRejectsGrowthDuringRead(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	receipt := snapshotTestReceipt(t, root)
	original, err := os.ReadFile(receipt.ReportPath)
	if err != nil {
		t.Fatal(err)
	}
	originalSum := sha256.Sum256(original)
	originalDigest := hex.EncodeToString(originalSum[:])
	statSeamRan, readSeamRan := false, false
	observedRead := -1
	ops := reportReadOps{stat: func(file *os.File) (os.FileInfo, error) {
		info, err := file.Stat()
		if err != nil {
			return nil, err
		}
		// Admission must observe the real, small, valid receipt before growth.
		if !info.Mode().IsRegular() || info.Size() > maxReportBytes {
			t.Fatalf("growth seam admitted %d bytes of mode %v", info.Size(), info.Mode())
		}
		statSeamRan = true
		writer, err := os.OpenFile(receipt.ReportPath, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			return nil, err
		}
		if _, err := writer.Write(bytes.Repeat([]byte("z"), maxReportBytes+2)); err != nil {
			_ = writer.Close()
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		grown, err := file.Stat()
		if err != nil {
			return nil, err
		}
		if !os.SameFile(info, grown) || grown.Size() <= maxReportBytes+1 {
			t.Fatalf("growth after admission = same inode %v, size %d", os.SameFile(info, grown), grown.Size())
		}
		return info, nil
	}, read: func(reader io.Reader) ([]byte, error) {
		readSeamRan = true
		body, err := io.ReadAll(reader)
		observedRead = len(body)
		return body, err
	}}
	snapshot, err := readReportSnapshotWithOps(receipt.ReportPath, ops)
	if !statSeamRan || !readSeamRan {
		t.Fatalf("growth seams executed = stat:%v read:%v", statSeamRan, readSeamRan)
	}
	if err == nil || !strings.Contains(err.Error(), "invalid retirement receipt file") {
		t.Fatalf("growth during read = %v, want bounded-read cap refusal", err)
	}
	if observedRead != maxReportBytes+1 {
		t.Fatalf("bounded read observed %d bytes, want the supplied limit %d", observedRead, maxReportBytes+1)
	}
	if snapshot.SHA256 != "" || len(snapshot.Bytes) != 0 || snapshot.Transaction != (Transaction{}) {
		t.Fatalf("refused growth accepted snapshot = %+v", snapshot)
	}
	// Restore the exact admitted bytes: the same small file passes the real
	// bounded reader, so the refusal above was the post-stat growth bound and
	// not admission or a broken read path.
	if err := os.WriteFile(receipt.ReportPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	control, err := ReadReportSnapshot(receipt.ReportPath)
	if err != nil {
		t.Fatalf("restored receipt snapshot = %v", err)
	}
	if control.SHA256 != originalDigest || control.Transaction.SourceSHA != receipt.SourceSHA || !bytes.Equal(control.Bytes, original) {
		t.Fatalf("restored receipt snapshot = %s, %q", control.SHA256, control.Transaction.SourceSHA)
	}
}
