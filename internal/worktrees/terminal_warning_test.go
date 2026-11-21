package worktrees

import (
	"bytes"
	"testing"
)

func TestTerminalHistoryWarningsArePrefixedLinesOnTheirSink(t *testing.T) {
	t.Parallel()
	if terminalHistoryPorts().Warn == nil {
		t.Fatal("production terminal history has no warning sink, so a skipped binding would vanish")
	}
	var out bytes.Buffer
	warnTo(&out)("skipped malformed pull request binding x.pull_request.json of task other")
	if got := out.String(); got != "wb: warning: skipped malformed pull request binding x.pull_request.json of task other\n" {
		t.Fatalf("warning line = %q", got)
	}
}
