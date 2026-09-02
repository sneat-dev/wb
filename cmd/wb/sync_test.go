package main

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/fleetsync"
)

func TestPrintSyncSummaryReportsFreshRemoteUpdates(t *testing.T) {
	var out bytes.Buffer
	printSyncSummary(&out, []fleetsync.Result{
		{Status: fleetsync.Pulled, PullAttempted: true, PullSucceeded: true, Updated: true},
		{Status: fleetsync.Pulled, PullAttempted: true, PullSucceeded: true},
		{Status: fleetsync.Unpushed, PullAttempted: true, PullSucceeded: true, Updated: true},
		{Status: fleetsync.Pulled, PullPlanned: true},
	}, false)
	for _, want := range []string{
		"Final outcomes",
		"Pulled              3",
		"Pull actions",
		"Pull planned        1",
		"Pull attempted      3",
		"Pull succeeded      3",
		"Updated from remote 2",
		"Already current     1",
		"Attention",
		"Failures",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("summary %q does not contain %q", out.String(), want)
		}
	}
	sections := []string{"Final outcomes", "Pull actions", "Attention", "Failures"}
	previous := -1
	for _, section := range sections {
		index := strings.Index(out.String(), section)
		if index <= previous {
			t.Fatalf("summary sections are not ordered %v: %q", sections, out.String())
		}
		previous = index
	}
}

func TestSyncReportWriterUsesStderrForInteractiveRuns(t *testing.T) {
	var stdout, stderr bytes.Buffer
	_, _ = syncReportWriter(true, &stdout, &stderr).Write([]byte("interactive report"))
	if stdout.Len() != 0 {
		t.Fatalf("interactive report was written to stdout: %q", stdout.String())
	}
	if got, want := stderr.String(), "interactive report"; got != want {
		t.Fatalf("interactive report on stderr = %q, want %q", got, want)
	}
}

func TestSyncReportWriterKeepsNonInteractiveReportsOnStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	_, _ = syncReportWriter(false, &stdout, &stderr).Write([]byte("plain report"))
	if got, want := stdout.String(), "plain report"; got != want {
		t.Fatalf("non-interactive report on stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("non-interactive report was written to stderr: %q", stderr.String())
	}
}

func TestResolveSyncOwnersRequiresAuthentication(t *testing.T) {
	_, err := resolveSyncOwners(nil,
		func() (string, error) { return "", fmt.Errorf("invalid token") },
		func() ([]string, error) { return nil, nil },
	)
	if err == nil || !strings.Contains(err.Error(), "GitHub authentication failed") {
		t.Fatalf("resolveSyncOwners() error = %v, want authentication failure", err)
	}
}

func TestResolveSyncOwnersSeparatesRequestedOwnersFromMembership(t *testing.T) {
	owners, err := resolveSyncOwners([]string{"sneat-co"},
		func() (string, error) { return "trakhimenok", nil },
		func() ([]string, error) { t.Fatal("member org lookup should not run for --org"); return nil, nil },
	)
	if err != nil || !reflect.DeepEqual(owners, []string{"sneat-co"}) {
		t.Fatalf("resolveSyncOwners() = %v, %v; want [sneat-co], nil", owners, err)
	}
}
