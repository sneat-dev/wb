package orchestrate

import (
	"github.com/sneat-dev/wb/internal/testenv"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func terminalOwnerAssertReleasedAndUnchanged(t *testing.T, root string, r WorktreeMergeReceipt, before []byte) {
	t.Helper()
	got, err := os.ReadFile(r.ReceiptPath)
	if err != nil || string(got) != string(before) {
		t.Fatalf("historical receipt changed: %v", err)
	}
	lock, err := AcquireOperationLock(root, r.Lane, true)
	if err != nil {
		t.Fatalf("owner kept operation lock: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

// terminalOwnerProviderFailure installs only an exact negative read; every
// other provider operation delegates the original native-DAG-backed script.
func terminalOwnerProviderFailure(t *testing.T) string {
	t.Helper()
	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	script := filepath.Join(bin, "gh")
	native := filepath.Join(bin, "gh-native")
	if err := os.Rename(script, native); err != nil {
		t.Fatal(err)
	}
	fault := filepath.Join(bin, "selected-read")
	if err := os.WriteFile(fault, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\nset -eu\nif [ \"$*\" = \"$(cat '" + fault + "')\" ]; then echo 'selected exact terminal provider read' >&2; exit 1; fi\nexec '" + native + "' \"$@\"\n"
	if err := testenv.WriteExecutableFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return fault
}
