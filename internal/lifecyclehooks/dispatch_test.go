package lifecyclehooks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDispatchDurablyCoalescesMatchingChangedRepositoriesWithoutRunningInline(t *testing.T) {
	dispatcher, repository := testDispatcher(t)
	run := false
	dispatcher.Run = func(context.Context, Invocation) error { run = true; return nil }
	report, err := dispatcher.Dispatch(context.Background(), []Event{
		{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repository, OldSHA: "a", NewSHA: "b", Cause: "pull"},
		{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repository, OldSHA: "b", NewSHA: "c", Cause: "merge"},
		{Name: EventCheckoutUpdated, Repository: "github.com/acme/current", Checkout: filepath.Join(filepath.Dir(repository), "current"), OldSHA: "a", NewSHA: "a", Cause: "pull"},
		{Name: EventCheckoutUpdated, Repository: "gitlab.com/acme/other", Checkout: filepath.Join(filepath.Dir(repository), "other"), OldSHA: "a", NewSHA: "b", Cause: "pull"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if run || report.Enqueued != 1 || report.Coalesced != 1 {
		t.Fatalf("run=%t report=%+v", run, report)
	}
	status, err := dispatcher.Status(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Pending) != 1 || status.Pending[0].Event.OldSHA != "a" || status.Pending[0].Event.NewSHA != "c" || status.Pending[0].CoalescedCount != 2 {
		t.Fatalf("pending=%+v", status.Pending)
	}
}

func TestDispatchCoalescesAcrossCallsAndKeepsLatestSHA(t *testing.T) {
	dispatcher, repository := testDispatcher(t)
	first := Event{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repository, OldSHA: "a", NewSHA: "b", Cause: "pull"}
	if _, err := dispatcher.Dispatch(context.Background(), []Event{first}); err != nil {
		t.Fatal(err)
	}
	second := []Event{
		{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repository, OldSHA: "b", NewSHA: "c", Cause: "merge"},
		{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repository, OldSHA: "c", NewSHA: "d", Cause: "merge"},
	}
	report, err := dispatcher.Dispatch(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if report.Enqueued != 1 || report.Coalesced != 2 {
		t.Fatalf("report=%+v", report)
	}
	status, err := dispatcher.Status(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Pending) != 1 || status.Pending[0].Event.OldSHA != "a" || status.Pending[0].Event.NewSHA != "d" || status.Pending[0].CoalescedCount != 3 {
		t.Fatalf("pending=%+v", status.Pending)
	}
}

func TestWorkerDrainsNewerEventQueuedWhilePriorRevisionRuns(t *testing.T) {
	dispatcher, repository := testDispatcher(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var mutex sync.Mutex
	var newSHAs []string
	dispatcher.Run = func(_ context.Context, invocation Invocation) error {
		sha := envValue(invocation.Env, "WB_NEW_SHA")
		mutex.Lock()
		newSHAs = append(newSHAs, sha)
		first := len(newSHAs) == 1
		mutex.Unlock()
		if first {
			close(started)
			<-release
		}
		return nil
	}
	first := Event{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repository, OldSHA: "a", NewSHA: "b", Cause: "pull"}
	if _, err := dispatcher.Dispatch(context.Background(), []Event{first}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := dispatcher.Drain(context.Background(), 1)
		done <- err
	}()
	<-started
	second := Event{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repository, OldSHA: "b", NewSHA: "c", Cause: "merge"}
	if _, err := dispatcher.Dispatch(context.Background(), []Event{second}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if strings.Join(newSHAs, ",") != "b,c" {
		t.Fatalf("executed SHAs=%v, want [b c]", newSHAs)
	}
}

func TestWorkerRecoversInterruptedClaim(t *testing.T) {
	dispatcher, repository := testDispatcher(t)
	event := Event{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repository, OldSHA: "a", NewSHA: "b", Cause: "pull"}
	if _, err := dispatcher.Dispatch(context.Background(), []Event{event}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dispatcher.pendingDir())
	if err != nil || len(entries) != 1 {
		t.Fatalf("pending entries=%d err=%v", len(entries), err)
	}
	if err := os.Rename(filepath.Join(dispatcher.pendingDir(), entries[0].Name()), filepath.Join(dispatcher.runningDir(), entries[0].Name())); err != nil {
		t.Fatal(err)
	}
	dispatcher.Run = func(context.Context, Invocation) error { return nil }
	report, err := dispatcher.Drain(context.Background(), 1)
	if err != nil || report.Executed != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	status, err := dispatcher.Status(20)
	if err != nil || len(status.Pending) != 0 || len(status.Running) != 0 || len(status.Receipts) != 1 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestDrainUsesBoundedParallelismAndPrivateBoundedDiagnostics(t *testing.T) {
	dispatcher, repository := testDispatcher(t)
	var active atomic.Int32
	var maximum atomic.Int32
	release := make(chan struct{})
	dispatcher.Run = func(_ context.Context, invocation Invocation) error {
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		_, _ = invocation.Stdout.Write([]byte(strings.Repeat("o", maxDiagnosticBytes+1024)))
		_, _ = invocation.Stderr.Write([]byte("private diagnostic"))
		<-release
		active.Add(-1)
		return nil
	}
	var events []Event
	for index, name := range []string{"one", "two", "three"} {
		checkout := filepath.Join(filepath.Dir(repository), name)
		if err := os.MkdirAll(checkout, 0o755); err != nil {
			t.Fatal(err)
		}
		events = append(events, Event{Name: EventCheckoutUpdated, Repository: "github.com/acme/" + name, Checkout: checkout, OldSHA: "a", NewSHA: string(rune('b' + index)), Cause: "pull"})
	}
	if _, err := dispatcher.Dispatch(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := dispatcher.Drain(context.Background(), 2); err != nil {
			t.Errorf("drain: %v", err)
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for maximum.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	<-done
	if maximum.Load() != 2 {
		t.Fatalf("maximum concurrency=%d, want 2", maximum.Load())
	}
	status, err := dispatcher.Status(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Pending) != 0 || len(status.Running) != 0 || len(status.Receipts) != 3 {
		t.Fatalf("status=%+v", status)
	}
	for _, receipt := range status.Receipts {
		if receipt.Status != "succeeded" || !receipt.StdoutTruncated {
			t.Fatalf("receipt=%+v", receipt)
		}
		for _, path := range []string{receipt.StdoutPath, receipt.StderrPath} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("diagnostic %s mode=%s", path, info.Mode())
			}
		}
		if info, _ := os.Stat(receipt.StdoutPath); info.Size() != maxDiagnosticBytes {
			t.Fatalf("stdout size=%d, want %d", info.Size(), maxDiagnosticBytes)
		}
	}
}

func TestFailedReceiptCanBeRetriedAfterRepair(t *testing.T) {
	dispatcher, repository := testDispatcher(t)
	dispatcher.Run = func(context.Context, Invocation) error { return errors.New("broken index") }
	event := Event{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repository, OldSHA: "a", NewSHA: "b", Cause: "pull"}
	if _, err := dispatcher.Dispatch(context.Background(), []Event{event}); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.Drain(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	status, err := dispatcher.Status(20)
	if err != nil || len(status.Receipts) != 1 || status.Receipts[0].Status != "failed" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	dispatcher.Run = func(context.Context, Invocation) error { return nil }
	if report, err := dispatcher.Retry(status.Receipts[0].ID); err != nil || report.Enqueued != 1 {
		t.Fatalf("retry report=%+v err=%v", report, err)
	}
	if _, err := dispatcher.Drain(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	status, err = dispatcher.Status(20)
	if err != nil || len(status.Receipts) != 2 || status.Receipts[1].Status != "succeeded" || !strings.HasPrefix(status.Receipts[1].Cause, "retry:") {
		t.Fatalf("status=%+v err=%v", status, err)
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
	configured := filepath.Join(root, "trusted-bin", "indexer")
	if err := os.MkdirAll(filepath.Dir(configured), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(insideExecutable, configured); err != nil {
		t.Fatal(err)
	}
	dispatcher := newTestDispatcher(t, root, configured)
	if _, err := dispatcher.Dispatch(context.Background(), []Event{{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repository, OldSHA: "a", NewSHA: "b", Cause: "pull"}}); err != nil {
		t.Fatal(err)
	}
	report, err := dispatcher.Drain(context.Background(), 1)
	if err != nil || len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "outside") {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestCheckRejectsGroupWritableExecutable(t *testing.T) {
	dispatcher, _ := testDispatcher(t)
	cfg, _, err := Load(dispatcher.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	executable := cfg.Executors["index"].Run
	if err := os.Chmod(executable, 0o775); err != nil {
		t.Fatal(err)
	}
	report, err := dispatcher.Check()
	if err != nil || len(report.Findings) != 1 || !strings.Contains(report.Findings[0], "writable") {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestRevalidateRejectsExecutableIdentitySwap(t *testing.T) {
	dispatcher, repository := testDispatcher(t)
	cfg, _, err := Load(dispatcher.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	item := pending{event: Event{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repository, OldSHA: "a", NewSHA: "b"}, name: "index", executor: cfg.Executors["index"], count: 1}
	invocation, err := dispatcher.prepare(item)
	if err != nil {
		t.Fatal(err)
	}
	replacement := invocation.Run + ".replacement"
	if err := os.WriteFile(replacement, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, invocation.Run); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.revalidate(invocation); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("error=%v", err)
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
			errors <- appendReceipt(path, Receipt{SchemaVersion: receiptSchemaVersion, ID: string(rune('a' + index)), NewSHA: "new"})
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
		var got Receipt
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("invalid receipt line %q: %v", line, err)
		}
	}
}

func testDispatcher(t *testing.T) (Dispatcher, string) {
	t.Helper()
	root := t.TempDir()
	executable := filepath.Join(root, "outside", "indexer")
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(root, "checkout")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	return newTestDispatcher(t, root, executable), repository
}

func newTestDispatcher(t *testing.T, root, executable string) Dispatcher {
	t.Helper()
	config := writeTestConfig(t, root, executable, "warn")
	return Dispatcher{
		ConfigPath: config, StateDir: filepath.Join(root, "state"), ReceiptPath: filepath.Join(root, "receipts.jsonl"),
		Now: time.Now, EvalSymlinks: filepath.EvalSymlinks,
		LaunchWorker: func(WorkerRequest) error { return nil },
		VerifyCheckout: func(event Event) (string, os.FileInfo, error) {
			physical, err := filepath.EvalSymlinks(event.Checkout)
			if err != nil {
				return "", nil, err
			}
			info, err := os.Stat(physical)
			return physical, info, err
		},
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
