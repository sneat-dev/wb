package main

import (
	"strings"
	"testing"
	"time"
)

func TestWorktreeHelpExplainsCanonicalAndCentralLayout(t *testing.T) {
	command := newWorktreeCreateCmd()
	for _, wanted := range []string{
		"canonical clone must be clean",
		"pulls",
		"<wb-home>/worktrees/<task>/<owner>/<repository>",
		"WB_HOME",
		"--resume",
	} {
		if !strings.Contains(command.Long, wanted) {
			t.Errorf("worktree create help does not mention %q", wanted)
		}
	}
}

func TestWorktreeCleanupDefaultsToSafeDryRun(t *testing.T) {
	command := newWorktreeCleanupCmd()
	olderThan := command.Flags().Lookup("older-than")
	if olderThan == nil || olderThan.DefValue != (24*time.Hour).String() {
		t.Fatalf("--older-than default = %#v, want %s", olderThan, 24*time.Hour)
	}
	apply := command.Flags().Lookup("apply")
	if apply == nil || apply.DefValue != "false" {
		t.Fatalf("--apply default = %#v, want false", apply)
	}
	if command.Flags().Lookup("report-dir") == nil {
		t.Fatal("cleanup command has no --report-dir")
	}
	if err := command.Args(command, nil); err == nil || !strings.Contains(err.Error(), "--all-merged") {
		t.Fatalf("cleanup without selection error = %v", err)
	}
}

func TestWorktreeLifecycleHelpExplainsNetworkAndCleanupSafety(t *testing.T) {
	list := newWorktreeListCmd()
	for _, wanted := range []string{"only local Git data", "--github", "exact-head"} {
		if !strings.Contains(list.Long, wanted) {
			t.Errorf("worktree list help does not mention %q", wanted)
		}
	}
	cleanup := newWorktreeCleanupCmd()
	for _, wanted := range []string{"default is a dry-run", "recorded head match", "force-with-lease"} {
		if !strings.Contains(cleanup.Long, wanted) {
			t.Errorf("worktree cleanup help does not mention %q", wanted)
		}
	}
}
