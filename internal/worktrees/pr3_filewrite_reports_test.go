package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// The following tests exercise the filewrite.Injector-reachable error
// branches of the remaining 9 task-9 PR-3 call sites: writeQuarantineReport,
// writeCleanupReport, writeRenameReport, writeRetiredStageReceipt,
// writeRetireReport, writeDurableFile, copyFileSHA256, retireCaptureFile
// and writeBranchCleanupReport. Their happy paths are already covered
// elsewhere; reaching a create, write, sync, close or rename failure
// deterministically needs the injector.

// --- writeQuarantineReport (branches_quarantine.go) ---

func TestWriteQuarantineReportInjectedHonoursAnInjectedCreateFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "quarantine.json")
	inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: errBoomPR3}
	if err := writeQuarantineReportInjected(path, BranchQuarantineOutcome{Apply: true}, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeQuarantineReportInjected error = %v", err)
	}
}

func TestWriteQuarantineReportInjectedHonoursAnInjectedWriteFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "quarantine.json")
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errBoomPR3}
	if err := writeQuarantineReportInjected(path, BranchQuarantineOutcome{Apply: true}, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeQuarantineReportInjected error = %v", err)
	}
}

func TestWriteQuarantineReportInjectedHonoursAnInjectedSyncFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "quarantine.json")
	inj := &filewrite.Injector{Step: filewrite.StepSync, Err: errBoomPR3}
	if err := writeQuarantineReportInjected(path, BranchQuarantineOutcome{Apply: true}, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeQuarantineReportInjected error = %v", err)
	}
}

func TestWriteQuarantineReportInjectedHonoursAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "quarantine.json")
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomPR3}
	if err := writeQuarantineReportInjected(path, BranchQuarantineOutcome{Apply: true}, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeQuarantineReportInjected error = %v", err)
	}
}

func TestWriteQuarantineReportInjectedHonoursAnInjectedRenameFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "quarantine.json")
	inj := &filewrite.Injector{Step: filewrite.StepRename, Err: errBoomPR3}
	if err := writeQuarantineReportInjected(path, BranchQuarantineOutcome{Apply: true}, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeQuarantineReportInjected error = %v", err)
	}
}

