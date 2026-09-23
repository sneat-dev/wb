package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type retireStatusProvider struct {
	*slowStatusProvider
	status remotestate.StatusSnapshot
	err    error
}

func (provider *retireStatusProvider) Status(context.Context) (remotestate.StatusSnapshot, error) {
	return provider.status, provider.err
}

func TestRetireRemoteOwnershipChecksClaimsAndMachineSnapshots(t *testing.T) {
	config := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(config, []byte("remote:\n  repo: team/wb-state\n  machine: laptop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &retireStatusProvider{slowStatusProvider: &slowStatusProvider{}}
	deps := remoteDeps{configPath: config, login: func() (string, error) { return "alice", nil },
		open: func(remotestate.Config, string) (remotestate.Provider, error) { return provider, nil }}
	for _, tc := range []struct {
		name   string
		status remotestate.StatusSnapshot
		err    error
		want   string
	}{
		{name: "own claim and checkout", status: remotestate.StatusSnapshot{
			Claims:   []remotestate.ClaimEntry{{Claim: remotestate.Claim{Task: "retire-task", Login: "alice", Machine: "laptop"}}},
			Machines: []remotestate.Entry{{Snapshot: remotestate.Snapshot{Login: "alice", Machine: "laptop", Worktrees: []remotestate.WorktreeState{{Task: "retire-task"}}}}},
		}},
		{name: "remote claim", status: remotestate.StatusSnapshot{Claims: []remotestate.ClaimEntry{{Claim: remotestate.Claim{Task: "retire-task", Login: "alice", Machine: "vm"}}}}, want: "competing remote claim"},
		{name: "remote checkout", status: remotestate.StatusSnapshot{Machines: []remotestate.Entry{{Snapshot: remotestate.Snapshot{Login: "alice", Machine: "vm", Worktrees: []remotestate.WorktreeState{{Task: "retire-task"}}}}}}, want: "competing remote checkout"},
		{name: "unreadable claim", status: remotestate.StatusSnapshot{Claims: []remotestate.ClaimEntry{{Claim: remotestate.Claim{Task: "other-task"}, Error: "corrupt"}}}, want: "unreadable"},
		{name: "unreadable snapshot", status: remotestate.StatusSnapshot{Machines: []remotestate.Entry{{Snapshot: remotestate.Snapshot{Login: "bob", Machine: "vm"}, Error: "corrupt"}}}, want: "unreadable remote machine snapshot"},
		{name: "store unavailable", err: errors.New("offline"), want: "read remote task ownership"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider.status, provider.err = tc.status, tc.err
			err := retireCheckRemoteOwnership(context.Background(), deps, "/projects", "retire-task")
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("remote ownership: %v, want %q", err, tc.want)
			}
		})
	}
	deps.configPath = filepath.Join(t.TempDir(), "missing.yaml")
	if err := retireCheckRemoteOwnership(context.Background(), deps, "/projects", "retire-task"); err == nil {
		t.Fatal("missing remote config was accepted")
	}
}

func TestRetireReleaseClaimOnlyAfterLastTaskWorktree(t *testing.T) {
	for _, tc := range []struct {
		name       string
		inventory  worktrees.ListOutcome
		listErr    error
		release    autoReleaseResult
		wantCalled bool
		wantStatus string
	}{
		{name: "last checkout", wantCalled: true, wantStatus: "released"},
		{name: "another repository remains", inventory: worktrees.ListOutcome{Results: []worktrees.ListResult{{Task: "task", Repository: "acme/other"}}}, wantStatus: "skipped"},
		{name: "malformed candidate remains", inventory: worktrees.ListOutcome{Diagnostics: []worktrees.ListDiagnostic{{}}}, wantStatus: "failed"},
		{name: "inventory unavailable", listErr: errors.New("offline"), wantStatus: "failed"},
		{name: "release unavailable", release: autoReleaseResult{Outcome: "disabled"}, wantCalled: true, wantStatus: "failed"},
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
					if tc.release.Outcome != "" {
						return tc.release
					}
					return autoReleaseResult{Outcome: "released"}
				})
			if called != tc.wantCalled || result.Outcome != tc.wantStatus {
				t.Fatalf("called=%t result=%#v output=%q", called, result, out.String())
			}
			if result.Leaked() != (tc.wantStatus == "failed") {
				t.Fatalf("release failure visibility: %#v", result)
			}
			if tc.wantCalled && !strings.Contains(out.String(), "remote claim: released task") {
				t.Fatalf("missing release receipt: %q", out.String())
			}
		})
	}
}
