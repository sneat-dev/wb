package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestCwWtMarkCreatedRenamedRelocatedAndSynced(t *testing.T) {
	seeds := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	cwCovCloneWithOrigin(t, seeds, "app", clone)

	var errOut strings.Builder
	command := &cobra.Command{}
	command.SetOut(&strings.Builder{})
	command.SetErr(&errOut)

	// refreshSyncedCheckoutMarkers skips failed and unnamed results, and
	// counts a clone it cannot describe.
	errOut.Reset()
	refreshSyncedCheckoutMarkers([]fleetsync.Result{
		{Repo: discover.Repo{Org: "acme", Name: "app"}, Status: fleetsync.Failed},
		{Repo: discover.Repo{Org: "", Name: ""}},
		{Repo: discover.Repo{Org: "acme", Name: "app"}},
		{Repo: discover.Repo{Org: "acme", Name: "absent"}},
	}, projects, &errOut)
	if errOut.Len() != 0 {
		t.Fatalf("refreshSyncedCheckoutMarkers warnings = %q", errOut.String())
	}

	// markRenamedCheckouts skips unapplied results and warns on failure.
	errOut.Reset()
	markRenamedCheckouts(&invocation{projectsRoot: projects}, command, "main", []worktrees.RenameResult{
		{Applied: false, NewWorktreeDir: filepath.Join(t.TempDir(), "missing")},
		{Applied: true, NewWorktreeDir: ""},
		{Applied: true, NewWorktreeDir: filepath.Join(t.TempDir(), "missing")},
	})
	if !strings.Contains(errOut.String(), "warning: could not refresh") {
		t.Fatalf("markRenamedCheckouts warnings = %q", errOut.String())
	}

	// markRelocatedCheckouts skips unapplied results and warns on failure.
	errOut.Reset()
	markRelocatedCheckouts(&invocation{projectsRoot: projects}, command, []worktrees.RelocateResult{
		{Applied: false, Destination: filepath.Join(t.TempDir(), "missing")},
		{Applied: true, Destination: filepath.Join(t.TempDir(), "missing")},
	})
	if !strings.Contains(errOut.String(), "warning: relocated worktree marker") {
		t.Fatalf("markRelocatedCheckouts warnings = %q", errOut.String())
	}
}
