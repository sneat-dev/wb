package integration

import (
	"context"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type parkedResumeRefusalFixture struct {
	root, config string
	store        sessionpark.Store
	lock         *sessionpark.SourceLock
	state        sessionpark.State
}

func newPublicResumeRefusalFixture(t *testing.T) parkedResumeRefusalFixture {
	t.Helper()
	root := t.TempDir()
	store := sessionpark.NewStore(filepath.Join(root, ".wb", sessionpark.SourceDirName))
	bundle := sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: "park-refusal", Source: session.Record{
		PID: 41, WBSessionID: "wbs-source", Machine: "source", Runtime: "codex", StartedAt: time.Unix(10, 0).UTC(),
	}, Continuation: "private context", Worktrees: []sessionpark.Worktree{cleanParkedWorktree(filepath.Join(root, "checkout"), "topic")}, ParkedAt: time.Unix(11, 0).UTC()}
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	lock, err := store.Acquire(context.Background(), bundle.ParkedSessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	state, err := store.LoadUnderLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "wb.yaml")
	if err := os.WriteFile(config, []byte("session_move:\n  targets:\n    target:\n      default_courier: ssh\n      ssh:\n        host: target.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return parkedResumeRefusalFixture{root: root, store: store, lock: lock, state: state, config: config}
}
func TestSessionResumeRejectsRemoteFlagsWithoutTargetBeforeCustody(t *testing.T) {
	fixture := newPublicResumeRefusalFixture(t)
	if err := fixture.lock.Close(); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--via=ssh", "--config=" + fixture.config} {
		command := resumeCommand(fixture.root, sessionrun.ResumeDependencies{})
		command.SilenceUsage, command.SilenceErrors = true, true
		command.SetArgs([]string{"park-refusal", flag})
		if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "require --to") {
			t.Fatalf("%s: error = %v", flag, err)
		}
	}
	state, err := fixture.store.Load("park-refusal")
	if err != nil {
		t.Fatal(err)
	}
	if state.ResumeRoute != nil || state.Status != sessionpark.StatusParked {
		t.Fatalf("refusal changed state: %+v", state)
	}
}
