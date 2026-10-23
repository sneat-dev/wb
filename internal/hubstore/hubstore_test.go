package hubstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/internal/hubconfig"
)

func testSnapshot(machineID string) hub.StoredMachineSnapshot {
	published := time.Date(2026, 9, 11, 14, 0, 0, 0, time.UTC)
	return hub.StoredMachineSnapshot{
		IdentityID: "local",
		MachineID:  machineID,
		Snapshot: machinesnapshot.Snapshot{
			SchemaVersion: machinesnapshot.SchemaVersion,
			Login:         "alice",
			Machine:       "laptop",
			PublishedAt:   published,
			Worktrees:     []machinesnapshot.Worktree{{Task: "bench", Repository: "sneat-dev/wb", Branch: "feature/bench"}},
		},
		ReceivedAt: published.Add(time.Second),
		Digest:     "digest",
	}
}

// TestMemoryEngineRunsAWholeStoreJourney is the engine the serve-and-fetch
// test and every throwaway run use, so it has to do real work rather than
// merely open.
func TestMemoryEngineRunsAWholeStoreJourney(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, closer, err := Open(ctx, hubconfig.Store{Engine: hubconfig.EngineMemory})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := closer.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	_, _, snapshots := hub.NewMachineStores(store)
	record := testSnapshot("machine-1")
	if result, err := snapshots.StoreLatest(ctx, record); err != nil || !result.Updated {
		t.Fatalf("StoreLatest = %+v, %v", result, err)
	}
	listed, err := snapshots.ListLatest(ctx)
	if err != nil || len(listed) != 1 || listed[0].MachineID != record.MachineID {
		t.Fatalf("ListLatest = %+v, %v", listed, err)
	}
}

// TestAnEmptyEngineIsTheMemoryEngine keeps Open total: hubconfig always fills
// the engine in, but a caller that builds a Store by hand must not get a
// silent nil database.
func TestAnEmptyEngineIsTheMemoryEngine(t *testing.T) {
	t.Parallel()
	store, closer, err := Open(context.Background(), hubconfig.Store{})
	if err != nil || store == nil {
		t.Fatalf("Open = %v, %v", store, err)
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownEngineIsRefused(t *testing.T) {
	t.Parallel()
	_, closer, err := Open(context.Background(), hubconfig.Store{Engine: "postgres"})
	if err == nil || !strings.Contains(err.Error(), "postgres") {
		t.Fatalf("Open = %v", err)
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestOpenVaultDBEngineIsConstructedWithoutReachingTheServer proves the
// constructor path: NewDB validates its arguments without a request, and a
// use against an unreachable URL fails at the call rather than at Open, so a
// daemon whose OpenVaultDB is down still starts and reports the error.
func TestOpenVaultDBEngineIsConstructedWithoutReachingTheServer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// 127.0.0.1:1 has nothing listening and needs no network to refuse.
	store, closer, err := Open(ctx, hubconfig.Store{Engine: hubconfig.EngineOpenVaultDB, URL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	_, _, snapshots := hub.NewMachineStores(store)
	if _, err := snapshots.ListLatest(ctx); err == nil {
		t.Fatal("a query against an unreachable OpenVaultDB succeeded")
	}
}

func TestOpenVaultDBEngineRefusesAnEmptyURL(t *testing.T) {
	t.Parallel()
	if _, _, err := Open(context.Background(), hubconfig.Store{Engine: hubconfig.EngineOpenVaultDB}); err == nil {
		t.Fatal("openvaultdb without a URL was accepted")
	}
}

func TestInGitDBEngineRefusesAnEmptyPathAndAnUnusableDirectory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if _, _, err := Open(ctx, hubconfig.Store{Engine: hubconfig.EngineInGitDB}); err == nil {
		t.Fatal("ingitdb without a path was accepted")
	}
	// A regular file where the project directory should be makes both MkdirAll
	// and the adapter's own stat fail.
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(ctx, hubconfig.Store{Engine: hubconfig.EngineInGitDB, Path: file}); err == nil {
		t.Fatal("a file in place of the project directory was accepted")
	}
}

// TestInGitDBEngineSurfacesADeclarationFailure covers the branch that makes a
// half-declared project loud: a regular file occupying a collection's
// directory name stops CreateCollection, and Open must name the collection
// rather than hand back a store that fails on first write.
func TestInGitDBEngineSurfacesADeclarationFailure(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	blocked, _, _ := strings.Cut(hub.Collections()[0], "/")
	if err := os.WriteFile(filepath.Join(path, blocked), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := Open(context.Background(), hubconfig.Store{Engine: hubconfig.EngineInGitDB, Path: path})
	if err == nil || !strings.Contains(err.Error(), blocked) {
		t.Fatalf("Open = %v, want it to name %q", err, blocked)
	}
}
