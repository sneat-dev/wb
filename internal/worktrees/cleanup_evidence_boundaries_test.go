package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // Each case sets the process-wide home used by WB layout resolution.
func TestTerminalCleanupProofReportRootAndTimestampBoundaries(t *testing.T) {
	for _, scenario := range []string{"absent root", "regular root", "symlink root", "looped parent", "timestamp file", "mismatched timestamp", "foreign result", "missing head"} {
		//nolint:paralleltest // t.Setenv cannot be used in a parallel descendant.
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", filepath.Join(root, "home"))
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
			reports := filepath.Join(root, ".wb", "reports", "worktree-cleanup")
			at := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
			name := at.Format("20060102T150405.000000000Z")
			worktree := filepath.Join(root, ".worktrees", "task", "acme", "app")
			canonical, err := CanonicalRepositoryPath(root, "acme/app")
			if err != nil {
				t.Fatal(err)
			}
			result := CleanupResult{ListResult: ListResult{Task: "task", Repository: "acme/app", WorktreeDir: worktree,
				CanonicalDir: canonical, Branch: "wb/task", Base: "main", HeadSHA: strings.Repeat("a", 40),
				RemoteTargetSHA: strings.Repeat("b", 40), Clean: true, IntegratedAtOrigin: true},
				Eligible: true, Applied: true, WorktreeGone: true, BranchDeleted: true}
			if scenario != "absent root" {
				if err := os.MkdirAll(filepath.Dir(reports), 0o700); err != nil {
					t.Fatal(err)
				}
				switch scenario {
				case "regular root":
					if err := os.WriteFile(reports, []byte("not a directory"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "symlink root":
					other := t.TempDir()
					if err := os.Symlink(other, reports); err != nil {
						t.Fatal(err)
					}
				case "looped parent":
					parent := filepath.Dir(reports)
					if err := os.Remove(parent); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(parent, parent); err != nil {
						t.Fatal(err)
					}
				default:
					if err := os.Mkdir(reports, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(filepath.Join(reports, "not-a-timestamp"), 0o700); err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(reports, name)
					if scenario == "timestamp file" {
						if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
							t.Fatal(err)
						}
					} else {
						if err := os.Mkdir(path, 0o700); err != nil {
							t.Fatal(err)
						}
						report := cleanupReport{GeneratedAt: at, Phase: "applied", Apply: true, Task: "task", Results: []CleanupResult{result}}
						if scenario == "mismatched timestamp" {
							report.GeneratedAt = at.Add(time.Second)
						}
						if scenario == "foreign result" {
							report.Results[0].Repository = "acme/other"
						}
						if scenario == "missing head" {
							report.Results[0].HeadSHA = ""
						}
						encoded, err := json.Marshal(report)
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(path, "cleanup.json"), encoded, 0o600); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			proof, err := FindTerminalCleanupProof(root, "acme/app", "main", "task", worktree, "wb/task")
			if err == nil || proof != nil {
				t.Fatalf("%s accepted: %+v, %v", scenario, proof, err)
			}
		})
	}
}

func TestReadCleanupReportFileRejectsUnreadableRegularFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cleanup.json")
	if err := os.WriteFile(path, []byte(`{}`), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := readCleanupReportFile(path); err == nil {
		t.Fatal("unreadable regular report accepted")
	}
}

func TestResumableBacklogSelectionAndFilesystemBoundaries(t *testing.T) {
	t.Parallel()
	projectsRoot, worktreesRoot, record := wtLogCovBacklogRecord(t, "removed")
	home := t.TempDir()
	selected := map[string]bool{record.Task: true}
	load := func() ([]lifecycleBacklogRecord, []LifecycleBacklogQuarantine, error) {
		return loadResumableLifecycleBacklog(context.Background(), home, projectsRoot, []string{worktreesRoot}, selected, "", "removed")
	}
	if records, quarantine, err := load(); err != nil || len(records) != 0 || len(quarantine) != 0 {
		t.Fatalf("missing backlog = %v/%v/%v", records, quarantine, err)
	}
	if err := os.WriteFile(filepath.Join(home, "reports"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := load(); err == nil {
		t.Fatal("non-directory backlog parent accepted")
	}
	if err := os.Remove(filepath.Join(home, "reports")); err != nil {
		t.Fatal(err)
	}
	record.Stage = lifecycleStageRetiringRemote
	if err := persistLifecycleBacklog(home, &record, record.Stage); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(lifecycleBacklogDirectory(home), "ignored.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lifecycleBacklogDirectory(home), "notes.txt"), []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	if records, quarantine, err := load(); err != nil || len(records) != 1 || len(quarantine) != 0 {
		t.Fatalf("selected incomplete backlog = %v/%v/%v", records, quarantine, err)
	}
	bad := record
	bad.Version++
	bad.ID = strings.Repeat("b", 64)
	plantBacklogRecord(t, home, bad.ID+".json", mustJSON(t, bad))
	if records, quarantine, err := load(); err != nil || len(records) != 1 || len(quarantine) != 1 || quarantine[0].Task != record.Task {
		t.Fatalf("selected invalid backlog = %v/%v/%v", records, quarantine, err)
	}
	if records, quarantine, err := loadResumableLifecycleBacklog(context.Background(), home, projectsRoot,
		[]string{worktreesRoot}, selected, "other/repository", "removed"); err != nil || len(records) != 0 || len(quarantine) != 0 {
		t.Fatalf("foreign filter observed selected backlog = %v/%v/%v", records, quarantine, err)
	}
	secondEntry := ListResult{Task: "other-task", Repository: record.Repository, CanonicalDir: record.CanonicalDir,
		WorktreesRoot: worktreesRoot, WorktreeDir: filepath.Join(worktreesRoot, "other-task", "acme", "app"),
		Branch: "wb/other-task", Base: record.Base, HeadSHA: record.HeadSHA}
	second := newLifecycleBacklogRecord(projectsRoot, secondEntry, "removed")
	if err := persistLifecycleBacklog(home, &second, lifecycleStageRetiringRemote); err != nil {
		t.Fatal(err)
	}
	selected[second.Task] = true
	if records, quarantine, err := load(); err != nil || len(records) != 2 || len(quarantine) != 1 || records[0].ID > records[1].ID {
		t.Fatalf("sorted independent backlogs = %v/%v/%v", records, quarantine, err)
	}
	delete(selected, second.Task)
	if err := os.MkdirAll(record.WorktreeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if records, _, err := load(); err != nil || len(records) != 0 {
		t.Fatalf("present unregistered directory with unreadable canonical = %v/%v", records, err)
	}
	if err := os.RemoveAll(filepath.Join(worktreesRoot, record.Task)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktreesRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	loop := filepath.Join(worktreesRoot, record.Task)
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	if _, _, err := load(); err == nil || !strings.Contains(err.Error(), "inspect lifecycle backlog worktree") {
		t.Fatalf("uninspectable selected worktree was accepted: %v", err)
	}
}

func TestBacklogWriterAndValidatorRejectInvalidState(t *testing.T) {
	t.Parallel()
	_, _, record := wtLogCovBacklogRecord(t, "removed")
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "reports"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := persistLifecycleBacklog(home, &record, lifecycleStageSealed); err == nil {
		t.Fatal("blocked private backlog directory accepted")
	}
	if err := os.Remove(filepath.Join(home, "reports")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(lifecycleBacklogDirectory(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lifecycleBacklogPath(home, record.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := persistLifecycleBacklog(home, &record, lifecycleStageSealed); err == nil {
		t.Fatal("directory occupying immutable backlog file was replaced")
	}
	invalid := record
	invalid.WorktreesRoot = invalid.WorktreeDir
	if err := validateLifecycleBacklog(invalid); err == nil {
		t.Fatal("invalid managed path accepted")
	}
	invalid = record
	invalid.RecoveryKind = "create_work_log_failed"
	invalid.Failure = strings.Repeat("x", 2001)
	if err := validateLifecycleBacklog(invalid); err == nil {
		t.Fatal("oversized recovery detail accepted")
	}
	invalid = newLifecycleBacklogRecord(record.ProjectsRoot, ListResult{Task: record.Task, Repository: record.Repository,
		CanonicalDir: record.CanonicalDir, WorktreesRoot: record.WorktreesRoot, WorktreeDir: record.WorktreeDir,
		Branch: record.Branch, Base: record.Base, HeadSHA: record.HeadSHA}, "unknown")
	if err := validateLifecycleBacklog(invalid); err == nil || !strings.Contains(err.Error(), "disposition") {
		t.Fatalf("unknown disposition accepted: %v", err)
	}
	invalid = newLifecycleBacklogRecord(record.ProjectsRoot, ListResult{Task: record.Task, Repository: record.Repository,
		CanonicalDir: record.CanonicalDir, WorktreesRoot: record.WorktreesRoot, WorktreeDir: record.WorktreeDir,
		Branch: record.Branch, Base: record.Base, HeadSHA: record.HeadSHA}, string(AbortDiscarded))
	invalid.RecoveryKind = "create_work_log_failed"
	if err := validateLifecycleBacklog(invalid); err == nil || !strings.Contains(err.Error(), "create recovery backlog") {
		t.Fatalf("discarded create recovery accepted: %v", err)
	}
}

//nolint:paralleltest // WB home resolution reads process-wide home settings.
func TestDiscardedProofRejectsUnavailableBacklogBoundaries(t *testing.T) {
	for _, scenario := range []string{"projects root loop", "backlog parent file", "worktree ancestor loop"} {
		//nolint:paralleltest // A case may change the process-wide WB home.
		t.Run(scenario, func(t *testing.T) {
			projectsRoot, worktreesRoot, record := wtLogCovBacklogRecord(t, string(AbortDiscarded))
			switch scenario {
			case "projects root loop":
				projectsRoot = filepath.Join(t.TempDir(), "loop")
				if err := os.Symlink(projectsRoot, projectsRoot); err != nil {
					t.Fatal(err)
				}
			case "backlog parent file":
				if err := os.Mkdir(filepath.Join(projectsRoot, ".wb"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(projectsRoot, ".wb", "reports"), []byte("blocked"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "worktree ancestor loop":
				record.Stage = lifecycleStageComplete
				if err := persistLifecycleBacklog(filepath.Join(projectsRoot, ".wb"), &record, record.Stage); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(worktreesRoot, 0o700); err != nil {
					t.Fatal(err)
				}
				loop := filepath.Join(worktreesRoot, record.Task)
				if err := os.Symlink(loop, loop); err != nil {
					t.Fatal(err)
				}
			}
			proof, err := FindDiscardedLifecycleBacklogProof(context.Background(), projectsRoot,
				record.Repository, record.Base, record.Task, record.WorktreeDir, record.Branch, record.HeadSHA)
			if proof != nil || err == nil {
				t.Fatalf("%s accepted: %+v, %v", scenario, proof, err)
			}
		})
	}
}

func TestCleanupArtifactHeldDirectoryRefusesSourceDrift(t *testing.T) {
	t.Parallel()
	home, root := t.TempDir(), t.TempDir()
	taskDir := filepath.Join(root, "task")
	stage := filepath.Join(taskDir, ".wb-stage-one")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	handle := wtLifeCovCleanupTaskHandle(t, root, taskDir)
	t.Cleanup(handle.close)
	foreign := []LifecycleArtifact{{Path: filepath.Join(t.TempDir(), ".wb-stage-one"), Kind: lifecycleArtifactKindStage}}
	if _, _, _, err := prepareCleanupLifecycleArtifacts(home, handle, []int{0}, foreign); err == nil || !strings.Contains(err.Error(), "path changed") {
		t.Fatalf("foreign path accepted: %v", err)
	}
	artifacts := []LifecycleArtifact{{Path: stage, Kind: lifecycleArtifactKindStage}}
	archive, archivePath, handles, err := prepareCleanupLifecycleArtifacts(home, handle, []int{0}, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = archive.Close(); closeCleanupLifecycleArtifacts(handles) })
	if err := handles[0].directory.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archiveCleanupLifecycleArtifacts(handle, archive, archivePath, handles, artifacts); err == nil || !strings.Contains(err.Error(), "reinspect") {
		t.Fatalf("closed held stage accepted: %v", err)
	}
}

func TestCleanupArtifactInspectionPreservesUnknownAndNonemptyStages(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	task := "task"
	taskDir := filepath.Join(root, task)
	if err := os.Mkdir(taskDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ordinary", ".wb-stage-one", ".wb-stage-two"} {
		path := filepath.Join(taskDir, name)
		if name == ".wb-stage-one" {
			if err := os.Symlink(t.TempDir(), path); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(taskDir, ".wb-stage-two", "evidence"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		artifact, found := inspectLifecycleArtifact(context.Background(), root, task, filepath.Join(taskDir, entry.Name()), entry)
		switch entry.Name() {
		case "ordinary":
			if found {
				t.Fatalf("ordinary directory became cleanup artifact: %+v", artifact)
			}
		case ".wb-stage-one":
			if !found || artifact.Eligible || !strings.Contains(artifact.Reason, "no-follow") {
				t.Fatalf("symlink stage = %+v, %t", artifact, found)
			}
		case ".wb-stage-two":
			if !found || artifact.Eligible || !strings.Contains(artifact.Reason, "non-empty") {
				t.Fatalf("nonempty stage = %+v, %t", artifact, found)
			}
		}
	}
}

func TestVacantBacklogRefusesMissingRegistrationAuthority(t *testing.T) {
	t.Parallel()
	_, _, record := wtLogCovBacklogRecord(t, "removed")
	lockErr := errors.New("task namespace is gone")
	if err := completeVacantLifecycleBacklog(context.Background(), t.TempDir(), &record, lockErr); !errors.Is(err, lockErr) {
		t.Fatalf("missing canonical authority = %v", err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
