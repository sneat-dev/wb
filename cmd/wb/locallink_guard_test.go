package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestLandingGuardIgnoresReservedFleetEventLog(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, filepath.Join(t.TempDir(), "wb-home"))
	projectsRoot := filepath.Join(t.TempDir(), "projects")

	store, err := streams.Open(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EventLog(".fleet").Append(streams.Event{Verb: "pr land", Outcome: "findings"}); err != nil {
		t.Fatalf("append fleet landing event: %v", err)
	}
	if err := refuseLinkedRepositoryWorktrees(&invocation{projectsRoot: projectsRoot}, "acme/app"); err != nil {
		t.Fatalf("fleet metadata blocked the landing guard: %v", err)
	}
}

// Schema version 2 is a fully understood, current stream-state format (it
// added linked_consumers for repositories admitted as alternate managed
// worktrees rather than stream members). A repository named only in
// linked_consumers must still be guarded on its live links, exactly like a
// member: joining as a linked consumer instead of a member must not be a way
// to dodge the landing guard.
func TestLandingGuardRefusesALiveLinkOnALinkedConsumerRepository(t *testing.T) {
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	t.Setenv(wbhome.EnvOverride, projectsRoot)
	home := filepath.Join(projectsRoot, ".wb")

	linkedWorktree := filepath.Join(projectsRoot, "acme", "linked", ".worktrees", "task")
	stateDir := filepath.Join(home, "streams", "known")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	state := `{"schema_version":2,"phase":"open","members":[{"repository":"acme/member"}],"linked_consumers":[` +
		`{"repository":"acme/linked","worktree":` + jsonString(linkedWorktree) + `,"links":[` +
		`{"library":"/path/to/library","library_repository":"acme/library","mechanism":"pnpm-link","identity":"@acme/library","created_at":"2026-09-06T00:00:00Z"}` +
		`]}]}`
	if err := os.WriteFile(filepath.Join(stateDir, "stream.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := refuseLinkedRepositoryWorktrees(&invocation{projectsRoot: projectsRoot}, "acme/unrelated"); err != nil {
		t.Fatalf("unrelated repository blocked landing: %v", err)
	}
	if err := refuseLinkedRepositoryWorktrees(&invocation{projectsRoot: projectsRoot}, "acme/linked"); err == nil {
		t.Fatal("a repository admitted only as a linked consumer with a live link did not fail closed")
	}
}

// A resume argument naming a worktree directly (not a merge receipt) is the
// documented second form of the argument; refuseLinkedReceiptWorktrees must
// route it straight into the live-link guard instead of trying to parse it
// as a receipt.
func TestRefuseLinkedReceiptWorktreesGuardsAWorktreeArgumentDirectly(t *testing.T) {
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	t.Setenv(wbhome.EnvOverride, projectsRoot)
	worktree := filepath.Join(t.TempDir(), "some-worktree")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := refuseLinkedReceiptWorktrees(&invocation{projectsRoot: projectsRoot}, worktree); err != nil {
		t.Fatalf("a worktree argument with no recorded live link was refused: %v", err)
	}
}

func jsonString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
