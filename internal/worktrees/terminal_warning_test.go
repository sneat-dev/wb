package worktrees

import "testing"

func TestTerminalHistoryPortsWarnSkippedEvidenceOnStderr(t *testing.T) {
	t.Parallel()
	ports := terminalHistoryPorts()
	if ports.Warn == nil {
		t.Fatal("production terminal history has no warning sink, so a skipped binding would vanish")
	}
	// The sink writes one prefixed line to stderr and returns.
	ports.Warn("skipped malformed pull request binding x of task other while reading the terminal Work Log of task t")
}
