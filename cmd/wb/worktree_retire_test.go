package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestRetireReleaseClaimOnlyAfterLastTaskWorktree(t *testing.T) {
	for _, tc := range []struct {
		name       string
		inventory  worktrees.ListOutcome
		listErr    error
		wantCalled bool
		wantStatus string
	}{
		{name: "last checkout", wantCalled: true, wantStatus: "released"},
		{name: "another repository remains", inventory: worktrees.ListOutcome{Results: []worktrees.ListResult{{Task: "task", Repository: "acme/other"}}}, wantStatus: "skipped"},
		{name: "malformed candidate remains", inventory: worktrees.ListOutcome{Diagnostics: []worktrees.ListDiagnostic{{}}}, wantStatus: "skipped"},
		{name: "inventory unavailable", listErr: errors.New("offline"), wantStatus: "skipped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			called := false
			result := retireReleaseClaim(context.Background(), "/projects", "task", &out,
				func(_ context.Context, options worktrees.ListOptions) (worktrees.ListOutcome, error) {
					if options.ProjectsRoot != "/projects" || options.Task != "task" || options.Filter != "" {
						t.Fatalf("unexpected task inventory options: %#v", options)
					}
					return tc.inventory, tc.listErr
				},
				func(root, task string, writer io.Writer) autoReleaseResult {
					called = true
					if root != "/projects" || task != "task" {
						t.Fatalf("release target: %s %s", root, task)
					}
					_, _ = io.WriteString(writer, "remote claim: released task\n")
					return autoReleaseResult{Outcome: "released"}
				})
			if called != tc.wantCalled || result.Outcome != tc.wantStatus {
				t.Fatalf("called=%t result=%#v output=%q", called, result, out.String())
			}
			if tc.wantCalled && !strings.Contains(out.String(), "remote claim: released task") {
				t.Fatalf("missing release receipt: %q", out.String())
			}
		})
	}
}