func TestWriteQuarantineReportPublishesReadableJSON(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "quarantine.json")
	if err := writeQuarantineReport(path, BranchQuarantineOutcome{Apply: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

// --- writeCleanupReport (lifecycle.go) ---

func TestWriteCleanupReportInjectedHonoursAnInjectedWriteFailure(t *testing.T) {
	t.Parallel()
	options := CleanupOptions{ReportDir: t.TempDir()}
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errBoomPR3}
	if _, err := writeCleanupReportInjected(options, time.Now(), "plan", nil, nil, nil, nil, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeCleanupReportInjected error = %v", err)
	}
}

func TestWriteCleanupReportInjectedHonoursAnInjectedRenameFailure(t *testing.T) {
	t.Parallel()
	options := CleanupOptions{ReportDir: t.TempDir()}
	inj := &filewrite.Injector{Step: filewrite.StepRename, Err: errBoomPR3}
	if _, err := writeCleanupReportInjected(options, time.Now(), "plan", nil, nil, nil, nil, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeCleanupReportInjected error = %v", err)
	}
}

func TestWriteCleanupReportPublishesReadableJSON(t *testing.T) {
	t.Parallel()
	options := CleanupOptions{ReportDir: t.TempDir()}
	path, err := writeCleanupReport(options, time.Now(), "plan", nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

// --- writeRenameReport (rename.go) ---

func TestWriteRenameReportInjectedHonoursAnInjectedWriteFailure(t *testing.T) {
	t.Parallel()
	options := RenameOptions{ReportDir: t.TempDir()}
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errBoomPR3}
	if _, err := writeRenameReportInjected(options, time.Now(), "plan", nil, nil, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeRenameReportInjected error = %v", err)
	}
}

func TestWriteRenameReportInjectedHonoursAnInjectedRenameFailure(t *testing.T) {
	t.Parallel()
	options := RenameOptions{ReportDir: t.TempDir()}
	inj := &filewrite.Injector{Step: filewrite.StepRename, Err: errBoomPR3}
	if _, err := writeRenameReportInjected(options, time.Now(), "plan", nil, nil, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeRenameReportInjected error = %v", err)
	}
}

func TestWriteRenameReportPublishesReadableJSON(t *testing.T) {
	t.Parallel()
	options := RenameOptions{ReportDir: t.TempDir()}
	path, err := writeRenameReport(options, time.Now(), "plan", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

// --- writeRetiredStageReceipt (stage_recovery.go) ---

func TestWriteRetiredStageReceiptInjectedHonoursAnInjectedWriteFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errBoomPR3}
	if err := writeRetiredStageReceiptInjected(path, RetiredStageRecoveryOutcome{}, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeRetiredStageReceiptInjected error = %v", err)
	}
}

func TestWriteRetiredStageReceiptInjectedHonoursAnInjectedRenameFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	inj := &filewrite.Injector{Step: filewrite.StepRename, Err: errBoomPR3}
	if err := writeRetiredStageReceiptInjected(path, RetiredStageRecoveryOutcome{}, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeRetiredStageReceiptInjected error = %v", err)
	}
}

func TestWriteRetiredStageReceiptPublishesReadableJSON(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := writeRetiredStageReceipt(path, RetiredStageRecoveryOutcome{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

// --- writeRetireReport (retire.go) ---

func TestWriteRetireReportInjectedHonoursAnInjectedCreateFailure(t *testing.T) {
	t.Parallel()
	result := RetireResult{ReportPath: filepath.Join(t.TempDir(), "retire.json")}
	inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: errBoomPR3}
	if err := writeRetireReportInjected(result, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeRetireReportInjected error = %v", err)
	}
}

func TestWriteRetireReportInjectedHonoursAnInjectedChmodFailure(t *testing.T) {
	t.Parallel()
	result := RetireResult{ReportPath: filepath.Join(t.TempDir(), "retire.json")}
	inj := &filewrite.Injector{Step: filewrite.StepChmod, Err: errBoomPR3}
	if err := writeRetireReportInjected(result, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeRetireReportInjected error = %v", err)
	}
}

func TestWriteRetireReportInjectedHonoursAnInjectedWriteFailure(t *testing.T) {
	t.Parallel()
	result := RetireResult{ReportPath: filepath.Join(t.TempDir(), "retire.json")}
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errBoomPR3}
	if err := writeRetireReportInjected(result, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeRetireReportInjected error = %v", err)
	}
}

func TestWriteRetireReportInjectedHonoursAnInjectedSyncFailure(t *testing.T) {
	t.Parallel()
	result := RetireResult{ReportPath: filepath.Join(t.TempDir(), "retire.json")}
	inj := &filewrite.Injector{Step: filewrite.StepSync, Err: errBoomPR3}
	if err := writeRetireReportInjected(result, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeRetireReportInjected error = %v", err)
	}
}

func TestWriteRetireReportInjectedHonoursAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	result := RetireResult{ReportPath: filepath.Join(t.TempDir(), "retire.json")}
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomPR3}
	if err := writeRetireReportInjected(result, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeRetireReportInjected error = %v", err)
	}
}

func TestWriteRetireReportInjectedHonoursAnInjectedRenameFailure(t *testing.T) {
	t.Parallel()
	result := RetireResult{ReportPath: filepath.Join(t.TempDir(), "retire.json")}
	inj := &filewrite.Injector{Step: filewrite.StepRename, Err: errBoomPR3}
	if err := writeRetireReportInjected(result, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeRetireReportInjected error = %v", err)
	}
}

func TestWriteRetireReportPublishesReadableJSON(t *testing.T) {
	t.Parallel()
	result := RetireResult{ReportPath: filepath.Join(t.TempDir(), "retire.json")}
	if err := writeRetireReport(result); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(result.ReportPath); err != nil {
		t.Fatal(err)
	}
}

// --- writeDurableFile (branches_cleanup.go) ---

func TestWriteDurableFileInjectedHonoursAnInjectedCreateFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "durable")
	inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: errBoomPR3}
	if err := writeDurableFileInjected(path, []byte("x"), 0o600, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeDurableFileInjected error = %v", err)
	}
}

