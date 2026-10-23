package lifecyclehooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func hkCovWriteReceiptStream(t *testing.T, path string, receipts ...Receipt) {
	t.Helper()
	var builder strings.Builder
	for _, receipt := range receipts {
		builder.WriteString(hkCovMustJSON(t, receipt))
		builder.WriteString("\n")
	}
	hkCovWriteFile(t, path, builder.String(), 0o600)
}

func TestHkCovGCRejectsInvalidOptionsAndPaths(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if _, err := dispatcher.GC(GCOptions{Keep: -1}); err == nil || !strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("negative keep error=%v", err)
	}
	if _, err := dispatcher.GC(GCOptions{OlderThan: -time.Minute}); err == nil || !strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("negative age error=%v", err)
	}

	blocker := hkCovWriteFile(t, filepath.Join(t.TempDir(), "blocker"), "x", 0o600)
	broken := dispatcher
	broken.StateDir = filepath.Join(blocker, "state")
	if _, err := broken.GC(GCOptions{}); err == nil || !strings.Contains(err.Error(), "create lifecycle hook state") {
		t.Fatalf("state error=%v", err)
	}

	blockedReceipts := dispatcher
	blockedReceipts.ReceiptPath = filepath.Join(blocker, "receipts.jsonl")
	if _, err := blockedReceipts.GC(GCOptions{}); err == nil {
		t.Fatal("expected receipt parent failure")
	}
}

