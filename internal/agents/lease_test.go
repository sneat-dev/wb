package agents

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLeaseExcludesASecondWriterUntilTheHolderExits(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	worktree := t.TempDir()
	now := time.Unix(1000, 0).UTC()

	first, err := store.AcquireLease(worktree, "agt-first", "exp-one", "acme/app", now)
	if err != nil {
		t.Fatalf("first AcquireLease: %v", err)
	}
	if first.File() == nil || first.Path() == "" {
		t.Fatalf("lease = %#v", first)
	}
	if info := readLeaseInfo(first.Path()); info.AgentID != "agt-first" || info.ClaimedAt.IsZero() {
		t.Fatalf("lease info = %#v", info)
	}

	_, err = store.AcquireLease(worktree, "agt-second", "", "acme/app", now)
	var held *LeaseHeldError
	if !errors.As(err, &held) {
		t.Fatalf("second claim error = %v, want LeaseHeldError", err)
	}
	if !strings.Contains(err.Error(), "agt-first") {
		t.Fatalf("the refusal must name the owner: %v", err)
	}

	if holder, ok, holderErr := store.LeaseHolder(worktree); holderErr != nil || !ok || holder.AgentID != "agt-first" {
		t.Fatalf("LeaseHolder = %#v %v %v", holder, ok, holderErr)
	}

	if err := first.SetOwner(4242); err != nil {
		t.Fatalf("SetOwner: %v", err)
	}
	if info := readLeaseInfo(first.Path()); info.OwnerPID != 4242 {
		t.Fatalf("owner PID = %d", info.OwnerPID)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release must be idempotent: %v", err)
	}

	second, err := store.AcquireLease(worktree, "agt-second", "", "acme/app", now)
	if err != nil {
		t.Fatalf("the lease must be free after Release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseIsKeyedOnTheResolvedPath(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	worktree := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(worktree, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	lease, err := store.AcquireLease(worktree, "agt-one", "", "acme/app", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	// The same checkout under another spelling must be the same claim.
	if _, err := store.AcquireLease(link, "agt-two", "", "acme/app", time.Unix(1, 0)); err == nil {
		t.Fatal("a symlinked path must resolve to the same lease")
	}
}

func TestLeasePathHandlesACheckoutThatDoesNotExistYet(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	parent := t.TempDir()
	future := filepath.Join(parent, "not-yet", "created")
	path, err := store.LeasePath(future)
	if err != nil {
		t.Fatalf("LeasePath: %v", err)
	}
	if !strings.HasPrefix(path, store.Root+string(filepath.Separator)) {
		t.Fatalf("lease path %s is outside the store %s", path, store.Root)
	}
	if _, err := store.LeasePath("  "); err == nil {
		t.Fatal("an empty path must be refused")
	}
}

func TestLeaseHolderToleratesAMissingOrCorruptBody(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	worktree := t.TempDir()
	if _, ok, err := store.LeaseHolder(worktree); err != nil || ok {
		t.Fatalf("a missing lease = %v %v", ok, err)
	}
	path, err := store.LeasePath(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, ok, err := store.LeaseHolder(worktree)
	if err != nil || !ok || info.AgentID != "" {
		t.Fatalf("corrupt lease body = %#v %v %v", info, ok, err)
	}
}

func TestLeaseReportsAMissingStoreDirectory(t *testing.T) {
	t.Parallel()
	store := Store{Root: filepath.Join(t.TempDir(), "absent", "agents")}
	worktree := t.TempDir()
	lease, err := store.AcquireLease(worktree, "agt-one", "", "", time.Unix(1, 0))
	if err != nil {
		t.Fatalf("AcquireLease must create its directory: %v", err)
	}
	_ = lease.Release()
}

// failingLeaseWriter fails exactly one stage of the lease body write.
type failingLeaseWriter struct {
	failTruncate bool
	failWrite    bool
	failSync     bool
}

func (writer failingLeaseWriter) Truncate(int64) error {
	if writer.failTruncate {
		return errors.New("truncate failed")
	}
	return nil
}

func (writer failingLeaseWriter) WriteAt(data []byte, _ int64) (int, error) {
	if writer.failWrite {
		return 0, errors.New("write failed")
	}
	return len(data), nil
}

func (writer failingLeaseWriter) Sync() error {
	if writer.failSync {
		return errors.New("sync failed")
	}
	return nil
}

func TestLeaseBodyWriteReportsEachFailingStage(t *testing.T) {
	t.Parallel()
	for name, writer := range map[string]failingLeaseWriter{
		"truncate": {failTruncate: true},
		"write":    {failWrite: true},
		"sync":     {failSync: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			lease := &Lease{writer: writer, path: "lease.lock", info: LeaseInfo{AgentID: "agt-one"}}
			if err := lease.SetOwner(7); err == nil {
				t.Fatalf("%s failure was not reported", name)
			}
			// A failed write leaves the in-memory body unchanged.
			if lease.info.OwnerPID != 0 {
				t.Fatalf("owner PID = %d after a failed write", lease.info.OwnerPID)
			}
		})
	}
}

func TestLeaseHelperAccessorsTolerateNil(t *testing.T) {
	t.Parallel()
	var lease *Lease
	if lease.File() != nil || lease.Path() != "" {
		t.Fatal("nil lease accessors must be safe")
	}
	if err := lease.SetOwner(1); err != nil {
		t.Fatalf("nil SetOwner: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("nil Release: %v", err)
	}
	err := &LeaseHeldError{Path: "/lease"}
	if !strings.Contains(err.Error(), "another execution") || !strings.Contains(err.Error(), "/lease") {
		t.Fatalf("a bodiless lease error = %q", err.Error())
	}
}

func TestAcquireLeaseReportsAnUnusableLeaseDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := NewStore(root)
	if err := os.MkdirAll(store.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	// A file where the leases directory belongs makes the directory unusable.
	if err := os.WriteFile(filepath.Join(store.Root, leaseDirName), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireLease(t.TempDir(), "agt-one", "", "", time.Unix(1, 0)); err == nil {
		t.Fatal("an unusable lease directory must be reported")
	}
}

func TestAcquireLeaseReportsAClosedStoreRoot(t *testing.T) {
	t.Parallel()
	store := NewStore(filepath.Join(t.TempDir(), "absent"))
	// LeasePath itself succeeds through the nearest existing ancestor, so the
	// failing step is creating the directory under a file parent.
	parent := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.Root = filepath.Join(parent, "agents")
	if _, err := store.AcquireLease(t.TempDir(), "agt-one", "", "", time.Unix(1, 0)); err == nil {
		t.Fatal("a store under a file must be reported")
	}
}
