package main

import (
	"bytes"
	"context"
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
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	receiptPath := filepath.Join(stateHome, "wb", "lifecycle-hook-events.jsonl")
	dispatcher := lifecyclehooks.DefaultDispatcher()
	dispatcher.ConfigPath = config
	dispatcher.StateDir = filepath.Join(stateHome, "wb", "lifecycle-hooks")
	dispatcher.ReceiptPath = receiptPath
	terminal := false
	waitTerminal := func() bool {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			status, err := dispatcher.Status(1)
			if err == nil && status.Worker == "idle" && len(status.Pending) == 0 && len(status.Running) == 0 && status.WorkerHealth != nil && status.WorkerHealth.FinishedAt.After(time.Time{}) {
				terminal = true
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}
	// Receipt assertion failure must still join the actual detached worker's
	// durable terminal state before TempDir removes its private state.
	t.Cleanup(func() {
		if !terminal && !waitTerminal() {
			t.Error("detached lifecycle worker did not publish terminal state during cleanup")
		}
	})
	command := exec.CommandContext(ctx, binary, "--projects-root", projects, "hooks", "lifecycle", "backfill", "--apply", "--format", "json")
	command.Env = append(os.Environ(), "XDG_CONFIG_HOME="+configHome, "XDG_STATE_HOME="+stateHome)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("backfill: %v\n%s", err, output)
	}
	var receipt lifecyclehooks.Receipt
	receiptDeadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(receiptDeadline) {
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
	if waitTerminal() {
		return
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
