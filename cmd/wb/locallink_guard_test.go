package main

import (
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

func TestLandingGuardIgnoresUnrelatedFutureSchemaStream(t *testing.T) {
	home := filepath.Join(t.TempDir(), "wb-home")
	t.Setenv(wbhome.EnvOverride, home)
	previousProjectsRoot := projectsRoot
	projectsRoot = filepath.Join(t.TempDir(), "projects")
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })

	stateDir := filepath.Join(home, "streams", "future")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	state := []byte(`{"schema_version":2,"members":[{"repository":"acme/member"}],"linked_consumers":[{"repository":"acme/linked"}]}`)
	if err := os.WriteFile(filepath.Join(stateDir, "stream.json"), state, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := refuseLinkedRepositoryWorktrees("acme/unrelated"); err != nil {
		t.Fatalf("unrelated future stream blocked landing: %v", err)
	}
	if err := refuseLinkedRepositoryWorktrees("acme/linked"); err == nil {
		t.Fatal("future stream containing the repository did not fail closed")
	}
}
