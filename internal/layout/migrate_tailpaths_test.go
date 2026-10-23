package layout

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMigrationFailureIncludesRelocationOutcome(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"skipped", "failed"} {
		report := MigrateReport{Clones: []MigrateClone{{Status: "done", Relocations: []MigrateRelocation{{Status: status}}}}}
		if !MigrateFailed(report) {
			t.Fatalf("relocation %s must fail the run", status)
		}
	}
}

func TestMigrationManifestHelpersPreservePartialOutcome(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	manifest := &migrationManifest{Clones: []manifestClone{
		{Repository: "one/a", Source: "/legacy/a", Destination: "/host/a"},
		{Repository: "two/b", Source: "/legacy/b", Destination: "/host/b"},
	}}
	remaining := manifestUnprocessedClones(manifest, 1)
	if len(remaining) != 1 || remaining[0].Repository != "two/b" || remaining[0].Source != "/host/b" || remaining[0].Destination != "/legacy/b" || remaining[0].Status != "skipped" {
		t.Fatalf("unprocessed manifest tail = %+v", remaining)
	}
	updateManifestClone(manifest, MigrateClone{Source: "/legacy/a", Destination: "/host/a", Status: "done", IncludedTasks: []string{"task"}}, now)
	if got := manifest.Clones[0]; got.Status != "done" || got.CompletedAt == nil || !got.CompletedAt.Equal(now) || len(got.IncludedTasks) != 1 {
		t.Fatalf("completed manifest entry = %+v", got)
	}
	// A failed or skipped move must not add inclusions to an undo record.
	updateManifestClone(manifest, MigrateClone{Source: "/legacy/b", Destination: "/host/b", Status: "failed", IncludedTasks: []string{"task"}}, now)
	if len(manifest.Clones[1].IncludedTasks) != 0 {
		t.Fatalf("failed move recorded included tasks: %+v", manifest.Clones[1])
	}
}

func TestMigrationManifestReadAndWriteErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(path); err == nil {
		t.Fatal("malformed manifest must be rejected")
	}
	// time.Time refuses to marshal years outside JSON's four-digit range.
	bad := &migrationManifest{CreatedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := writeManifest(path, bad); err == nil {
		t.Fatal("unserializable manifest must be rejected before writing")
	}
	blockedRoot := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blockedRoot, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := createMigrationManifest(blockedRoot, nil, time.Now()); err == nil {
		t.Fatal("file at projects root must refuse manifest creation")
	}
}

func TestMigrationUndoPlansAndReportsManifestStates(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	id := "20260927T120000Z-test"
	path := filepath.Join(root, ".wb", migrationsDirName, id, "manifest.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := &migrationManifest{Clones: []manifestClone{
		{Repository: "acme/done", Source: filepath.Join(root, "acme", "done"), Destination: filepath.Join(root, "github.com", "acme", "done"), Status: "done"},
		{Repository: "acme/unfinished", Source: filepath.Join(root, "acme", "unfinished"), Destination: filepath.Join(root, "github.com", "acme", "unfinished"), Status: "planned"},
		{Repository: "acme/reversed", Source: filepath.Join(root, "acme", "reversed"), Destination: filepath.Join(root, "github.com", "acme", "reversed"), Status: "done", Reversed: true},
		{Repository: "acme/relocated", Source: filepath.Join(root, "github.com", "acme", "relocated"), Destination: filepath.Join(root, "github.com", "acme", "relocated"), Status: "already_done", Relocations: []manifestRelocation{{Task: "task", Source: "/old/task", Destination: "/new/task", Status: "done"}}},
	}}
	if err := writeManifest(path, manifest); err != nil {
		t.Fatal(err)
	}
	report, err := Migrate(context.Background(), root, MigrateOptions{UndoID: id})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Clones) != 4 || report.Clones[0].Status != "planned" || report.Clones[1].Status != "skipped" || report.Clones[2].Status != "skipped" || report.Clones[3].Status != "reversed" {
		t.Fatalf("undo dry-run states = %+v", report.Clones)
	}
	reloc := report.Clones[3].Relocations
	if len(reloc) != 1 || reloc[0].Source != "/new/task" || reloc[0].Destination != "/old/task" || reloc[0].Status != "planned" {
		t.Fatalf("undo relocation plan = %+v", reloc)
	}
}

