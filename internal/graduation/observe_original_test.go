package graduation

import (
	"errors"
	"github.com/sneat-dev/wb/internal/filewrite"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var errReceiptWrite = errors.New("boom")

func TestWriteGraduationReceiptWritesTheRawBytes(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := writeReceipt(path, []byte("payload")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "payload" {
		t.Fatalf("content = %q, want %q", got, "payload")
	}
}

func TestWriteGraduationReceiptReportsAMissingOutputDirectory(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing", "receipt.json")
	if err := writeReceipt(path, []byte("payload")); err == nil {
		t.Fatal("writeReceipt under a missing directory = nil, want an error")
	}
}

func TestWriteGraduationReceiptReportsTheOutputDirectoryBeingAFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	notADirectory := filepath.Join(dir, "notadir")
	if err := os.WriteFile(notADirectory, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(notADirectory, "receipt.json")
	if err := writeReceipt(path, []byte("payload")); err == nil {
		t.Fatal("writeReceipt with a file where the directory should be = nil, want an error")
	}
}

func TestWriteGraduationReceiptReportsAnExistingReceiptFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(path, []byte("already here"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeReceipt(path, []byte("payload")); err == nil {
		t.Fatal("writeReceipt over an existing file = nil, want an error")
	}
}

func TestWriteGraduationReceiptInjectedHonoursAnInjectedWriteFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Name: path, Err: errReceiptWrite}
	if err := writeReceiptInjected(path, []byte("payload"), inj); !errors.Is(err, errReceiptWrite) {
		t.Fatalf("writeReceiptInjected with injected write failure = %v, want errReceiptWrite", err)
	}
}

func TestWriteGraduationReceiptInjectedHonoursAnInjectedSyncFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	inj := &filewrite.Injector{Step: filewrite.StepSync, Name: path, Err: errReceiptWrite}
	if err := writeReceiptInjected(path, []byte("payload"), inj); !errors.Is(err, errReceiptWrite) {
		t.Fatalf("writeReceiptInjected with injected sync failure = %v, want errReceiptWrite", err)
	}
}

func TestWriteGraduationReceiptInjectedHonoursAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	inj := &filewrite.Injector{Step: filewrite.StepClose, Name: path, Err: errReceiptWrite}
	if err := writeReceiptInjected(path, []byte("payload"), inj); !errors.Is(err, errReceiptWrite) {
		t.Fatalf("writeReceiptInjected with injected close failure = %v, want errReceiptWrite", err)
	}
}

func TestReadGraduationEvidenceRejectsDirectoryAndEmptyFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := readEvidence(dir, "--local-check"); err == nil || !strings.Contains(err.Error(), "regular JSON evidence file") {
		t.Fatalf("directory error = %v", err)
	}
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readEvidence(empty, "--ci-wait"); err == nil || !strings.Contains(err.Error(), "between 1 and") {
		t.Fatalf("empty file error = %v", err)
	}
}
