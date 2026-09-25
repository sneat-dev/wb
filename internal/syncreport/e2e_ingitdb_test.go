//go:build e2e

// This file exercises ValidatePath/Install against the real, installed
// ingitdb CLI (internal/runner.Real). It is e2e-tagged, not
// default-tier: task-8's runtime guard (internal/runner/guard.go) refuses to
// start a real process from an ordinary `go test` binary, and this test's
// premise -- a real ingitdb on PATH -- cannot be satisfied through
// runnertest.Fake.
package syncreport

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestValidateInGitDBAcceptsGeneratedCollection(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("ingitdb"); err != nil {
		t.Skip("ingitdb is not installed")
	}
	raw := strings.Replace(strings.Replace(validRecord, "sync-20260908T145950Z", "sync-a", 1), "sneat-co/schoolus", "acme/app", 1)
	record, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	record.Raw = []byte(raw)
	directory := t.TempDir()
	if err := Install(directory, Report{ID: "sync-a", Records: []Record{record}}); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePath(context.Background(), directory); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("ingitdb", "list", "collections", "--path", directory)
	output, err := command.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != CollectionID {
		t.Fatalf("list collections = %q, %v", output, err)
	}
}
