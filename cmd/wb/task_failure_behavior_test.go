package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/taskoffload"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func TestTaskOffloadRejectsBlankContinuationBeforeCreatingWorktree(t *testing.T) {
	t.Parallel()
	contextFile := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(contextFile, []byte(" \n "), 0o600); err != nil {
		t.Fatal(err)
	}
	created := false
	cmd := newTaskOffloadCmdWithDeps(&invocation{}, taskOffloadDependencies{
		create: func(context.Context, []string, worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
			created = true
			return nil, nil
		},
	}, false)
	cmd.SetArgs([]string{"review-auth", "acme/app", "--context-file", contextFile})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "non-empty continuation") {
		t.Fatalf("blank continuation error = %v", err)
	}
	if created {
		t.Fatal("blank continuation created a worktree")
	}
}

func TestTaskOffloadRejectsInvalidHarnessBeforeCreatingWorktree(t *testing.T) {
	t.Parallel()
	contextFile := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(contextFile, []byte("Review auth."), 0o600); err != nil {
		t.Fatal(err)
	}
	created := false
	cmd := newTaskOffloadCmdWithDeps(&invocation{}, taskOffloadDependencies{
		create: func(context.Context, []string, worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
			created = true
			return nil, nil
		},
	}, false)
	cmd.SetArgs([]string{"review-auth", "acme/app", "--context-file", contextFile, "--harness", "unknown-runtime"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil {
		t.Fatal("invalid harness accepted")
	}
	if created {
		t.Fatal("invalid harness created a worktree")
	}
}

func TestTaskOffloadReportsEmptyCreateResultWithoutSavingContinuation(t *testing.T) {
	t.Parallel()
	contextFile := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(contextFile, []byte("Review auth."), 0o600); err != nil {
		t.Fatal(err)
	}
	storeCalled := false
	cmd := newTaskOffloadCmdWithDeps(&invocation{}, taskOffloadDependencies{
		create: func(context.Context, []string, worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
			return nil, nil
		},
		store: func() (taskoffload.Store, error) { storeCalled = true; return taskoffload.Store{}, nil },
	}, false)
	cmd.SetArgs([]string{"review-auth", "acme/app", "--context-file", contextFile})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "created no worktree") {
		t.Fatalf("empty create result error = %v", err)
	}
	if storeCalled {
		t.Fatal("continuation store opened without a worktree")
	}
}

func TestTaskOffloadPreservesSavedBriefWhenSuccessorLaunchFails(t *testing.T) {
	t.Parallel()
	contextFile := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(contextFile, []byte("Review auth."), 0o600); err != nil {
		t.Fatal(err)
	}
	store := taskoffload.NewStore(t.TempDir())
	want := errors.New("launcher unavailable")
	cmd := newTaskOffloadCmdWithDeps(&invocation{}, taskOffloadDependencies{
		create: func(context.Context, []string, worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
			return []worktrees.CreateResult{{Repository: "acme/app", WorktreeDir: "/tmp/review-auth"}}, nil
		},
		launch: func(_ *cobra.Command, request taskLaunchRequest) error {
			body, err := os.ReadFile(request.ContextFile)
			if err != nil || string(body) != "Review auth." {
				t.Fatalf("saved brief = %q, err=%v", body, err)
			}
			return want
		},
		store: func() (taskoffload.Store, error) { return store, nil },
	}, false)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"review-auth", "acme/app", "--context-file", contextFile})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); !errors.Is(err, want) {
		t.Fatalf("launch error = %v", err)
	}
	entries, err := os.ReadDir(store.Root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("saved task entries = %v, err=%v", entries, err)
	}
	_, savedBrief, err := store.Load(entries[0].Name())
	if err != nil || savedBrief != "Review auth." {
		t.Fatalf("brief after failed launch = %q, err=%v", savedBrief, err)
	}
}

func TestTaskPickupLaunchFailureLeavesRecordParked(t *testing.T) {
	t.Parallel()
	store := taskoffload.NewStore(t.TempDir())
	record := taskoffload.Record{SchemaVersion: 1, TaskID: "task-abc123", Task: "review-auth", WorktreeDir: "/tmp/review-auth", Repository: "acme/app", Status: taskoffload.StatusParked, CreatedAt: time.Unix(10, 0).UTC()}
	if err := store.Save(record, "Review auth."); err != nil {
		t.Fatal(err)
	}
	want := errors.New("launcher unavailable")
	cmd := newTaskPickupCmdWithDeps(taskOffloadDependencies{
		launch: func(_ *cobra.Command, request taskLaunchRequest) error {
			if request.WorktreeDir != record.WorktreeDir {
				t.Fatalf("launch request = %+v", request)
			}
			return want
		},
		store: func() (taskoffload.Store, error) { return store, nil },
	})
	cmd.SetArgs([]string{record.TaskID, "--to", " remote-vm "})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); !errors.Is(err, want) {
		t.Fatalf("pickup error = %v", err)
	}
	loaded, _, err := store.Load(record.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != taskoffload.StatusParked {
		t.Fatalf("status after failed launch = %s", loaded.Status)
	}
}