func TestHkCovGCLockAndStreamTrustFailures(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	hkCovLockFile(t, dispatcher.ReceiptPath+".lock")
	if _, err := dispatcher.GC(GCOptions{}); err == nil {
		t.Fatal("expected receipt lock failure")
	}
	if err := os.Remove(dispatcher.ReceiptPath + ".lock"); err != nil {
		t.Fatal(err)
	}

	symlinked, _ := hkCovEnv(t)
	target := hkCovWriteFile(t, filepath.Join(t.TempDir(), "stream.jsonl"), "", 0o600)
	if err := os.Symlink(target, symlinked.ReceiptPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := symlinked.GC(GCOptions{}); err == nil || !strings.Contains(err.Error(), "regular non-symlink") {
		t.Fatalf("symlinked stream error=%v", err)
	}
}

func TestHkCovGCProtectsKeptReceiptsAndUsesStartedAt(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	old := hkCovReceipt("old")
	old.FinishedAt = now.Add(-72 * time.Hour)
	startedOnly := hkCovReceipt("started-only")
	startedOnly.FinishedAt = time.Time{}
	startedOnly.StartedAt = now.Add(-72 * time.Hour)
	hkCovWriteReceiptStream(t, dispatcher.ReceiptPath, old, startedOnly)

	protected, err := dispatcher.GC(GCOptions{Keep: 100, OlderThan: 0, Now: now})
	if err != nil || len(protected.Candidates) != 0 || protected.Receipts != 2 {
		t.Fatalf("protected=%+v err=%v", protected, err)
	}

	pruned, err := dispatcher.GC(GCOptions{Keep: 0, OlderThan: time.Hour, Now: now})
	if err != nil || strings.Join(pruned.Candidates, ",") != "old,started-only" {
		t.Fatalf("pruned=%+v err=%v", pruned, err)
	}
}

func TestHkCovGCReportsLongReceiptIDStatFailure(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	long := hkCovReceipt(strings.Repeat("x", 300))
	hkCovWriteReceiptStream(t, dispatcher.ReceiptPath, long)
	if _, err := dispatcher.GC(GCOptions{Keep: 0, OlderThan: time.Hour}); err == nil {
		t.Fatal("expected stat failure for an over-long receipt ID")
	}
}

func TestHkCovGCApplySurfacesIndexRemovalFailure(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	receipt := hkCovReceipt("blocked-index")
	receipt.FinishedAt = now.Add(-72 * time.Hour)
	hkCovWriteReceiptStream(t, dispatcher.ReceiptPath, receipt)
	indexPath := filepath.Join(receiptIndexDir(dispatcher.ReceiptPath), receipt.ID+".json")
	if err := os.MkdirAll(filepath.Join(indexPath, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.GC(GCOptions{Apply: true, Keep: 0, OlderThan: time.Hour, Now: now}); err == nil {
		t.Fatal("expected index removal failure")
	}
}

func TestHkCovGCApplyRejectsDiagnosticsOutsideReceiptDirectory(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	receipt := hkCovReceipt("escaped-diagnostics")
	receipt.FinishedAt = now.Add(-72 * time.Hour)
	receipt.StdoutPath = filepath.Join(t.TempDir(), "outside.log")
	hkCovWriteReceiptStream(t, dispatcher.ReceiptPath, receipt)
	if _, err := dispatcher.GC(GCOptions{Apply: true, Keep: 0, OlderThan: time.Hour, Now: now}); err == nil || !strings.Contains(err.Error(), "refuse diagnostic path outside receipt directory") {
		t.Fatalf("error=%v", err)
	}
}

func TestHkCovGCApplyRemovesIndexAndDiagnostics(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	receipt := hkCovReceipt("removable")
	receipt.FinishedAt = now.Add(-72 * time.Hour)
	directory := filepath.Join(dispatcher.diagnosticsDir(), receipt.ID)
	receipt.StdoutPath = filepath.Join(directory, "stdout.log")
	receipt.StderrPath = filepath.Join(directory, "stderr.log")
	hkCovWriteReceiptStream(t, dispatcher.ReceiptPath, receipt)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	hkCovWriteFile(t, receipt.StdoutPath, "out", 0o600)
	hkCovWriteFile(t, receipt.StderrPath, "err", 0o600)
	hkCovWriteFile(t, filepath.Join(receiptIndexDir(dispatcher.ReceiptPath), receipt.ID+".json"), "{}", 0o600)

	report, err := dispatcher.GC(GCOptions{Apply: true, Keep: 0, OlderThan: time.Hour, Now: now})
	if err != nil || report.RemovedReceipts != 1 || report.RemovedDiagnostics != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("diagnostics still present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(receiptIndexDir(dispatcher.ReceiptPath), receipt.ID+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("index still present: %v", err)
	}
}

func TestHkCovRemoveReceiptDiagnosticsReportsMissingDirectory(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	receipt := hkCovReceipt("no-diagnostics")
	receipt.StdoutPath = filepath.Join(dispatcher.diagnosticsDir(), receipt.ID, "stdout.log")
	removed, err := dispatcher.removeReceiptDiagnostics(receipt)
	if err != nil || removed {
		t.Fatalf("removed=%t err=%v", removed, err)
	}
}

func TestHkCovRemoveReceiptDiagnosticsReportsStatFailure(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	receipt := hkCovReceipt(strings.Repeat("y", 300))
	if _, err := dispatcher.removeReceiptDiagnostics(receipt); err == nil {
		t.Fatal("expected stat failure for an over-long receipt ID")
	}
}

func TestHkCovReadReceiptRecordsMissingFile(t *testing.T) {
	t.Parallel()
	records, findings, err := readReceiptRecords(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil || records != nil || findings != nil {
		t.Fatalf("records=%v findings=%v err=%v", records, findings, err)
	}
}

func TestHkCovReadReceiptRecordsKeepsInvalidLines(t *testing.T) {
	t.Parallel()
	path := hkCovWriteFile(t, filepath.Join(t.TempDir(), "receipts.jsonl"), hkCovMustJSON(t, hkCovReceipt("good"))+"\nnot-json\n", 0o600)
	records, findings, err := readReceiptRecords(path)
	if err != nil || len(records) != 2 || len(findings) != 1 || !strings.Contains(findings[0], "preserved invalid lifecycle hook receipt line 2") {
		t.Fatalf("records=%v findings=%v err=%v", records, findings, err)
	}
	if !records[0].valid || records[1].valid {
		t.Fatalf("validity flags=%t,%t", records[0].valid, records[1].valid)
	}
}

func TestHkCovRewriteReceiptRecordsFailures(t *testing.T) {
	t.Parallel()
	blocker := hkCovWriteFile(t, filepath.Join(t.TempDir(), "blocker"), "x", 0o600)
	if err := rewriteReceiptRecords(filepath.Join(blocker, "receipts.jsonl"), nil); err == nil {
		t.Fatal("expected MkdirAll failure")
	}

	destination := filepath.Join(t.TempDir(), "receipts.jsonl")
	if err := os.MkdirAll(filepath.Join(destination, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := rewriteReceiptRecords(destination, nil); err == nil {
		t.Fatal("expected rename failure onto a non-empty directory")
	}
}

func TestHkCovRewriteReceiptRecordsDropsRemovedLines(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipts.jsonl")
	records := []receiptRecord{
		{raw: []byte(`{"id":"keep"}`), valid: true},
		{raw: []byte(`{"id":"drop"}`), valid: true, remove: true},
		{raw: []byte(`not-json`), valid: false},
	}
	if err := rewriteReceiptRecords(path, records); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"keep"`) || strings.Contains(string(raw), `"drop"`) || !strings.Contains(string(raw), "not-json") {
		t.Fatalf("receipt stream=%q", raw)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("info=%v err=%v", info, err)
	}
}

func TestHkCovGCWithoutReceiptStreamIsNoOp(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	report, err := dispatcher.GC(GCOptions{})
	if err != nil || report.Receipts != 0 || len(report.Candidates) != 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}
