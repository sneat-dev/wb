package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// TestRelocateSharedTargetsTheCentralStore encodes
// projects-root-layout#ac:relocate-targets-the-store. A checkout created in
// repository-local mode must be movable to the central store by
// `wb worktree relocate --to=shared`, and the receipt must record the
// destination relative to the store root that produced it, so a later
// reconfigure of that root does not invalidate the evidence.
func TestRelocateSharedTargetsTheCentralStore(t *testing.T) {
	fixture := newStoreModeFixture(t, "app")
	fixture.selectStoreMode(t, StoreModeRepositoryLocal)
	ctx := context.Background()

	created, err := Create(ctx, []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "relocate-store", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("repository-local create = %#v, err=%v", created, err)
	}
	localWorktree := filepath.Join(fixture.canonicals["app"], ".worktrees", "relocate-store")
	if created[0].WorktreeDir != localWorktree {
		t.Fatalf("checkout = %q, want the repository-local %q", created[0].WorktreeDir, localWorktree)
	}

	// A shared destination needs a shared store. While the machine-local mode
	// is repository-local there is none, so the plan must refuse rather than
	// invent one.
	refused, err := Relocate(ctx, RelocateOptions{ProjectsRoot: fixture.projectsRoot, Task: "relocate-store", To: "shared"})
	if err != nil || len(refused.Results) != 1 {
		t.Fatalf("repository-local shared plan = %#v, err=%v", refused, err)
	}
	if refused.Results[0].Eligible || !strings.Contains(refused.Results[0].Reason, StoreModeRepositoryLocal) {
		t.Fatalf("repository-local shared plan = %#v, want a refusal naming the store mode", refused.Results[0])
	}

	// Back to the default: no store mode and no worktrees.root configured, so
	// the shared destination must be <root>/.worktrees with the canonical
	// clone's literal host level.
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	storeRoot, err := wbhome.StoreRoot(fixture.projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	wantDestination := filepath.Join(storeRoot, "relocate-store", "github.com", "acme", "app")

	plan, err := Relocate(ctx, RelocateOptions{ProjectsRoot: fixture.projectsRoot, Task: "relocate-store", To: "shared"})
	if err != nil || len(plan.Results) != 1 || !plan.Results[0].Eligible || plan.Results[0].Applied {
		t.Fatalf("shared plan = %#v, err=%v", plan, err)
	}
	if plan.Results[0].Destination != wantDestination {
		t.Fatalf("planned destination = %q, want the central store path %q", plan.Results[0].Destination, wantDestination)
	}
	if _, err := os.Stat(localWorktree); err != nil {
		t.Fatalf("dry run moved the checkout: %v", err)
	}

	applied, err := Relocate(ctx, RelocateOptions{ProjectsRoot: fixture.projectsRoot, Task: "relocate-store", To: "shared", Apply: true})
	if err != nil || len(applied.Results) != 1 || !applied.Results[0].Applied || applied.Results[0].ReceiptPath == "" {
		t.Fatalf("shared apply = %#v, err=%v", applied, err)
	}
	if _, err := os.Stat(wantDestination); err != nil {
		t.Fatalf("checkout was not moved to the central store: %v", err)
	}
	if _, err := Guard(ctx, wantDestination, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil {
		t.Fatalf("guard after relocation: %v", err)
	}
	listed, err := List(ctx, ListOptions{ProjectsRoot: fixture.projectsRoot, Task: "relocate-store"})
	if err != nil || len(listed) != 1 || listed[0].WorktreeDir != wantDestination {
		t.Fatalf("list after relocation = %#v, err=%v", listed, err)
	}

	// The receipt must stay interpretable after the store root is reconfigured:
	// it records the root it used and the destination below it.
	content, err := os.ReadFile(applied.Results[0].ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	var receipt workLogRelocationIntent
	if err := json.Unmarshal(content, &receipt); err != nil {
		t.Fatal(err)
	}
	wantRelative := filepath.Join("relocate-store", "github.com", "acme", "app")
	if receipt.DestinationRoot != storeRoot || receipt.DestinationRelative != wantRelative {
		t.Fatalf("receipt placement = %q / %q, want %q / %q", receipt.DestinationRoot, receipt.DestinationRelative, storeRoot, wantRelative)
	}
	if filepath.Join(receipt.DestinationRoot, receipt.DestinationRelative) != receipt.Destination {
		t.Fatalf("receipt placement %q + %q does not describe destination %q", receipt.DestinationRoot, receipt.DestinationRelative, receipt.Destination)
	}
	reconfigured := filepath.Join(fixture.base, "reconfigured-store")
	if got, want := filepath.Join(reconfigured, receipt.DestinationRelative), filepath.Join(reconfigured, "relocate-store", "github.com", "acme", "app"); got != want {
		t.Fatalf("re-rooted destination = %q, want %q", got, want)
	}
	// The absolute destination is still the historical fact, so a record
	// written before a reconfigure keeps validating afterwards.
	if err := validateRelocationRecord(receipt, workLogClaim{
		ClaimID: receipt.ClaimID, Task: receipt.Task, Repository: receipt.Repository, Branch: receipt.Branch,
	}, false); err != nil {
		t.Fatalf("receipt no longer validates: %v", err)
	}
}
