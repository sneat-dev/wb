package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/taskoffload"
)

// Every existing offload test builds a fake taskOffloadDependencies, never
// the real one newTaskCmd wires up; this proves the real store and launch
// closures themselves.
func TestDefaultTaskOffloadDependenciesStoreOpensUnderProjectsRoot(t *testing.T) {
	root := t.TempDir()
	deps := defaultTaskOffloadDependencies(&invocation{projectsRoot: root})
	store, err := deps.store()
	if err != nil {
		t.Fatalf("default task-offload store: %v", err)
	}
	// A dropped or empty projectsRoot (mutation M13, sneat-dev/wb#760 review
	// B5: wbhome.Root("")) resolves this store under the operator's real WB
	// home instead of the fixture; asserting only "no error" cannot tell the
	// two apart. The store's own root must actually live under this test's
	// isolated root, and never under the wbhome default's own directory name.
	if !strings.HasPrefix(store.Root, root) {
		t.Fatalf("task-offload store root = %q, want it under the fixture root %q", store.Root, root)
	}
	if !strings.HasSuffix(store.Root, taskoffload.DirName) {
		t.Fatalf("task-offload store root = %q, want it to end in %q", store.Root, taskoffload.DirName)
	}
}

func TestDefaultTaskOffloadDependenciesLaunchDelegatesToSessionMove(t *testing.T) {
	root := t.TempDir()
	deps := defaultTaskOffloadDependencies(&invocation{projectsRoot: root})
	command := newTaskOffloadCmdWithDeps(&invocation{projectsRoot: root}, deps, false)
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	err := deps.launch(command, taskLaunchRequest{
		WorktreeDir: filepath.Join(root, "no-such-worktree"),
		ContextFile: filepath.Join(root, "handover.md"),
	})
	if err == nil {
		t.Fatal("launch against a nonexistent worktree unexpectedly succeeded")
	}
}
