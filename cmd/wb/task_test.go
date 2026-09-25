package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/taskoffload"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestTaskParkCreatesWorktreeWithoutLaunch(t *testing.T) {
	store := taskoffload.NewStore(t.TempDir())
	contextFile := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(contextFile, []byte("Review auth."), 0o600); err != nil {
		t.Fatal(err)
	}
	launched := false
	deps := taskOffloadDependencies{
		create: func(_ context.Context, repositories []string, options worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
			if options.Operation != "review-auth" || len(repositories) != 1 {
				t.Fatalf("create options = %#v repos=%v", options, repositories)
			}
			return []worktrees.CreateResult{{Repository: repositories[0], WorktreeDir: "/tmp/review-auth"}}, nil
		},
		launch: func(*cobra.Command, taskLaunchRequest) error {
			launched = true
			return nil
		},
		store: func() (taskoffload.Store, error) { return store, nil },
	}
	command := newTaskOffloadCmdWithDeps(&invocation{}, deps, true)
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"review-auth", "acme/app", "--context-file", contextFile})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if launched {
		t.Fatal("park must not launch a successor")
	}
	if !strings.Contains(output.String(), "parked task review-auth") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestTaskOffloadLaunchesSuccessor(t *testing.T) {
	store := taskoffload.NewStore(t.TempDir())
	contextFile := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(contextFile, []byte("Review auth."), 0o600); err != nil {
		t.Fatal(err)
	}
	var launch taskLaunchRequest
	deps := taskOffloadDependencies{
		create: func(_ context.Context, _ []string, _ worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
			return []worktrees.CreateResult{{Repository: "acme/app", WorktreeDir: "/tmp/review-auth"}}, nil
		},
		launch: func(_ *cobra.Command, request taskLaunchRequest) error {
			launch = request
			return nil
		},
		store: func() (taskoffload.Store, error) { return store, nil },
	}
	command := newTaskOffloadCmdWithDeps(&invocation{}, deps, false)
	command.SetOut(&bytes.Buffer{})
	command.SetArgs([]string{"review-auth", "acme/app", "--context-file", contextFile, "--harness", "claude", "--model", "opus"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if launch.WorktreeDir != "/tmp/review-auth" || launch.Harness != "claude-code" || launch.Model != "opus" {
		t.Fatalf("launch = %#v", launch)
	}
}

func TestTaskPickupLaunchesParkedTask(t *testing.T) {
	store := taskoffload.NewStore(t.TempDir())
	record := taskoffload.Record{
		SchemaVersion: 1, TaskID: "task-abc123", Task: "review-auth", WorktreeDir: "/tmp/review-auth",
		Repository: "acme/app", Status: taskoffload.StatusParked, CreatedAt: time.Unix(10, 0).UTC(),
	}
	if err := store.Save(record, "Review auth."); err != nil {
		t.Fatal(err)
	}
	var launch taskLaunchRequest
	deps := taskOffloadDependencies{
		launch: func(_ *cobra.Command, request taskLaunchRequest) error {
			launch = request
			return nil
		},
		store: func() (taskoffload.Store, error) { return store, nil },
	}
	command := newTaskPickupCmdWithDeps(deps)
	command.SetOut(&bytes.Buffer{})
	command.SetArgs([]string{"task-abc123", "--harness", "codex", "--model", "sol"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if launch.WorktreeDir != "/tmp/review-auth" || launch.Harness != "codex" || launch.Model != "sol" {
		t.Fatalf("launch = %#v", launch)
	}
	loaded, _, err := store.Load("task-abc123")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != taskoffload.StatusOffload {
		t.Fatalf("status = %s, want offloaded", loaded.Status)
	}
}
