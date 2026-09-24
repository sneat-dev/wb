//go:build !windows

package fleetsync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/testenv"
)

// TestRPCovSyncArchivedRemovalFailureLeavesAFailedReceipt exercises the one
// destructive path that must never be silent: on POSIX, a read-only clone
// directory defeats os.RemoveAll after the pre-deletion receipt was already
// durably written, so the run has to report Failed, retain the HEAD it was
// about to destroy, and update the receipt to the failed phase.
func TestRPCovSyncArchivedRemovalFailureLeavesAFailedReceipt(t *testing.T) {
	installArchivedFakeGh(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "f.txt", "v1\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "v1")
	remote := t.TempDir()
	git(t, remote, "init", "-q", "--bare", "-b", "main")
	testenv.ConfigureGitAutoMaintenanceOff(t, remote)
	git(t, dir, "remote", "add", "origin", remote)
	git(t, dir, "push", "-q", "origin", "main")

	// A directory entry inside the clone cannot be unlinked without write
	// permission on the clone root, which is exactly what os.RemoveAll needs.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()

	repo := discover.Repo{Org: "acme", Name: "widgets", Path: dir, Remote: true, Archived: true}
	res := Sync(context.Background(), repo, "", false, true)

	if res.Status != Failed || res.Err == nil {
		t.Fatalf("result = %+v, want a Failed removal", res)
	}
	if res.HeadSHA == "" {
		t.Fatal("failed removal lost the HEAD it was about to destroy")
	}
	if res.ReceiptPath == "" {
		t.Fatal("failed removal left no receipt")
	}
	raw, err := os.ReadFile(res.ReceiptPath)
	if err != nil {
		t.Fatalf("read removal receipt: %v", err)
	}
	var receipt RemovalReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatalf("decode removal receipt: %v", err)
	}
	if receipt.Phase != PhaseFailed || receipt.Error == "" {
		t.Fatalf("receipt = %+v, want the failed phase with its error recorded", receipt)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("clone contents were removed despite the failure being reported: %v", err)
	}
	if !strings.Contains(res.Err.Error(), "permission denied") {
		t.Fatalf("error = %v, want the underlying permission failure", res.Err)
	}
}
