package main

import (
	"strings"
	"testing"
)

// The help is the contract a reviewer reads before reaching for gh pr checkout,
// so it has to say what it replaces and why.
func TestWorktreeReviewHelpNamesWhatItReplaces(t *testing.T) {
	command := newWorktreeReviewCmd()
	for _, wanted := range []string{
		"gh pr checkout",
		"detached HEAD",
		"tracked",
		"wb worktree review end",
		"--sha",
	} {
		if !strings.Contains(command.Long, wanted) {
			t.Errorf("wb worktree review help does not mention %q", wanted)
		}
	}
	for _, flag := range []string{"sha", "ttl", "task", "model", "mode", "format"} {
		if command.Flags().Lookup(flag) == nil {
			t.Errorf("wb worktree review is missing --%s", flag)
		}
	}
	end, _, err := command.Find([]string{"end"})
	if err != nil || end.Name() != "end" {
		t.Fatalf("wb worktree review end is not reachable: %v", err)
	}
	if !strings.Contains(end.Long, "even when it is dirty") {
		t.Error("review end must say that a dirty checkout is still ended, after capture")
	}
}

// A review checkout needs no operator-authored prompt: its originating
// instruction is the pull request. Requiring one would make the tracked path
// harder than the untracked one, which is the whole failure being fixed.
func TestWorktreeReviewRequiresNoOriginalPromptFile(t *testing.T) {
	command := newWorktreeReviewCmd()
	if command.Flags().Lookup("original-prompt-file") != nil {
		t.Fatal("wb worktree review must not require a prompt file the reviewer would have to invent")
	}
}
