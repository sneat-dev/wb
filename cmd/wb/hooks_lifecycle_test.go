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
	terminalDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(terminalDeadline) {
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
	// The receipt is written before the worker removes its running record and
	// publishes terminal health. Wait for that durable terminal state so the
	// test does not race TempDir cleanup with the detached process.
	dispatcher := lifecyclehooks.DefaultDispatcher()
	dispatcher.ConfigPath = config
	dispatcher.StateDir = filepath.Join(stateHome, "wb", "lifecycle-hooks")
	dispatcher.ReceiptPath = receiptPath
	for time.Now().Before(deadline) {
		status, err := dispatcher.Status(1)
		if err == nil && status.Worker == "idle" && len(status.Pending) == 0 && len(status.Running) == 0 && status.WorkerHealth != nil && status.WorkerHealth.FinishedAt.After(time.Time{}) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("detached lifecycle worker did not publish terminal state")
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
	for _, want := range []string{`"configured": true`, `"name": "code-index"`, `"delivery": "at-least-once"`, `"status": "ready"`} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("check output missing %q:\n%s", want, output.String())
		}
	}
}

func TestLifecycleResumeAndGCAreSafeOnEmptyState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))

	resume := newHooksLifecycleResumeCmd()
	var resumeOutput bytes.Buffer
	resume.SetOut(&resumeOutput)
	resume.SetArgs([]string{"--format", "json"})
	if err := resume.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resumeOutput.String(), `"worker_started": false`) {
		t.Fatalf("resume output=%s", resumeOutput.String())
	}

	gc := newHooksLifecycleGCCmd()
	var gcOutput bytes.Buffer
	gc.SetOut(&gcOutput)
	gc.SetArgs([]string{"--format", "json"})
	if err := gc.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"apply": false`, `"receipts": 0`} {
		if !strings.Contains(gcOutput.String(), want) {
			t.Fatalf("GC output missing %q: %s", want, gcOutput.String())
		}
	}
}

// TestHooksLifecycleBackfillCommandOnEmptyProjectsRootReportsZeroExecutions
// drives "wb hooks lifecycle backfill" through the real CLI dispatch (not
// just planLifecycleBackfill called directly), so the RunE closure that
// reads inv.filterFlag before calling planLifecycleBackfill actually
// executes.
func TestHooksLifecycleBackfillCommandOnEmptyProjectsRootReportsZeroExecutions(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	args := []string{"hooks", "lifecycle", "backfill", "--projects-root", root}
	if code := run(args, &stdout, &stderr); code != exitOK {
		t.Fatalf("run(%q) exit = %d, stderr=%s", args, code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "planned 0 execution(s) from 0 repositories") {
		t.Fatalf("stdout = %q, want a zero-execution plan", stdout.String())
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
