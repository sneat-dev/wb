package main

// Pack unit p02 coverage: printWorktreeEnd, writeWorktreeMergeReceipt,
// markCreatedCheckouts. Unit tier only: no real git/gh, no process starts.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/worktreeend"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func TestPkp02PrintWorktreeEndNotApplied(t *testing.T) {
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

func TestPkp02WriteWorktreeMergeReceiptFindings(t *testing.T) {
	t.Parallel()
	receipt := orchestrate.WorktreeMergeReceipt{
		Repository: "sneat-dev/wb",
		Target:     "main",
		Findings: []orchestrate.WorktreeMergeFinding{
			{Code: "c1", Message: "m1"},
		},
	}
	buf := &bytes.Buffer{}
	if err := writeWorktreeMergeReceipt(buf, "text", receipt); err != nil {
		t.Fatalf("writeWorktreeMergeReceipt: %v", err)
	}
	if !strings.Contains(buf.String(), "finding: c1: m1") {
		t.Fatalf("expected finding line, got %q", buf.String())
	}
}

func TestPkp02MarkCreatedCheckoutsEmptyPath(t *testing.T) {
	t.Parallel()
	inv := &invocation{}
	command := &cobra.Command{}
	errBuf := &bytes.Buffer{}
	command.SetErr(errBuf)
	results := []worktrees.CreateResult{
		{Repository: "demo", WorktreeDir: "", CanonicalDir: ""},
	}
	markCreatedCheckouts(inv, command, "main", results)
	if errBuf.Len() != 0 {
		t.Fatalf("expected no warning for an entirely empty checkout path, got %q", errBuf.String())
	}
}
