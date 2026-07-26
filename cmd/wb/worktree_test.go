package main

import (
	"strings"
	"testing"
)

func TestWorktreeHelpExplainsCanonicalAndCentralLayout(t *testing.T) {
	command := newWorktreeCreateCmd()
	for _, wanted := range []string{
		"canonical clone must be clean",
		"pulls",
		".wb/worktrees/<task>/<owner>/<repository>",
		"--resume",
	} {
		if !strings.Contains(command.Long, wanted) {
			t.Errorf("worktree create help does not mention %q", wanted)
		}
	}
}
