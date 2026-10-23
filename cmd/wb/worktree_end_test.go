package main

import (
	"bytes"
	"testing"
)

// The production capture must leave the worktree CLEAN — otherwise the
// existing cleanup transaction, which refuses a dirty checkout, could never
// retire it — and the reference it prints must still resolve afterwards.

// The link guard reads both independent signals, so a hand-written go.work
// with no stream record still refuses.

// A live link refuses the verb with exit 2 and names the command that clears
// it, rather than retiring a checkout that builds against an unpublished tree.

// `wb worktree end` on a task WB does not know is an error, not a silent
// success that would let a lane believe it had closed.
func TestWorktreeEndOnAnUnknownTaskFails(t *testing.T) {
	t.Setenv("WB_PROJECTS_ROOT", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := run([]string{"worktree", "end", "no-such-task", "--non-interactive"}, &stdout, &stderr)
	if code == exitOK {
		t.Fatalf("ending an unknown task succeeded; stdout=%s", stdout.String())
	}
}
