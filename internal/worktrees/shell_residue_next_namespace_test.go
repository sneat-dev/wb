package worktrees

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestShellResidueNextNamespaceRejectsMalformedNativeName(t *testing.T) {
	t.Parallel()
	task := newHostLevelCleanupTaskFixture(t)
	original := task.taskPath
	task.taskPath = filepath.Join(task.worktreesPath, "invalid task")
	if removeEmptyTaskDirectory(task) {
		t.Fatal("malformed task name granted retirement")
	}
	if info, err := os.Stat(original); err != nil || !info.IsDir() {
		t.Fatalf("held task lost: %v %v", info, err)
	}
	root := t.TempDir()
	invalid := filepath.Join(root, "invalid task")
	if err := os.Mkdir(invalid, 0700); err != nil {
		t.Fatal(err)
	}
	got, err := emptyTaskNamespaces([]wbhome.Layout{{WorktreesRoot: root}}, nil, "", t.TempDir(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("malformed namespace admitted: %+v %v", got, err)
	}
	if _, err := os.Stat(invalid); err != nil {
		t.Fatal(err)
	}
}

func TestShellResidueNextNamespaceRetirementPreservesAcquisitionRefusal(t *testing.T) {
	t.Parallel()
	task := newHostLevelCleanupTaskFixture(t)
	lockPath := filepath.Join(task.taskPath, ".lock")
	before, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := []LifecycleArtifact{{Task: filepath.Base(task.taskPath), WorktreesRoot: task.worktreesPath, Path: task.taskPath, Kind: lifecycleArtifactKindTaskNamespace, Eligible: true}}
	retireEmptyTaskNamespaces(artifacts)
	if artifacts[0].Applied || !strings.Contains(artifacts[0].Reason, "task namespace is in use") {
		t.Fatalf("held task admitted: %+v", artifacts)
	}
	after, err := os.ReadFile(lockPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("held lock altered: %q %v", after, err)
	}
}

func TestShellResidueNextNamespaceRetirementPreservesNativeReleaseRefusal(t *testing.T) {
	t.Parallel()
	root := shellResidueNextPhysicalTemp(t)
	taskPath := filepath.Join(root, "retire-task")
	if err := os.Mkdir(taskPath, 0700); err != nil {
		t.Fatal(err)
	}
	artifacts := []LifecycleArtifact{{Task: "retire-task", WorktreesRoot: root, Path: taskPath, Kind: lifecycleArtifactKindTaskNamespace, Eligible: true}}
	observed := false
	retireEmptyTaskNamespacesObserved(artifacts, func(task *cleanupTaskHandle) {
		observed = true
		if err := task.task.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := task.task.Stat(); err == nil {
			t.Fatal("closed owned task directory did not refuse Stat")
		}
	})
	if !observed || artifacts[0].Applied || !strings.Contains(artifacts[0].Reason, "release task namespace lock") {
		t.Fatalf("release refusal=%+v observed=%v", artifacts, observed)
	}
	if _, err := os.Stat(filepath.Join(taskPath, ".lock")); err != nil {
		t.Fatalf("failed release lost lock: %v", err)
	}
}
