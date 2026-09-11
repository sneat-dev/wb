package main

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/gitops"
)

func TestPrintSyncSummaryReportsFreshRemoteUpdates(t *testing.T) {
	var out bytes.Buffer
	printSyncSummary(&out, []fleetsync.Result{
		{Status: fleetsync.Pulled, PullAttempted: true, PullSucceeded: true, Updated: true},
		{Status: fleetsync.Pulled, PullAttempted: true, PullSucceeded: true},
		{Status: fleetsync.Unpushed, PullAttempted: true, PullSucceeded: true, Updated: true},
		{Status: fleetsync.Pulled, PullPlanned: true},
	}, false, false)
	for _, want := range []string{
		"Final outcomes",
		"Pulled                  3",
		"Pull actions",
		"Pull planned            1",
		"Pull attempted          3",
		"Pull succeeded          3",
		"Updated from remote     2",
		"Already current         1",
		"Attention",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("summary %q does not contain %q", out.String(), want)
		}
	}
	if strings.Contains(out.String(), "Failures") || strings.Contains(out.String(), "Errors") {
		t.Fatalf("zero-error summary contains a failure section: %q", out.String())
	}
	sections := []string{"Final outcomes", "Pull actions", "Attention"}
	previous := -1
	for _, section := range sections {
		index := strings.Index(out.String(), section)
		if index <= previous {
			t.Fatalf("summary sections are not ordered %v: %q", sections, out.String())
		}
		previous = index
	}
}

func TestPrintSyncSummaryKeepsAttentionAboveFailuresAndNamesWorktree(t *testing.T) {
	commits := make([]string, 22)
	for i := range commits {
		commits[i] = fmt.Sprintf("%07d commit", i)
	}
	attention := fleetsync.Result{
		Repo: discover.Repo{
			Org:  "openvaultdb",
			Name: "openvaultdb-go",
			Path: "/projects/openvaultdb/openvaultdb-go",
		},
		Status: fleetsync.Unpushed,
		Detail: gitops.RepoStatus{
			Unpushed: commits,
			UnpushedBranches: []gitops.UnpushedBranch{{
				Branch:   "layered-acl-query",
				Worktree: "/projects/openvaultdb/openvaultdb-go/.worktrees/layered-acl-query",
				Commits:  commits,
			}},
		},
	}
	failure := fleetsync.Result{
		Repo:   discover.Repo{Org: "acme", Name: "broken"},
		Status: fleetsync.Failed,
		Err:    errors.New("network failed"),
	}

	var out bytes.Buffer
	printSyncSummary(&out, []fleetsync.Result{failure, attention}, false, false)
	got := out.String()
	wantAttention := "    ! openvaultdb/openvaultdb-go 🌳 layered-acl-query — 22 commits not yet pushed"
	if !strings.Contains(got, wantAttention) {
		t.Fatalf("summary does not attribute unpushed commits to their worktree:\n%s", got)
	}
	attentionIndex := strings.Index(got, wantAttention)
	failuresIndex := strings.Index(got, "\nFailures\n")
	errorIndex := strings.Index(got, "    ✗ acme/broken — network failed")
	if attentionIndex < 0 || failuresIndex <= attentionIndex || errorIndex <= failuresIndex {
		t.Fatalf("attention and failures are not rendered beneath their own sections:\n%s", got)
	}
}

func TestSyncSummaryStylesColorInteractiveWarnings(t *testing.T) {
	styles := newSyncSummaryStyles(true)
	var out bytes.Buffer
	writeSyncAttention(&out, styles, fleetsync.Result{
		Repo:   discover.Repo{Org: "acme", Name: "app"},
		Status: fleetsync.Unpushed,
		Detail: gitops.RepoStatus{
			Unpushed: []string{"abc1234 work"},
			UnpushedBranches: []gitops.UnpushedBranch{{
				Branch:   "feature",
				Worktree: "/projects/acme/app/.worktrees/feature",
				Commits:  []string{"abc1234 work"},
			}},
		},
	})
	if !strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("styled attention line has no ANSI styling: %q", out.String())
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
