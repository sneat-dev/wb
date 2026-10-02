//go:build e2e && (darwin || linux)

package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"syscall"
)

func TestE2ELifecycleNextArtifactArchiveRetainsSourceRecreation(t *testing.T) {
	t.Parallel()
	task := newHostLevelCleanupTaskFixture(t)
	stage := filepath.Join(task.taskPath, ".wb-stage-native")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	artifacts := []LifecycleArtifact{{Path: stage, Kind: lifecycleArtifactKindStage}}
	archive, archivePath, handles, err := prepareCleanupLifecycleArtifacts(t.TempDir(), task, []int{0}, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = archive.Close(); closeCleanupLifecycleArtifacts(handles) })
	before, err := os.Stat(stage)
	if err != nil {
		t.Fatal(err)
	}
	observed := false
	task.afterArtifactMove = func() {
		observed = true
		if err := os.Mkdir(stage, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	err = archiveCleanupLifecycleArtifacts(task, archive, archivePath, handles, artifacts)
	if !observed || !errors.Is(err, errDirectoryMoveIdentityChanged) {
		t.Fatalf("source recreation cause=%v observed=%v", err, observed)
	}
	moved, err := os.Stat(filepath.Join(archivePath, filepath.Base(stage)))
	if err != nil || !os.SameFile(before, moved) {
		t.Fatalf("original stage not retained in archive: %v %v", moved, err)
	}
	replacement, err := os.Stat(stage)
	if err != nil || os.SameFile(before, replacement) {
		t.Fatalf("successor source removed or confused: %v %v", replacement, err)
	}
	if artifacts[0].Applied || artifacts[0].ArchivePath != "" {
		t.Fatalf("failed identity corroboration published success: %+v", artifacts[0])
	}
}

func TestE2ELifecycleNextCleanupRejectsUntraversableRawPath(t *testing.T) {
	t.Parallel()
	task := newHostLevelCleanupTaskFixture(t)
	owner := filepath.Join(task.taskPath, "owner")
	repository := filepath.Join(owner, "app")
	blocker := filepath.Join(owner, "blocker")
	if err := os.MkdirAll(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	bytes := []byte("retained regular blocker")
	if err := os.WriteFile(blocker, bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	// Preserve the actual navigation: filepath.Join would clean away the blocker.
	raw := owner + "/blocker/../app"
	if _, err := os.Lstat(raw); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("native untraversable raw-path prerequisite=%v", err)
	}
	handle, err := openCleanupWorktree(task, CleanupResult{ListResult: ListResult{WorktreeDir: raw}})
	if handle != nil || err == nil || !strings.Contains(err.Error(), "cleanup worktree path changed: "+raw) {
		t.Fatalf("raw path admitted=%v %v", handle, err)
	}
	if after, err := os.ReadFile(blocker); err != nil || string(after) != string(bytes) {
		t.Fatalf("refusal changed native blocker: %q %v", after, err)
	}
	if err := task.validate(); err != nil {
		t.Fatalf("refusal invalidated borrowed task authority: %v", err)
	}
	valid, err := openCleanupWorktree(task, CleanupResult{ListResult: ListResult{WorktreeDir: repository}})
	if err != nil {
		t.Fatalf("clean native path refused after failure: %v", err)
	}
	valid.close()
	if entries, err := os.ReadDir(repository); err != nil || len(entries) != 0 {
		t.Fatalf("refusal mutated owned checkout: %v %v", entries, err)
	}
}
