package worktrees

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromptReservationUsesOneGeneratedRunForArchiveAndReceipt(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	options, err := (WorkLogOptions{EffortID: "destination", Model: "unknown"}).WithOriginalPromptFromStdin([]byte("exact generated-run prompt\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reservePreApplyRenameWorkLog(home, "source", "destination", options); err != nil {
		t.Fatal(err)
	}
	runs, err := os.ReadDir(filepath.Join(home, "worklogs", "destination", "runs"))
	if err != nil || len(runs) != 1 {
		t.Fatalf("one reserved run: %v %v", runs, err)
	}
	candidates, err := findPreApplyRenameReservations(home, "destination")
	if err != nil || len(candidates) != 1 || candidates[0].RunID != runs[0].Name() || candidates[0].PromptSHA256 != options.snapshot.Digest {
		t.Fatalf("archive and receipt identity: %+v %v", candidates, err)
	}
	archive, err := os.ReadFile(filepath.Join(home, "worklogs", "destination", "runs", runs[0].Name(), "original-prompt.txt"))
	if err != nil || string(archive) != "exact generated-run prompt\n" {
		t.Fatalf("exact immutable archive: %q %v", archive, err)
	}
}

func TestPromptReservationRunPreservesErrorsAndNoPromptNoop(t *testing.T) {
	t.Parallel()
	for _, reserve := range []struct {
		name string
		call func(string, WorkLogOptions) error
	}{
		{"archive", func(home string, options WorkLogOptions) error {
			return reserveOriginalPromptArchive(home, "destination", options)
		}},
		{"rename", func(home string, options WorkLogOptions) error {
			return reservePreApplyRenameWorkLog(home, "source", "destination", options)
		}},
	} {
		t.Run(reserve.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			if err := reserve.call(home, WorkLogOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(home, "worklogs")); !os.IsNotExist(err) {
				t.Fatalf("no-prompt reservation created namespace: %v", err)
			}
			options, err := (WorkLogOptions{EffortID: "destination", RunID: "run", Model: "unknown"}).WithOriginalPromptFromStdin([]byte("preserve prompt\n"))
			if err != nil {
				t.Fatal(err)
			}
			invalid := options
			invalid.EffortID = "../outside"
			if err := reserve.call(home, invalid); err == nil || !strings.Contains(err.Error(), "safe path segment") {
				t.Fatalf("invalid effort accepted: %v", err)
			}
			if err := os.WriteFile(filepath.Join(home, "worklogs"), []byte("occupied"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := reserve.call(home, options); err == nil {
				t.Fatal("occupied namespace accepted")
			}
			if content, err := os.ReadFile(filepath.Join(home, "worklogs")); err != nil || string(content) != "occupied" {
				t.Fatalf("occupant changed: %q %v", content, err)
			}
			failedHome := t.TempDir()
			missing := WorkLogOptions{EffortID: "destination", RunID: "run", Model: "unknown", OriginalPrompt: filepath.Join(failedHome, "missing-prompt"), RequireOriginalPrompt: true}
			if err := reserve.call(failedHome, missing); err == nil {
				t.Fatal("missing prompt accepted")
			}
			// A failed archive must allow a later valid reservation in the same run.
			if err := reserve.call(failedHome, options); err != nil {
				t.Fatalf("valid reservation after missing prompt: %v", err)
			}
		})
	}
}
