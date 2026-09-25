package main

import (
	"bytes"
	"path/filepath"
	"testing"
)

// Every existing offload test builds a fake taskOffloadDependencies, never
// the real one newTaskCmd wires up; this proves the real store and launch
// closures themselves.
func TestDefaultTaskOffloadDependenciesStoreOpensUnderProjectsRoot(t *testing.T) {
	root := t.TempDir()
	deps := defaultTaskOffloadDependencies(&invocation{projectsRoot: root})
	if _, err := deps.store(); err != nil {
		t.Fatalf("default task-offload store: %v", err)
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
