package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/lifecyclehooks"
)

func TestLifecycleStatusCommandShowsPrivateDiagnosticsAndRetry(t *testing.T) {
	root := t.TempDir()
	configHome := filepath.Join(root, "config")
	stateHome := filepath.Join(root, "state")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	executable := lifecycleTestExecutable(t, root)
	config := filepath.Join(configHome, "wb", "wb.yaml")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(lifecycleConfigContents(executable)), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(root, "checkout")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	dispatcher := lifecyclehooks.DefaultDispatcher()
	dispatcher.LaunchWorker = func(lifecyclehooks.WorkerRequest) error { return nil }
	dispatcher.Run = func(context.Context, lifecyclehooks.Invocation) error { return errors.New("index failed") }
	if _, err := dispatcher.Dispatch(context.Background(), []lifecyclehooks.Event{{Name: lifecyclehooks.EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repository, OldSHA: "a", NewSHA: "b", Cause: "pull"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.Drain(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	command := newHooksLifecycleStatusCmd()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"failed", "stdout.log", "stderr.log", "wb hooks lifecycle retry"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("status output missing %q:\n%s", want, output.String())
		}
	}
}

func TestLifecycleBackfillStartsDetachedWorkerProcess(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	repository := filepath.Join(projects, "acme", "app")
	initOriginRepository(t, repository, "acme/app")
	executable := lifecycleTestExecutable(t, root)
	configHome := filepath.Join(root, "config")
	stateHome := filepath.Join(root, "state")
	config := filepath.Join(configHome, "wb", "wb.yaml")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	contents := lifecycleConfigContents(executable)
	if err := os.WriteFile(config, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := buildJourneyWB(t)
	command := exec.Command(binary, "--projects-root", projects, "hooks", "lifecycle", "backfill", "--apply", "--format", "json")
	command.Env = append(os.Environ(), "XDG_CONFIG_HOME="+configHome, "XDG_STATE_HOME="+stateHome)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("backfill: %v\n%s", err, output)
	}
	receiptPath := filepath.Join(stateHome, "wb", "lifecycle-hook-events.jsonl")
	deadline := time.Now().Add(5 * time.Second)
	var receipt lifecyclehooks.Receipt
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(receiptPath)
		if err == nil {
			line := strings.Split(strings.TrimSpace(string(raw)), "\n")[0]
			if json.Unmarshal([]byte(line), &receipt) == nil && receipt.Status != "" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if receipt.Status != "succeeded" || receipt.Repository != "github.com/acme/app" || receipt.Cause != "lifecycle-backfill" {
		t.Fatalf("detached worker receipt=%+v", receipt)
	}
	if info, err := os.Stat(receipt.StdoutPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stdout diagnostic info=%v err=%v", info, err)
	}
}

func TestLifecycleCheckCommandReportsTrustedExecutor(t *testing.T) {
	root := t.TempDir()
	executable := lifecycleTestExecutable(t, root)
	config := lifecycleTestConfig(t, root, executable)
	command := newHooksLifecycleCheckCmd()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"--config", config, "--format", "json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"configured": true`, `"name": "code-index"`, `"status": "ready"`} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("check output missing %q:\n%s", want, output.String())
		}
	}
}

func TestLifecycleBackfillPlansAndAppliesOnlyMatchingCanonicalRepositories(t *testing.T) {
	root := t.TempDir()
	matching := filepath.Join(root, "acme", "app")
	nonMatching := filepath.Join(root, "other", "tool")
	initOriginRepository(t, matching, "acme/app")
	initOriginRepository(t, nonMatching, "other/tool")
	executable := lifecycleTestExecutable(t, root)
	dispatcher := lifecyclehooks.Dispatcher{
		ConfigPath: lifecycleTestConfig(t, root, executable),
		StateDir:   filepath.Join(root, "state"), ReceiptPath: filepath.Join(root, "receipts.jsonl"),
		LaunchWorker: func(lifecyclehooks.WorkerRequest) error { return nil },
	}
	plan, err := planLifecycleBackfill(context.Background(), root, "", dispatcher, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Apply || plan.Scanned != 2 || len(plan.Executions) != 1 || plan.Executions[0].Event.Repository != "github.com/acme/app" || plan.Executions[0].Event.Cause != "lifecycle-backfill" {
		t.Fatalf("plan=%+v", plan)
	}
	applied, err := planLifecycleBackfill(context.Background(), root, "", dispatcher, true)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Apply || applied.Enqueue.Enqueued != 1 {
		t.Fatalf("applied=%+v", applied)
	}
	status, err := dispatcher.Status(20)
	if err != nil || len(status.Pending) != 1 || status.Pending[0].Event.Repository != "github.com/acme/app" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func lifecycleTestExecutable(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "bin", "indexer")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func lifecycleTestConfig(t *testing.T, root, executable string) string {
	t.Helper()
	path := filepath.Join(root, "wb.yaml")
	contents := lifecycleConfigContents(executable)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func lifecycleConfigContents(executable string) string {
	return `hooks:
  version: 1
  executors:
    code-index:
      run: ` + executable + `
      args: [sync, --init, .]
      cwd: repository
      mode: coalesced
      timeout: 2m
      failure: warn
  bindings:
    - on: [checkout-updated]
      match:
        repositories:
          include: [github.com/acme/*]
      execute: [code-index]
`
}
