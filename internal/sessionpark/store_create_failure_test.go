package sessionpark

import (
	"os"
	"testing"
)

// TestStoreCreateReportsAggregateDirectoryFailure drives Store.Create's
// aggregate-directory Mkdirat error branch: a plain file already occupies
// the exact path the per-session aggregate directory would be created at,
// so Mkdirat fails with EEXIST instead of succeeding (Create tolerates no
// pre-existing entry there, unlike the idempotent immutable-file helpers).
func TestStoreCreateReportsAggregateDirectoryFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := NewStore(root)
	bundle := testBundle(t)
	if err := os.WriteFile(root+"/"+bundle.ParkedSessionID, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(bundle); err == nil {
		t.Fatal("Store.Create succeeded despite a conflicting aggregate path, want error")
	}
}
