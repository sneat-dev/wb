package orchestrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOperationLockMetadataRequiresExactOwnerRecord(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, contents string
		wantPID        int
		wantValid      bool
	}{
		{"exact", "operation=sync\npid=42\n", 42, true},
		{"different operation", "operation=land\npid=42\n", 0, false},
		{"missing trailing newline", "operation=sync\npid=42", 0, false},
		{"extra record", "operation=sync\npid=42\nextra\n", 0, false},
		{"missing pid prefix", "operation=sync\n42\n", 42, false},
		{"noncanonical pid", "operation=sync\npid=042\n", 42, false},
		{"nonpositive pid", "operation=sync\npid=0\n", 0, false},
		{"unparseable pid", "operation=sync\npid=unknown\n", 0, false},
		{"oversized record", strings.Repeat("x", 4097), 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			file, err := os.Create(filepath.Join(t.TempDir(), ".lock"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = file.Close() })
			if _, err := file.WriteString(tc.contents); err != nil {
				t.Fatal(err)
			}
			// Parsing must rewind the actual descriptor left at EOF by writing.
			pid, valid := operationLockMetadataPID(file, "sync")
			if pid != tc.wantPID || valid != tc.wantValid {
				t.Fatalf("owner record: pid=%d valid=%v, want %d %v", pid, valid, tc.wantPID, tc.wantValid)
			}
		})
	}
}

func TestOperationLockMetadataRefusesUnavailableDescriptors(t *testing.T) {
	t.Parallel()
	if pid, valid := operationLockMetadataPID(nil, "sync"); pid != 0 || valid {
		t.Fatalf("missing descriptor accepted: pid=%d valid=%v", pid, valid)
	}
	file, err := os.Create(filepath.Join(t.TempDir(), ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if pid, valid := operationLockMetadataPID(file, "sync"); pid != 0 || valid {
		t.Fatalf("closed descriptor accepted: pid=%d valid=%v", pid, valid)
	}
	writeOnly, err := os.OpenFile(filepath.Join(t.TempDir(), ".lock"), os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writeOnly.Close() })
	if _, err := writeOnly.Seek(0, 0); err != nil {
		t.Fatalf("write-only regular descriptor must support seeking: %v", err)
	}
	if pid, valid := operationLockMetadataPID(writeOnly, "sync"); pid != 0 || valid {
		t.Fatalf("unreadable descriptor accepted: pid=%d valid=%v", pid, valid)
	}
}
