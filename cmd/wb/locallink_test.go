package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
)

// TestRefuseLinkedRepositoryWorktreesFailsClosedOnUnreadableStream drives the
// "len(unreadable) > 0" branch: when the guard cannot tell whether a stream
// holds a live local link to the repository (a truncated stream.json), it
// must fail closed and name every unreadable stream by name and reason
// rather than silently treating "could not read" as "no link".
func TestRefuseLinkedRepositoryWorktreesFailsClosedOnUnreadableStream(t *testing.T) {
	t.Parallel()
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	store, err := streams.Open(projectsRoot)
	if err != nil {
		t.Fatalf("streams.Open: %v", err)
	}
	if err := os.MkdirAll(store.Dir("broken"), 0o700); err != nil {
		t.Fatalf("mkdir broken stream dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir("broken"), "stream.json"), []byte("{truncated"), 0o600); err != nil {
		t.Fatalf("write truncated stream.json: %v", err)
	}

	err = refuseLinkedRepositoryWorktrees(&invocation{projectsRoot: projectsRoot}, "acme/app")
	if err == nil {
		t.Fatal("refuseLinkedRepositoryWorktrees returned nil error, want a fail-closed refusal")
	}
	exitErr, ok := err.(*exitError)
	if !ok {
		t.Fatalf("error type = %T, want *exitError", err)
	}
	if exitErr.code != exitUsage {
		t.Fatalf("exit code = %d, want %d", exitErr.code, exitUsage)
	}
	for _, want := range []string{
		"cannot tell whether acme/app holds a live local link",
		"broken (",
	} {
		if !strings.Contains(exitErr.message, want) {
			t.Errorf("message %q missing %q", exitErr.message, want)
		}
	}
}
