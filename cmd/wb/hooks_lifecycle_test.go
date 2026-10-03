package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/testenv"
)

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
	binary := buildWB(t)
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
func lifecycleTestExecutable(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "bin", "indexer")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := testenv.WriteExecutableFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
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
