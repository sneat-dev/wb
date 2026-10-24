package agentguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// moveClaimStoreIntoStateDir relocates the fixture's legacy wb-state mirror to
// <root>/.wb/remote-store/<owner>/wb-state, the way the one-time migration of
// sneat-dev/wb#901 leaves a machine, and returns the migrated clone path.
func moveClaimStoreIntoStateDir(t *testing.T, claim claimFixture) string {
	t.Helper()
	legacy := filepath.Join(claim.ProjectsRoot, claim.Owner, "wb-state")
	migrated := filepath.Join(claim.ProjectsRoot, ".wb", "remote-store", claim.Owner, "wb-state")
	if err := os.MkdirAll(filepath.Dir(migrated), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(legacy, migrated); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(migrated, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return migrated
}

// After the remote store clone moved into WB's state directory, the live-claim
// check reads the claim from there and still refuses the dispatch.
func TestAgentDispatchRefusesLiveClaimHeldInTheMigratedRemoteStore(t *testing.T) {
	claim := newClaimFixture(t)
	moveClaimStoreIntoStateDir(t, claim)

	prompt := "Fix the bug in /Users/alex/projects/" + claim.Owner + "/" + claim.Repository + " described above."
	decision := Inspect(agentCall(agentDispatch(prompt, "sonnet"), t.TempDir()), Options{ProjectsRoot: claim.ProjectsRoot})
	if !decision.Deny {
		t.Fatal("Inspect allowed a dispatch into a claim held in the migrated remote store")
	}
	if !strings.Contains(decision.Reason, "someoneelse/theirlaptop") {
		t.Fatalf("refusal does not name the claim owner from the migrated store:\n%s", decision.Reason)
	}
}

// Once migrated, the clone in the state directory is the store: a claim file
// left behind in a stray legacy copy is not live.
func TestAgentDispatchIgnoresAStrayLegacyClaimOnceTheStoreMigrated(t *testing.T) {
	claim := newClaimFixture(t)
	migrated := moveClaimStoreIntoStateDir(t, claim)
	if err := os.Remove(filepath.Join(migrated, "claims", claim.Task+".yaml")); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(claim.ProjectsRoot, claim.Owner, "wb-state", "claims")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, claim.Task+".yaml"), []byte("task: "+claim.Task+"\nlogin: a\nmachine: b\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	prompt := "Fix the bug in /Users/alex/projects/" + claim.Owner + "/" + claim.Repository + "."
	decision := Inspect(agentCall(agentDispatch(prompt, "sonnet"), t.TempDir()), Options{ProjectsRoot: claim.ProjectsRoot})
	if decision.Deny {
		t.Fatalf("Inspect refused a dispatch over a stray legacy claim:\n%s", decision.Reason)
	}
}