func TestMigrationUndoRefusesChangedCloneState(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	id := "20260927T120000Z-refusal"
	path := filepath.Join(root, ".wb", migrationsDirName, id, "manifest.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	clonePath := filepath.Join(root, "github.com", "acme", "repo")
	// An absent clone cannot pass the immediate pre-undo Git-state check.
	manifest := &migrationManifest{Clones: []manifestClone{{Repository: "acme/repo", Source: filepath.Join(root, "acme", "repo"), Destination: clonePath, Status: "done"}}}
	if err := writeManifest(path, manifest); err != nil {
		t.Fatal(err)
	}
	report, err := Migrate(context.Background(), root, MigrateOptions{UndoID: id, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Clones) != 1 || report.Clones[0].Status != "skipped" || !strings.Contains(report.Clones[0].Reason, "refused immediately before move back") {
		t.Fatalf("undo refusal = %+v", report.Clones)
	}
}

func TestMigrationUndoLeavesCloneWhenRelocationCannotReverse(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	id := "20260927T120000Z-relocation-refusal"
	path := filepath.Join(root, ".wb", migrationsDirName, id, "manifest.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// The checkout was removed after migration. Undo must record the failed
	// pre-move check and leave the clone move untouched for a later retry.
	manifest := &migrationManifest{Clones: []manifestClone{{
		Repository: "acme/repo", Source: filepath.Join(root, "acme", "repo"),
		Destination: filepath.Join(root, "github.com", "acme", "repo"), Status: "done",
		Relocations: []manifestRelocation{{Task: "task", Source: filepath.Join(root, "old", "task"), Destination: filepath.Join(root, "missing", "task"), Status: "done"}},
	}}}
	if err := writeManifest(path, manifest); err != nil {
		t.Fatal(err)
	}
	report, err := Migrate(context.Background(), root, MigrateOptions{UndoID: id, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Clones) != 1 || report.Clones[0].Status != "skipped" || len(report.Clones[0].Relocations) != 1 || report.Clones[0].Relocations[0].Status != "skipped" || !strings.Contains(report.Clones[0].Relocations[0].Reason, "refused immediately before relocation reversal") {
		t.Fatalf("undo relocation refusal = %+v", report.Clones)
	}
	got, err := readManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Clones[0].Reversed || got.Clones[0].Relocations[0].Reversed {
		t.Fatalf("refused undo marked manifest as reversed: %+v", got.Clones[0])
	}
}

func TestMigrationIDRejectsEmptyAndTraversalSegments(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"", ".", "..", "a/b", `a\b`} {
		if err := validateMigrationID(id); err == nil {
			t.Errorf("id %q unexpectedly accepted", id)
		}
	}
}

func TestEmptyRelocationDirectoryCleanupStopsAtNonemptyTask(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	taskDir := filepath.Join(root, ".worktrees", "task")
	destination := filepath.Join(taskDir, "github.com", "acme", "repo")
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(taskDir, "keep")
	if err := os.WriteFile(keep, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	removeEmptyRelocationDirectories(root, "task", destination)
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("cleanup removed nonempty task directory: %v", err)
	}
	removeEmptyRelocationDirectories(root, "", destination)
}

func TestRemoveEmptyLegacyOwnerReportsInspectionAndNonemptyDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if removed, reason := removeEmptyLegacyOwner(filepath.Join(root, "missing")); removed || !strings.Contains(reason, "cannot inspect") {
		t.Fatalf("missing owner = (%v, %q)", removed, reason)
	}
	path := filepath.Join(root, "owner")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if removed, reason := removeEmptyLegacyOwner(path); removed || !strings.Contains(reason, "child") {
		t.Fatalf("nonempty owner = (%v, %q)", removed, reason)
	}
}
