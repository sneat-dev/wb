package fleetsync

import (
	"context"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClassifyExplainsIncompleteRepositoryTransferRecovery(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		partial worktrees.RepositoryRelocateResult
		want    string
	}{
		{name: "cleanup pending", partial: worktrees.RepositoryRelocateResult{Eligible: true, Applied: true, CleanupPending: true, Reason: "clone quarantine remains", RecoveryCommand: "wb recover"}, want: "clone quarantine remains; recover with: wb recover"},
		{name: "application pending", partial: worktrees.RepositoryRelocateResult{Eligible: true, Reason: "target not applied"}, want: "target not applied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			result := classifyWithRelocate(context.Background(), discover.Repo{TransferFrom: "old/app", Org: "new", Name: "app"}, t.TempDir(), false, false, func(_ context.Context, options worktrees.RepositoryRelocateOptions) (worktrees.RepositoryRelocateResult, error) {
				calls++
				if !options.Apply || options.SourceRepository != "old/app" || options.DestinationRepository != "new/app" {
					t.Fatalf("relocation options=%+v", options)
				}
				return tc.partial, nil
			})
			if calls != 1 || result.Status != RepositoryTransferRequired || result.Err != nil || result.Reason != tc.want || result.RepositoryRelocation == nil {
				t.Fatalf("classification=%+v,calls=%d", result, calls)
			}
		})
	}
}

func TestWriteRemovalReceiptRejectsInvalidAuditTimeBeforePublishing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	invalid := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	path, err := writeRemovalReceipt(root, RemovalReceipt{Repository: "acme/app", CreatedAt: invalid})
	if err == nil || path != "" || !strings.Contains(err.Error(), "encode prune receipt") {
		t.Fatalf("receipt=%q,%v", path, err)
	}
	directory := filepath.Join(root, ".wb", "reports", "sync-prune-archived")
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed serialization left artifacts=%v,%v", entries, err)
	}
}

func TestOverwriteRemovalReceiptKeepsEvidenceWhenAuditTimeIsInvalid(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	before := []byte("retained audit evidence")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	invalid := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := overwriteRemovalReceipt(path, RemovalReceipt{CreatedAt: invalid}); err == nil || !strings.Contains(err.Error(), "encode prune receipt") {
		t.Fatalf("overwrite=%v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("evidence replaced after failed serialization: %q,%v", after, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("failed serialization left staged artifacts: %v,%v", entries, err)
	}
}
