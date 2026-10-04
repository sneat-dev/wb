package main

// Pack unit p02 coverage: printWorktreeEnd, writeWorktreeMergeReceipt,
// markCreatedCheckouts. Unit tier only: no real git/gh, no process starts.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktreeend"
	"github.com/spf13/cobra"
)

func TestPrintWorktreeEndNotApplied(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	buf := &bytes.Buffer{}
	command.SetOut(buf)
	result := worktreeend.Result{Task: "demo", Applied: false}
	if err := printWorktreeEnd(command, "text", result); err != nil {
		t.Fatalf("printWorktreeEnd: %v", err)
	}
	if !strings.Contains(buf.String(), "nothing was changed; re-run with --apply") {
		t.Fatalf("expected re-run hint, got %q", buf.String())
	}
}
