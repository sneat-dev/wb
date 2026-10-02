package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLifecycleNextArtifactPreparationRetainsClosedEnumerationCause(t *testing.T) {
	t.Parallel()
	task := newHostLevelCleanupTaskFixture(t)
	home := t.TempDir()
	stage := filepath.Join(task.taskPath, ".wb-stage-native")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	var nativeCause error
	task.afterArtifactAuthorization = func(directory *os.File) {
		if err := directory.Close(); err != nil {
			t.Fatal(err)
		}
		_, nativeCause = directory.Seek(0, 0)
		if nativeCause == nil {
			t.Fatal("closed owned descriptor did not refuse the directory-inspection seek")
		}
	}
	archive, archivePath, handles, err := prepareCleanupLifecycleArtifacts(home, task, []int{0}, []LifecycleArtifact{{Path: stage, Kind: lifecycleArtifactKindStage}})
	if archive != nil || archivePath != "" || handles != nil || err == nil || !strings.Contains(err.Error(), "reinspect cleanup lifecycle artifact") {
		t.Fatalf("preparation=%v %q %v %v", archive, archivePath, handles, err)
	}
	var cause *os.PathError
	if !errors.As(nativeCause, &cause) || !errors.Is(err, cause.Err) {
		t.Fatalf("lost native enumeration cause: %v / %v", nativeCause, err)
	}
	if info, err := os.Stat(stage); err != nil || !info.IsDir() {
		t.Fatalf("closed enumeration lost stage: %v %v", info, err)
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("refusal published archive: %v %v", entries, err)
	}
}

func TestLifecycleNextOpenedArtifactPreservesNativeEnumerationRefusal(t *testing.T) {
	t.Parallel()
	stage := t.TempDir()
	directory, err := openAbsoluteDirectoryNoFollow(stage, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	_, nativeCause := directory.Seek(0, 0)
	if nativeCause == nil {
		t.Fatal("closed owned descriptor did not refuse the directory-inspection seek")
	}
	artifact := inspectOpenedLifecycleArtifact(context.Background(), stage, LifecycleArtifact{Path: stage, Disposition: "cleanup_backlog"}, directory)
	if artifact.Eligible || artifact.Disposition != "cleanup_backlog" || !strings.Contains(artifact.Reason, nativeCause.Error()) {
		t.Fatalf("refusal was promoted or lost: %+v; native=%v", artifact, nativeCause)
	}
	if info, err := os.Stat(stage); err != nil || !info.IsDir() {
		t.Fatalf("inspection changed stage: %v %v", info, err)
	}
}

func TestLifecycleNextArtifactRetainsBareReservedStagePrefixes(t *testing.T) {
	t.Parallel()
	for _, name := range []string{".wb-stage-", ".wb-retired-stage-"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, name)
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 {
				t.Fatalf("native stage entry=%v %v", entries, err)
			}
			artifact, recognized := inspectLifecycleArtifact(context.Background(), root, "task", path, entries[0])
			if !recognized || artifact.Eligible || artifact.Disposition != "cleanup_backlog" || artifact.Reason != "reserved WB stage name has no collision-resistant identity suffix" {
				t.Fatalf("bare reserved stage admitted=%+v %v", artifact, recognized)
			}
			if retained, err := os.ReadDir(path); err != nil || len(retained) != 0 {
				t.Fatalf("refusal changed reserved stage=%v %v", retained, err)
			}
		})
	}
}
