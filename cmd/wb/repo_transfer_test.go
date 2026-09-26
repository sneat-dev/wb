package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRepoTransferCleanupRequiresReceiptAsUsage(t *testing.T) {
	root := t.TempDir()
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newRepoTransferCleanupCmd(&invocation{projectsRoot: root}) }); err == nil || !strings.Contains(err.Error(), "--receipt is required") {
		t.Fatalf("wb repo transfer cleanup without --receipt = %v, want a usage refusal", err)
	}
}

func TestRepoTransferCleanupRejectsAReceiptOutsideTheCleanupDirectory(t *testing.T) {
	root := t.TempDir()
	stray := root + "/not-a-real-receipt.json"
	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newRepoTransferCleanupCmd(&invocation{projectsRoot: root}) }, "--receipt", stray)
	if err == nil || !strings.Contains(err.Error(), "cleanup receipt must be") {
		t.Fatalf("wb repo transfer cleanup --receipt %s = err=%v stdout=%q, want a pending-receipt refusal", stray, err, stdout)
	}
}