func TestTaskParkAcceptsStdinBriefAndPersistsItsOriginalPrompt(t *testing.T) {
	t.Parallel()
	store := taskoffload.NewStore(t.TempDir())
	cmd := newTaskOffloadCmdWithDeps(&invocation{projectsRoot: t.TempDir()}, taskOffloadDependencies{
		create: func(_ context.Context, repositories []string, options worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
			if len(repositories) != 1 || repositories[0] != "acme/app" || options.WorkLog.OriginalPrompt != "(stdin)" {
				t.Fatalf("create repositories=%v work log=%+v", repositories, options.WorkLog)
			}
			return []worktrees.CreateResult{{Repository: "acme/app", WorktreeDir: "/tmp/review-auth"}}, nil
		},
		store: func() (taskoffload.Store, error) { return store, nil },
	}, true)
	cmd.SetIn(strings.NewReader("Review auth with tests."))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"review-auth", "acme/app", "--context-file=-", "--format=json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var result taskOffloadOutput
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != string(taskoffload.StatusParked) || result.TaskID == "" {
		t.Fatalf("park output = %+v", result)
	}
	_, brief, err := store.Load(result.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if brief != "Review auth with tests." {
		t.Fatalf("stored brief = %q", brief)
	}
}

func TestTaskOffloadAcceptsStdinBriefBeforeLaunchingSuccessor(t *testing.T) {
	t.Parallel()
	store := taskoffload.NewStore(t.TempDir())
	launched := false
	cmd := newTaskOffloadCmdWithDeps(&invocation{projectsRoot: t.TempDir()}, taskOffloadDependencies{
		create: func(_ context.Context, repositories []string, options worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
			if len(repositories) != 1 || repositories[0] != "acme/app" || options.WorkLog.OriginalPrompt != "(stdin)" {
				t.Fatalf("create repositories=%v work log=%+v", repositories, options.WorkLog)
			}
			return []worktrees.CreateResult{{Repository: "acme/app", WorktreeDir: "/tmp/review-auth"}}, nil
		},
		launch: func(_ *cobra.Command, request taskLaunchRequest) error {
			launched = true
			body, err := os.ReadFile(request.ContextFile)
			if err != nil || string(body) != "Review auth with tests." {
				t.Fatalf("successor brief = %q, err=%v", body, err)
			}
			return nil
		},
		store: func() (taskoffload.Store, error) { return store, nil },
	}, false)
	cmd.SetIn(strings.NewReader("Review auth with tests."))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"review-auth", "acme/app", "--context-file=-", "--format=json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !launched {
		t.Fatal("offload did not launch the successor")
	}
	var result taskOffloadOutput
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != string(taskoffload.StatusOffload) || result.TaskID == "" {
		t.Fatalf("offload output = %+v", result)
	}
}

func TestTaskPickupAppliesOverridesToLaunchAndSavedRecord(t *testing.T) {
	t.Parallel()
	store := taskoffload.NewStore(t.TempDir())
	record := taskoffload.Record{SchemaVersion: 1, TaskID: "task-abc123", Task: "review-auth", WorktreeDir: "/tmp/review-auth", Repository: "acme/app", Status: taskoffload.StatusParked, CreatedAt: time.Unix(10, 0).UTC()}
	if err := store.Save(record, "Review auth."); err != nil {
		t.Fatal(err)
	}
	var launched taskLaunchRequest
	cmd := newTaskPickupCmdWithDeps(taskOffloadDependencies{
		launch: func(_ *cobra.Command, request taskLaunchRequest) error { launched = request; return nil },
		store:  func() (taskoffload.Store, error) { return store, nil },
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{record.TaskID, "--harness=codex", "--model=sol", "--to=remote-vm", "--format=json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if launched.Harness != "codex" || launched.Model != "sol" || launched.Target != "remote-vm" || launched.Format != "json" {
		t.Fatalf("launch overrides = %+v", launched)
	}
	loaded, brief, err := store.Load(record.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != taskoffload.StatusOffload || loaded.Target != "remote-vm" || brief != "Review auth." {
		t.Fatalf("saved pickup = %+v brief=%q", loaded, brief)
	}
	if !strings.Contains(out.String(), `"status": "offloaded"`) {
		t.Fatalf("pickup JSON = %q", out.String())
	}
}