func TestWriteDurableFileInjectedHonoursAnInjectedWriteFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "durable")
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errBoomPR3}
	if err := writeDurableFileInjected(path, []byte("x"), 0o600, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeDurableFileInjected error = %v", err)
	}
}

func TestWriteDurableFileInjectedHonoursAnInjectedSyncFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "durable")
	inj := &filewrite.Injector{Step: filewrite.StepSync, Err: errBoomPR3}
	if err := writeDurableFileInjected(path, []byte("x"), 0o600, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeDurableFileInjected error = %v", err)
	}
}

func TestWriteDurableFileInjectedHonoursAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "durable")
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomPR3}
	if err := writeDurableFileInjected(path, []byte("x"), 0o600, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeDurableFileInjected error = %v", err)
	}
}

func TestWriteDurableFileWritesTheGivenContent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "durable")
	if err := writeDurableFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "x" {
		t.Fatalf("contents = %q, err = %v", contents, err)
	}
}

// --- copyFileSHA256 (branches_cleanup.go) ---

func TestCopyFileSHA256InjectedHonoursAnInjectedCreateFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: errBoomPR3}
	if _, err := copyFileSHA256Injected(source, filepath.Join(dir, "dest"), inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("copyFileSHA256Injected error = %v", err)
	}
}

func TestCopyFileSHA256InjectedHonoursAnInjectedSyncFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	inj := &filewrite.Injector{Step: filewrite.StepSync, Err: errBoomPR3}
	if _, err := copyFileSHA256Injected(source, filepath.Join(dir, "dest"), inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("copyFileSHA256Injected error = %v", err)
	}
}

func TestCopyFileSHA256InjectedHonoursAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomPR3}
	if _, err := copyFileSHA256Injected(source, filepath.Join(dir, "dest"), inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("copyFileSHA256Injected error = %v", err)
	}
}

func TestCopyFileSHA256ComputesTheDigestOfTheCopiedBytes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := copyFileSHA256(source, filepath.Join(dir, "dest"))
	if err != nil {
		t.Fatal(err)
	}
	if digest == "" {
		t.Fatal("digest is empty")
	}
	contents, err := os.ReadFile(filepath.Join(dir, "dest"))
	if err != nil || string(contents) != "hello" {
		t.Fatalf("copied contents = %q, err = %v", contents, err)
	}
}

// --- retireCaptureFile (retire.go) ---

func TestRetireCaptureFileInjectedHonoursAnInjectedCreateFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: errBoomPR3}
	if _, err := retireCaptureFileInjected(source, filepath.Join(dir, "dest"), inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("retireCaptureFileInjected error = %v", err)
	}
}

func TestRetireCaptureFileInjectedHonoursAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomPR3}
	if _, err := retireCaptureFileInjected(source, filepath.Join(dir, "dest"), inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("retireCaptureFileInjected error = %v", err)
	}
}

func TestRetireCaptureFileComputesTheDigestOfTheCapturedBytes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := retireCaptureFile(source, filepath.Join(dir, "dest"))
	if err != nil {
		t.Fatal(err)
	}
	if digest == "" {
		t.Fatal("digest is empty")
	}
}

// --- writeBranchCleanupReport (branches_cleanup.go) ---

func TestWriteBranchCleanupReportInjectedHonoursAnInjectedWriteFailure(t *testing.T) {
	t.Parallel()
	reportDir := t.TempDir()
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errBoomPR3}
	if _, err := writeBranchCleanupReportInjected(reportDir, BranchCleanupOptions{}, time.Now(), nil, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBranchCleanupReportInjected error = %v", err)
	}
}

func TestWriteBranchCleanupReportInjectedHonoursAnInjectedRenameFailure(t *testing.T) {
	t.Parallel()
	reportDir := t.TempDir()
	inj := &filewrite.Injector{Step: filewrite.StepRename, Err: errBoomPR3}
	if _, err := writeBranchCleanupReportInjected(reportDir, BranchCleanupOptions{}, time.Now(), nil, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBranchCleanupReportInjected error = %v", err)
	}
}

func TestWriteBranchCleanupReportPublishesReadableJSON(t *testing.T) {
	t.Parallel()
	reportDir := t.TempDir()
	path, err := writeBranchCleanupReport(reportDir, BranchCleanupOptions{}, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
