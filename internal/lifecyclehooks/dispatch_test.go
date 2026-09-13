package lifecyclehooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDispatchRunsOnlyChangedMatchingRepositoriesAndCoalesces(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "outside", "indexer")
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := writeTestConfig(t, root, executable, "warn")
	repository := filepath.Join(root, "checkout")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	var invocations []Invocation
	dispatcher := Dispatcher{
		ConfigPath: config, ReceiptPath: filepath.Join(root, "receipts.jsonl"),
		Now: time.Now, EvalSymlinks: filepath.EvalSymlinks,
		Run: func(_ context.Context, invocation Invocation) error {
			invocations = append(invocations, invocation)
			return nil
		},
	}
	report, err := dispatcher.Dispatch(context.Background(), []Event{
		{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repository, OldSHA: "a", NewSHA: "b", Cause: "pull"},
		{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repository, OldSHA: "b", NewSHA: "c", Cause: "merge"},
		{Name: EventCheckoutUpdated, Repository: "github.com/acme/current", Checkout: filepath.Join(root, "current"), OldSHA: "a", NewSHA: "a", Cause: "pull"},
		{Name: EventCheckoutUpdated, Repository: "gitlab.com/acme/other", Checkout: filepath.Join(root, "other"), OldSHA: "a", NewSHA: "b", Cause: "pull"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Executed != 1 || report.Coalesced != 1 || len(invocations) != 1 {
		t.Fatalf("report=%+v invocations=%d", report, len(invocations))
	}
	if got := envValue(invocations[0].Env, "WB_NEW_SHA"); got != "c" {
		t.Fatalf("WB_NEW_SHA=%q, want c", got)
	}
	if got := envValue(invocations[0].Env, "WB_OLD_SHA"); got != "a" {
		t.Fatalf("WB_OLD_SHA=%q, want earliest a", got)
	}
	resolvedExecutable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	if invocations[0].Dir != repository || invocations[0].Run != resolvedExecutable {
		t.Fatalf("invocation=%+v", invocations[0])
	}
	if raw, err := os.ReadFile(dispatcher.ReceiptPath); err != nil {
		t.Fatal(err)
	} else if strings.Count(strings.TrimSpace(string(raw)), "\n") != 0 || !strings.Contains(string(raw), `"coalesced_count":2`) {
		t.Fatalf("receipt=%s", raw)
	}
}

func TestDispatchRefusesExecutableInsideCheckout(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "checkout")
	insideExecutable := filepath.Join(repository, "tools", "indexer")
	if err := os.MkdirAll(filepath.Dir(insideExecutable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(insideExecutable, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "trusted-bin", "indexer")
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(insideExecutable, executable); err != nil {
		t.Fatal(err)
	}
	config := writeTestConfig(t, root, executable, "warn")
	run := false
	dispatcher := Dispatcher{
		ConfigPath: config, ReceiptPath: filepath.Join(root, "receipts.jsonl"),
		Now: time.Now, EvalSymlinks: filepath.EvalSymlinks,
		Run: func(context.Context, Invocation) error { run = true; return nil },
	}
	report, err := dispatcher.Dispatch(context.Background(), []Event{{
		Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repository, OldSHA: "a", NewSHA: "b", Cause: "pull",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if run || len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "outside") {
		t.Fatalf("run=%t report=%+v", run, report)
	}
}

func TestAppendReceiptKeepsConcurrentRecordsAsCompleteJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipts.jsonl")
	const writers = 32
	var wait sync.WaitGroup
	errors := make(chan error, writers)
	for index := range writers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errors <- appendReceipt(path, receipt{SchemaVersion: 1, ID: string(rune('a' + index)), NewSHA: "new"})
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != writers {
		t.Fatalf("receipt lines=%d, want %d", len(lines), writers)
	}
	for _, line := range lines {
		var got receipt
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("invalid receipt line %q: %v", line, err)
		}
	}
}

func writeTestConfig(t *testing.T, root, executable, failure string) string {
	t.Helper()
	config := filepath.Join(root, "wb.yaml")
	raw := "hooks:\n  version: 1\n  executors:\n    index:\n      run: " + executable + "\n      args: [sync, .]\n      cwd: repository\n      mode: coalesced\n      timeout: 2m\n      failure: " + failure + "\n  bindings:\n    - on: [checkout-updated]\n      match:\n        repositories:\n          include: [github.com/*/*]\n      execute: [index]\n"
	if err := os.WriteFile(config, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return config
}

func envValue(env []string, key string) string {
	for _, entry := range env {
		if strings.HasPrefix(entry, key+"=") {
			return strings.TrimPrefix(entry, key+"=")
		}
	}
	return ""
}
