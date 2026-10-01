//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

//nolint:paralleltest // newGitFixture changes process-wide Git environment for the interrupted native journey.
func TestE2ERenameReservationAbortRefusesConflictingTerminalWithoutMovingSource(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "preapply-old", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	originalHead := gitTestOutput(t, created[0].WorktreeDir, "rev-parse", "HEAD")
	prompt := filepath.Join(t.TempDir(), "recycle-prompt.txt")
	promptBytes := []byte("keep this exact request\n")
	if err := os.WriteFile(prompt, promptBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Rename(context.Background(), RenameOptions{
		ProjectsRoot: fixture.projectsRoot, OldTask: "preapply-old", NewTask: "preapply-new",
		DeleteRemote: true, Apply: true,
		WorkLog:                  WorkLogOptions{Model: "unknown", OriginalPrompt: prompt, RequireOriginalPrompt: true},
		afterPreApplyReservation: func() error { return errors.New("interrupted before claim publication") },
	})
	if err == nil || !strings.Contains(err.Error(), "interrupted before claim publication") {
		t.Fatalf("interrupted rename error = %v", err)
	}
	reservations, err := findPreApplyRenameReservations(fixture.home, "preapply-new")
	if err != nil || len(reservations) != 1 {
		t.Fatalf("reserved prompt = %#v, %v", reservations, err)
	}
	runDir, _, err := openWorkLogRun(fixture.home, reservations[0].EffortID, reservations[0].RunID, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONImmutableAt(runDir, preApplyRenameTerminalName, preApplyRenameTerminal{Version: 2}, false); err != nil {
		_ = runDir.Close()
		t.Fatal(err)
	}
	archivePath := filepath.Join(fixture.home, "worklogs", reservations[0].EffortID, "runs", reservations[0].RunID, "original-prompt.txt")
	_ = runDir.Close()
	result, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "preapply-new", Disposition: AbortDiscarded, Apply: true,
	})
	if err == nil || len(result) != 1 || result[0].Applied {
		t.Fatalf("conflicting terminal abort = %#v, %v", result, err)
	}
	if _, err := os.Stat(created[0].WorktreeDir); err != nil {
		t.Fatalf("source checkout moved on terminal refusal: %v", err)
	}
	if head := gitTestOutput(t, created[0].WorktreeDir, "rev-parse", "HEAD"); head != originalHead {
		t.Fatalf("source HEAD changed on terminal refusal: %q, want %q", head, originalHead)
	}
	archived, err := os.ReadFile(archivePath)
	if err != nil || string(archived) != string(promptBytes) {
		t.Fatalf("immutable archived prompt = %q, %v", archived, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.canonical, ".worktrees", "preapply-new")); !os.IsNotExist(err) {
		t.Fatalf("destination checkout exists after refusal: %v", err)
	}
}
