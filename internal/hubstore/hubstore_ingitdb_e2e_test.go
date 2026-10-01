//go:build e2e

package hubstore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/internal/hubconfig"
)

// hubstoreGitProject gives each inGitDB write journey a private Git metadata
// directory. The pinned adapter puts its transaction lock under the Git dir
// when one exists; a non-Git fixture falls back to the user's cache directory.
func hubstoreGitProject(t *testing.T, projectPath string) string {
	t.Helper()
	// A process-local environment keeps a hook's inherited GIT_DIR and Git
	// configuration from redirecting either command into the invoking checkout.
	// testenv has maintenance settings but no hermetic Git command environment.
	gitEnv := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
	}
	init := exec.Command("git", "-C", projectPath, "init", "-q")
	init.Env = gitEnv
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("initialize private Git project: %v: %s", err, out)
	}
	gitPath := exec.Command("git", "-C", projectPath, "rev-parse", "--git-path", "dalgo2ingitdb/transaction.lock")
	gitPath.Env = gitEnv
	out, err := gitPath.Output()
	if err != nil {
		t.Fatalf("resolve private transaction lock: %v", err)
	}
	lockPath := strings.TrimSpace(string(out))
	if !filepath.IsAbs(lockPath) {
		lockPath = filepath.Join(projectPath, lockPath)
	}
	lockPath = filepath.Clean(lockPath)
	want := filepath.Join(projectPath, ".git", "dalgo2ingitdb", "transaction.lock")
	if lockPath != want {
		t.Fatalf("transaction lock path = %q, want private path %q", lockPath, want)
	}
	return lockPath
}

func hubstoreAssertLocalLock(t *testing.T, lockPath string) {
	t.Helper()
	info, err := os.Stat(lockPath)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("private transaction lock = %v, %v", info, err)
	}
}

// TestE2EInGitDBEngineCreatesTheProjectAndDeclaresEveryCollection covers the
// half of the inGitDB path that works today: the directory is created when
// missing, every collection in hub.Collections() is declared, a write lands,
// and a second Open over the same directory is a no-op rather than an
// "already exists" failure.
func TestE2EInGitDBEngineCreatesTheProjectAndDeclaresEveryCollection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hub")
	store, closer, err := Open(ctx, hubconfig.Store{Engine: hubconfig.EngineInGitDB, Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })
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
	lockPath := hubstoreGitProject(t, path)
	if result, err := snapshots.StoreLatest(ctx, testSnapshot("machine-1")); err != nil || !result.Updated {
		t.Fatalf("StoreLatest = %+v, %v", result, err)
	}
	hubstoreAssertLocalLock(t, lockPath)

	reopened, reopenedCloser, err := Open(ctx, hubconfig.Store{Engine: hubconfig.EngineInGitDB, Path: path})
	if err != nil || reopened == nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := reopenedCloser.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestE2EInGitDBEngineRunsTheHubJourneys verifies the hub's durable snapshot,
// repository-event, and coverage write/read journeys against the real inGitDB
// adapter. A fixture-local Git repository keeps transaction locks private.
func TestE2EInGitDBEngineRunsTheHubJourneys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hub")
	store, closer, err := Open(ctx, hubconfig.Store{Engine: hubconfig.EngineInGitDB, Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	_, _, snapshots := hub.NewMachineStores(store)
	lockPath := hubstoreGitProject(t, path)
	if _, err := snapshots.StoreLatest(ctx, testSnapshot("machine-1")); err != nil {
		t.Fatalf("StoreLatest: %v", err)
	}
	hubstoreAssertLocalLock(t, lockPath)
	latest, err := snapshots.ListLatest(ctx)
	if err != nil || len(latest) != 1 || latest[0].MachineID != "machine-1" {
		t.Fatalf("ListLatest = %+v, %v", latest, err)
	}

	events, status := hub.NewRepositoryEventStore(store)
	machine := hub.Machine{ID: "machine-1", Name: "laptop", IdentityID: "local"}
	event := repositoryevent.Event{Version: repositoryevent.ContractVersion, ID: "evt-1", Repository: "github.com/sneat-dev/wb", Ref: "refs/heads/main", Reason: repositoryevent.ReasonDefaultBranchUpdated, TargetSHA: strings.Repeat("a", 40)}
	if result, err := events.EnqueueForMachines(ctx, event, []hub.Machine{machine}); err != nil || result.Enqueued != 1 {
		t.Fatalf("EnqueueForMachines = %+v, %v", result, err)
	}
	if result, err := events.EnqueueForMachines(ctx, event, []hub.Machine{machine}); err != nil || !result.Duplicate {
		t.Fatalf("replayed EnqueueForMachines = %+v, %v", result, err)
	}
	polled, err := events.Poll(ctx, machine, "", 10)
	if err != nil || len(polled.Events) != 1 || polled.Events[0].ID != "evt-1" {
		t.Fatalf("Poll = %+v, %v", polled, err)
	}
	ack := repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: polled.NextCursor, EventIDs: []string{"evt-1"}}
	if _, err := events.Acknowledge(ctx, machine, ack); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	received, pending, _, err := status.IdentityRepositoryEventStatus(ctx, "local")
	if err != nil || received == nil || received.LastAcknowledged == nil || len(pending) != 0 {
		t.Fatalf("status after acknowledge = %+v, %+v, %v", received, pending, err)
	}
	after, err := events.Poll(ctx, machine, polled.NextCursor, 10)
	if err != nil || len(after.Events) != 0 {
		t.Fatalf("Poll after acknowledge = %+v, %v", after, err)
	}

	coverageStore := hub.NewRepositoryCoverageStore(store)
	coverageRecord := hub.StoredRepositoryCoverage{
		Repository: "sneat-dev/wb",
		SHA:        strings.Repeat("a", 40),
		Statements: 1000,
		Covered:    850,
		Percentage: 85.0,
	}
	if err := coverageStore.SaveCoverage(ctx, coverageRecord); err != nil {
		t.Fatalf("SaveCoverage on inGitDB: %v", err)
	}
	gotCoverage, found, err := coverageStore.GetCoverage(ctx, "sneat-dev/wb")
	if err != nil || !found {
		t.Fatalf("GetCoverage on inGitDB = %+v, %v, %v", gotCoverage, found, err)
	}
	if gotCoverage.Statements != 1000 || gotCoverage.Covered != 850 {
		t.Fatalf("GetCoverage on inGitDB statements mismatch = %+v", gotCoverage)
	}
}
