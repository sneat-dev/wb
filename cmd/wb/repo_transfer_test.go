package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRepoTransferCleanupRejectsAReceiptOutsideTheCleanupDirectory(t *testing.T) {
	root := t.TempDir()
	stray := root + "/not-a-real-receipt.json"
	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newRepoCmd(&invocation{projectsRoot: root}) }, "transfer", "cleanup", "--receipt", stray)
	if err == nil || !strings.Contains(err.Error(), "cleanup receipt must be") {
		t.Fatalf("wb repo transfer cleanup --receipt %s = err=%v stdout=%q, want a pending-receipt refusal", stray, err, stdout)
	}
}
