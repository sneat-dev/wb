package hooks

import "testing"

// TestShouldReplicateStdinNeverDrainsATerminal is the regression guard for a
// command that hangs with no output: `wb hooks run pre-push` with more than one
// composed block used to call io.ReadAll(os.Stdin) unconditionally, so running
// it anywhere other than from git — by hand, or from an agent — blocked forever
// waiting for input nobody had been asked to provide.
func TestShouldReplicateStdinNeverDrainsATerminal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		hook       string
		blocks     int
		isTerminal bool
		want       bool
	}{
		{"composed pre-push from git drains the ref list", "pre-push", 2, false, true},
		{"composed pre-push at a terminal must not block", "pre-push", 2, true, false},
		{"single-block pre-push needs no replication", "pre-push", 1, false, false},
		{"single-block pre-push at a terminal", "pre-push", 1, true, false},
		{"no blocks at all", "pre-push", 0, false, false},
		{"pre-commit receives no stdin", "pre-commit", 3, false, false},
		{"commit-msg receives no stdin", "commit-msg", 2, false, false},
	}
	for _, test := range tests {
		if got := shouldReplicateStdin(test.hook, test.blocks, test.isTerminal); got != test.want {
			t.Errorf("%s: shouldReplicateStdin(%q, %d, %v) = %v, want %v",
				test.name, test.hook, test.blocks, test.isTerminal, got, test.want)
		}
	}
}
