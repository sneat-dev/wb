package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestSyncDryRunFilterNarrowsPullPlannedCount proves --filter actually
// narrows the fleet sync operates on, not just how it is described: with two
// already-cloned repositories in different orgs, an unfiltered dry run plans
// a pull for both, and adding --filter for one org's slug plans a pull for
// only that one. Mutating the filter application in fleet() (cmd/wb/fleet.go)
// to a no-op turns this from PASS to FAIL, proving the value.
func TestSyncDryRunFilterNarrowsPullPlannedCount(t *testing.T) {
	root := t.TempDir()
	seeds := t.TempDir()
	cwCovFakeGH(t, "cwcov-sync-user", nil,
		`[{"name":"app","sshUrl":"git@github.com:acme/app.git","isArchived":false,"isFork":false}]`)

	acmeClone := filepath.Join(root, "acme", "app")
	cwCovCloneWithOrigin(t, seeds, "acme-app", acmeClone)

	otherClone := filepath.Join(root, "other-org", "app")
	cwCovCloneWithOrigin(t, seeds, "other-org-app", otherClone)

	unfiltered := func() *cobra.Command {
		return newSyncCmd(&invocation{projectsRoot: root, nonInteractive: true})
	}
	stdout, _, err := cwCovExec(t, root, unfiltered, "--dry-run", "--org", "acme", "--org", "other-org")
	if err != nil {
		t.Fatalf("sync --dry-run: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "Pull planned            2") {
		t.Fatalf("unfiltered dry run stdout = %q, want Pull planned 2", stdout)
	}

	filteredBuild := func() *cobra.Command {
		return newSyncCmd(&invocation{projectsRoot: root, nonInteractive: true, filterFlag: "acme"})
	}
	filtered, _, err := cwCovExec(t, root, filteredBuild, "--dry-run", "--org", "acme", "--org", "other-org")
	if err != nil {
		t.Fatalf("sync --dry-run --filter acme: %v\n%s", err, filtered)
	}
	if !strings.Contains(filtered, "Pull planned            1") {
		t.Fatalf("filtered dry run stdout = %q, want Pull planned 1", filtered)
	}
	if strings.Contains(filtered, "Pull planned            2") {
		t.Fatalf("filtered dry run still reports the unfiltered count: %q", filtered)
	}
}
