package hubstore

import (
	"context"
	"errors"
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
	ctx := context.Background()
	store, closer, err := Open(ctx, hubconfig.Store{Engine: hubconfig.EngineMemory})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := closer.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()
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
	store, closer, err := Open(context.Background(), hubconfig.Store{})
	if err != nil || store == nil {
		t.Fatalf("Open = %v, %v", store, err)
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownEngineIsRefused(t *testing.T) {
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
	ctx := context.Background()
	// 127.0.0.1:1 has nothing listening and needs no network to refuse.
	store, closer, err := Open(ctx, hubconfig.Store{Engine: hubconfig.EngineOpenVaultDB, URL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = closer.Close() }()
	_, _, snapshots := hub.NewMachineStores(store)
	if _, err := snapshots.ListLatest(ctx); err == nil {
		t.Fatal("a query against an unreachable OpenVaultDB succeeded")
	}
}

func TestOpenVaultDBEngineRefusesAnEmptyURL(t *testing.T) {
	if _, _, err := Open(context.Background(), hubconfig.Store{Engine: hubconfig.EngineOpenVaultDB}); err == nil {
		t.Fatal("openvaultdb without a URL was accepted")
	}
}

// TestInGitDBEngineCreatesTheProjectAndDeclaresEveryCollection covers the
// half of the inGitDB path that works today: the directory is created when
// missing, every collection in hub.Collections() is declared, a write lands,
// and a second Open over the same directory is a no-op rather than an
// "already exists" failure.
func TestInGitDBEngineCreatesTheProjectAndDeclaresEveryCollection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hub")
	store, closer, err := Open(ctx, hubconfig.Store{Engine: hubconfig.EngineInGitDB, Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = closer.Close() }()
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("store directory = %v, %v", info, err)
	}
	for _, collection := range hub.Collections() {
		root, _, _ := strings.Cut(collection, "/")
		if _, err := os.Stat(filepath.Join(path, root, ".collection", "definition.yaml")); err != nil {
			t.Fatalf("collection %q was not declared: %v", collection, err)
		}
	}
	_, _, snapshots := hub.NewMachineStores(store)
	if result, err := snapshots.StoreLatest(ctx, testSnapshot("machine-1")); err != nil || !result.Updated {
		t.Fatalf("StoreLatest = %+v, %v", result, err)
	}

	reopened, reopenedCloser, err := Open(ctx, hubconfig.Store{Engine: hubconfig.EngineInGitDB, Path: path})
	if err != nil || reopened == nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := reopenedCloser.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestInGitDBEngineCannotServeQueriesYet pins the reason `engine: ingitdb` is
// documented as not usable in hub/README.md, so the limitation is a fact the
// suite asserts rather than a note someone has to remember.
//
// github.com/sneat-dev/wb/api/githubapp.DocumentStore.Query decodes each
// returned row into the element type of the caller's slice, which
// dalgostore does by taking the record factory a DALgo query carries.
// dalgo2ingitdb v0.4.0 ignores that factory: its query path rebuilds every
// record with a map[string]any payload (query.go readAllRecordsFromDisk /
// bakeStoredRecords), so dalgostore's reflect.ValueOf(found.Data()).Elem()
// panics on a map.
//
// Every hub read-back goes through Query — repository-event poll, the pending
// refresh list, machine snapshots, installation bindings — so the store
// journeys in hub/dalgostore_parity_test.go cannot run on inGitDB until
// upstream honours the factory. When it does, this test fails and the
// limitation in hub/README.md comes out.
func TestInGitDBEngineCannotServeQueriesYet(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hub")
	store, closer, err := Open(ctx, hubconfig.Store{Engine: hubconfig.EngineInGitDB, Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = closer.Close() }()
	_, _, snapshots := hub.NewMachineStores(store)
	if _, err := snapshots.StoreLatest(ctx, testSnapshot("machine-1")); err != nil {
		t.Fatalf("StoreLatest: %v", err)
	}
	failure := recovered(func() {
		if _, err := snapshots.ListLatest(ctx); err == nil {
			panic(errors.New("no failure"))
		}
	})
	if failure == nil {
		t.Fatal("dalgo2ingitdb now decodes query rows into the caller's type; run the hub parity journeys over it and drop the limitation from hub/README.md")
	}
	if !strings.Contains(failure.Error(), "reflect") {
		t.Fatalf("ListLatest failed for an unexpected reason: %v", failure)
	}
}

// recovered runs body and returns whatever it panicked with, as an error, or
// nil when it returned normally.
func recovered(body func()) (failure error) {
	defer func() {
		if value := recover(); value != nil {
			if err, ok := value.(error); ok {
				failure = err
				return
			}
			failure = errors.New(strings.TrimSpace(strings.Join([]string{"panic:", toString(value)}, " ")))
		}
	}()
	body()
	return nil
}

func toString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return "non-string panic value"
}

func TestInGitDBEngineRefusesAnEmptyPathAndAnUnusableDirectory(t *testing.T) {
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
