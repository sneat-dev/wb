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
	previousProjectsRoot := projectsRoot
	projectsRoot = filepath.Join(t.TempDir(), "projects")
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })

	store, err := streams.Open(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EventLog(".fleet").Append(streams.Event{Verb: "pr land", Outcome: "findings"}); err != nil {
		t.Fatalf("append fleet landing event: %v", err)
	}
	if err := refuseLinkedRepositoryWorktrees("acme/app"); err != nil {
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
	home := filepath.Join(t.TempDir(), "wb-home")
	t.Setenv(wbhome.EnvOverride, home)
	previousProjectsRoot := projectsRoot
	projectsRoot = filepath.Join(t.TempDir(), "projects")
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })

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

	if err := refuseLinkedRepositoryWorktrees("acme/unrelated"); err != nil {
		t.Fatalf("unrelated repository blocked landing: %v", err)
	}
	if err := refuseLinkedRepositoryWorktrees("acme/linked"); err == nil {
		t.Fatal("a repository admitted only as a linked consumer with a live link did not fail closed")
	}
}

func jsonString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
