//go:build darwin || linux

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetiredStageInventoryMatchesRequiresEveryRecordedProperty(t *testing.T) {
	result := RetiredStageRecoveryResult{ContentDigest: "digest", FileCount: 2, ByteCount: 3, SymlinkCount: 1}
	matching := stageContentInventory{Digest: "digest", Files: 2, Bytes: 3, Symlinks: 1}
	if !retiredStageInventoryMatches(result, matching) {
		t.Fatal("matching inventory was rejected")
	}
	for _, changed := range []stageContentInventory{
		{Digest: "other", Files: 2, Bytes: 3, Symlinks: 1},
		{Digest: "digest", Files: 3, Bytes: 3, Symlinks: 1},
		{Digest: "digest", Files: 2, Bytes: 4, Symlinks: 1},
		{Digest: "digest", Files: 2, Bytes: 3, Symlinks: 2},
	} {
		if retiredStageInventoryMatches(result, changed) {
			t.Fatalf("changed inventory was accepted: %#v", changed)
		}
	}
}

func TestApplyRetiredStageRecoveryPreservesDriftedIdentityAndExistingArchive(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		prepare func(t *testing.T, home string, result *RetiredStageRecoveryResult)
		want    string
	}{
		{
			name: "content drift",
			prepare: func(t *testing.T, _ string, result *RetiredStageRecoveryResult) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(result.Path, "new-evidence"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "changed after inventory",
		},
		{
			name: "identity drift",
			prepare: func(t *testing.T, _ string, result *RetiredStageRecoveryResult) {
				t.Helper()
				original := result.Path + "-inspected"
				if err := os.Rename(result.Path, original); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(result.Path, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(result.Path, "evidence"), []byte("reviewed"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "identity changed before recovery",
		},
		{
			name: "existing deterministic archive",
			prepare: func(t *testing.T, home string, result *RetiredStageRecoveryResult) {
				t.Helper()
				if err := os.MkdirAll(retiredStageArchivePath(home, *result), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: "deterministic archive already exists",
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			projectsRoot, worktreesRoot := setUpShellRetirementFixture(t)
			task := "batch8-stage"
			stage := filepath.Join(worktreesRoot, task, ".wb-retired-stage-0123456789abcdef0123456789abcdef")
			if err := os.MkdirAll(stage, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(stage, "evidence"), []byte("reviewed"), 0o600); err != nil {
				t.Fatal(err)
			}
			result := inspectRetiredStage(context.Background(), worktreesRoot, task, stage, filepath.Base(stage))
			if !result.Eligible {
				t.Fatalf("planned stage = %#v", result)
			}
			scenario.prepare(t, filepath.Join(projectsRoot, ".wb"), &result)
			applyRetiredStageRecovery(filepath.Join(projectsRoot, ".wb"), &result)
			if result.Applied || result.Eligible || !strings.Contains(result.Reason, scenario.want) {
				t.Fatalf("recovery result = %#v, want %q", result, scenario.want)
			}
			if _, err := os.Lstat(stage); err != nil {
				t.Fatalf("ambiguous stage was removed: %v", err)
			}
		})
	}
}

func TestRetirementRecoveryRefusesUnsafeGCRootAndShellRecheck(t *testing.T) {
	t.Run("gc root is not a directory", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(root, []byte("not a root"), 0o600); err != nil {
			t.Fatal(err)
		}
		artifacts := []LifecycleArtifact{{
			WorktreesRoot: root, Path: filepath.Join(root, ".wb-retired-stage-0123456789abcdef0123456789abcdef"),
			Kind: lifecycleArtifactKindStage, State: "quarantined", Disposition: dispositionEmptyUnscopedLocalRetiredStage, Eligible: true,
		}}
		retireEmptyUnscopedLocalStages(artifacts)
		if artifacts[0].Applied || artifacts[0].Eligible || !strings.Contains(artifacts[0].Reason, "open canonical-local") {
			t.Fatalf("unsafe root artifact = %#v", artifacts[0])
		}
	})

	t.Run("shell gains live repository before apply", func(t *testing.T) {
		_, worktreesRoot := setUpShellRetirementFixture(t)
		task := "batch8-shell"
		path := filepath.Join(worktreesRoot, task)
		if err := os.MkdirAll(filepath.Join(path, "sneat-co", "wb"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "sneat-co", "wb", "live"), []byte("do not remove"), 0o600); err != nil {
			t.Fatal(err)
		}
		result := RetiredShell{WorktreesRoot: worktreesRoot, Task: task, Path: path, Eligible: true}
		applyTaskShellRetirement(&result)
		if result.Applied || result.Error != "" || !strings.Contains(result.Reason, "not empty") {
			t.Fatalf("shell result = %#v", result)
		}
		if _, err := os.Stat(filepath.Join(path, "sneat-co", "wb", "live")); err != nil {
			t.Fatalf("live repository changed: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(path, ".lock")); !os.IsNotExist(err) {
			t.Fatalf("no-op shell recheck retained lock: %v", err)
		}
	})
}

func TestRetirementRecoveryAppliesExactReviewedDirectories(t *testing.T) {
	t.Run("recovery moves inspected stage into its deterministic archive", func(t *testing.T) {
		projectsRoot, worktreesRoot := setUpShellRetirementFixture(t)
		task := "batch8-apply"
		stage := filepath.Join(worktreesRoot, task, ".wb-retired-stage-0123456789abcdef0123456789abcdef")
		if err := os.MkdirAll(stage, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, "evidence"), []byte("preserve"), 0o600); err != nil {
			t.Fatal(err)
		}
		result := inspectRetiredStage(context.Background(), worktreesRoot, task, stage, filepath.Base(stage))
		if !result.Eligible {
			t.Fatalf("planned stage = %#v", result)
		}
		applyRetiredStageRecovery(filepath.Join(projectsRoot, ".wb"), &result)
		if !result.Applied || result.Disposition != "archived_retired_stage" {
			t.Fatalf("recovery result = %#v", result)
		}
		if data, err := os.ReadFile(filepath.Join(result.ArchivePath, "evidence")); err != nil || string(data) != "preserve" {
			t.Fatalf("archived evidence = %q, %v", data, err)
		}
		if _, err := os.Lstat(stage); !os.IsNotExist(err) {
			t.Fatalf("recovered stage remains: %v", err)
		}
	})

	t.Run("shell removes an exact empty owner namespace", func(t *testing.T) {
		_, worktreesRoot := setUpShellRetirementFixture(t)
		task := "batch8-empty-shell"
		path := filepath.Join(worktreesRoot, task)
		if err := os.MkdirAll(filepath.Join(path, "sneat-co", "wb"), 0o700); err != nil {
			t.Fatal(err)
		}
		result := RetiredShell{WorktreesRoot: worktreesRoot, Task: task, Path: path, Eligible: true}
		applyTaskShellRetirement(&result)
		if !result.Applied || result.Reason != "retired empty WB task shell" {
			t.Fatalf("shell result = %#v", result)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("retired shell remains: %v", err)
		}
	})
}
