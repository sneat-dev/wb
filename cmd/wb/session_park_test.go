package main

import (
	"errors"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

func snapshotTrees(t *testing.T, roots ...string) map[string][]byte {
	t.Helper()
	return testenv.SnapshotTrees(t, roots...)
}

// cwWtErrorReader remains a real fixture for the worktree finalization input test.
type cwWtErrorReader struct{}

func (cwWtErrorReader) Read([]byte) (int, error) { return 0, errors.New("cwWt: injected read failure") }
