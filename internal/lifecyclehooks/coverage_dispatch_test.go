package lifecyclehooks

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHkCovPackageLevelDispatchIsHermeticAndEmpty(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	report, err := Dispatch(context.Background(), nil)
	if err != nil || report.Enqueued != 0 || report.Executed != 0 || len(report.Warnings) != 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestHkCovDispatchWarnsWhenStateCannotBeInspected(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	blocker := hkCovWriteFile(t, filepath.Join(t.TempDir(), "blocker"), "x", 0o600)
	dispatcher.StateDir = filepath.Join(blocker, "state")
	report, err := dispatcher.Dispatch(context.Background(), nil)
	if err != nil || !containsText(report.Warnings, "read unseen lifecycle-hook failures") {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestHkCovDispatchSurfacesEnqueueFailure(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(dispatcher.pendingDir(), queueFileName(queueKey("index", checkout)))
	hkCovWriteFile(t, corrupt, "{broken", 0o600)
	if _, err := dispatcher.Dispatch(context.Background(), []Event{hkCovEvent(checkout)}); err == nil || !strings.Contains(err.Error(), "decode lifecycle hook queue item") {
		t.Fatalf("error=%v", err)
	}
}

func TestHkCovDispatchWarnsWhenWorkerCannotStart(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	dispatcher.LaunchWorker = func(WorkerRequest) error { return errors.New("no fork today") }
	report, err := dispatcher.Dispatch(context.Background(), []Event{hkCovEvent(checkout)})
	if err != nil || report.Enqueued != 1 || !containsText(report.Warnings, "background worker did not start") {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestHkCovPlanReportsWhatWouldBeQueued(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	events := []Event{
		{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: checkout, OldSHA: "a", NewSHA: "b", Cause: "pull"},
		{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: checkout, OldSHA: "b", NewSHA: "c", Cause: "merge"},
		{Name: EventCheckoutUpdated, Repository: "gitlab.com/acme/other", Checkout: checkout, OldSHA: "a", NewSHA: "b", Cause: "pull"},
	}
	planned, report, err := dispatcher.Plan(events)
	if err != nil || len(report.Warnings) != 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if len(planned) != 1 || planned[0].Executor != "index" || planned[0].CoalescedCount != 2 || planned[0].Event.NewSHA != "c" {
		t.Fatalf("planned=%+v", planned)
	}
}

func TestHkCovPlanWithoutHooksSectionPlansNothing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	config := hkCovWriteFile(t, filepath.Join(root, "wb.yaml"), "remote:\n  provider: git\n", 0o600)
	dispatcher := hkCovDispatcherFor(t, root, config)
	checkout := hkCovCheckout(t, root)
	planned, _, err := dispatcher.Plan([]Event{hkCovEvent(checkout)})
	if err != nil || len(planned) != 0 {
		t.Fatalf("planned=%+v err=%v", planned, err)
	}
}

func TestHkCovExecutorTimeoutDefaultsAndRejectsNonPositive(t *testing.T) {
	t.Parallel()
	if got, err := (Executor{}).timeout(); err != nil || got != 2*time.Minute {
		t.Fatalf("default timeout=%v err=%v", got, err)
	}
	if got, err := (Executor{Timeout: "3s"}).timeout(); err != nil || got != 3*time.Second {
		t.Fatalf("explicit timeout=%v err=%v", got, err)
	}
	if _, err := (Executor{Timeout: "soon"}).timeout(); err == nil {
		t.Fatal("expected unparseable timeout to fail")
	}
	if _, err := (Executor{Timeout: "0s"}).timeout(); err == nil {
		t.Fatal("expected non-positive timeout to fail")
	}
}

func TestHkCovPrepareSurfacesExecutableAndCheckoutFailures(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	event := hkCovEvent(checkout)
	relative := pending{event: event, name: "index", executor: Executor{Run: "relative/indexer"}}
	if _, err := dispatcher.prepare(relative); err == nil || !strings.Contains(err.Error(), "absolute executable path") {
		t.Fatalf("relative run error=%v", err)
	}

	executable := hkCovWriteFile(t, filepath.Join(t.TempDir(), "indexer.exe"), "#!/bin/sh\n", 0o755)
	brokenCheckout := dispatcher
	brokenCheckout.VerifyCheckout = func(Event) (string, os.FileInfo, error) { return "", nil, errors.New("checkout gone") }
	item := pending{event: event, name: "index", executor: Executor{Run: executable}}
	if _, err := brokenCheckout.prepare(item); err == nil || !strings.Contains(err.Error(), "checkout gone") {
		t.Fatalf("checkout error=%v", err)
	}
}

func TestHkCovPrepareBuildsInvocation(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	executable := hkCovWriteFile(t, filepath.Join(t.TempDir(), "indexer.exe"), "#!/bin/sh\n", 0o755)
	item := pending{event: hkCovEvent(checkout), name: "index", executor: Executor{Run: executable, Args: []string{"sync", "."}}}
	invocation, err := dispatcher.prepare(item)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Executor != "index" || invocation.Run != resolved || len(invocation.Args) != 2 || invocation.Dir == "" {
		t.Fatalf("invocation=%+v", invocation)
	}
	if envValue(invocation.Env, "WB_HOOK_EVENT") != EventCheckoutUpdated || envValue(invocation.Env, "WB_NEW_SHA") != "b" {
		t.Fatalf("environment=%v", invocation.Env)
	}
}

func TestHkCovInspectExecutableRejectsUnusableRuns(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	relative := dispatcher
	relative.EvalSymlinks = filepath.EvalSymlinks
	if _, err := relative.inspectExecutable("tools/indexer"); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative error=%v", err)
	}

	resolveFailed := dispatcher
	resolveFailed.EvalSymlinks = func(string) (string, error) { return "", errors.New("loop") }
	if _, err := resolveFailed.inspectExecutable("/tmp/indexer"); err == nil || !strings.Contains(err.Error(), "resolve executable") {
		t.Fatalf("resolve error=%v", err)
	}

	missing := dispatcher
	missing.EvalSymlinks = func(string) (string, error) { return filepath.Join(t.TempDir(), "absent.exe"), nil }
	if _, err := missing.inspectExecutable("/tmp/indexer"); err == nil || !strings.Contains(err.Error(), "inspect executable") {
		t.Fatalf("stat error=%v", err)
	}

	directory := dispatcher
	directory.EvalSymlinks = func(string) (string, error) { return t.TempDir(), nil }
	if _, err := directory.inspectExecutable("/tmp/indexer"); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory error=%v", err)
	}

	notExecutable := hkCovWriteFile(t, filepath.Join(t.TempDir(), "indexer"), "#!/bin/sh\n", 0o644)
	plain := dispatcher
	plain.EvalSymlinks = func(string) (string, error) { return notExecutable, nil }
	if _, err := plain.inspectExecutable("/tmp/indexer"); err == nil {
		t.Fatal("expected non-executable run to be rejected")
	}

	executable := hkCovWriteFile(t, filepath.Join(t.TempDir(), "indexer.exe"), "#!/bin/sh\n", 0o755)
	ok := dispatcher
	ok.EvalSymlinks = func(string) (string, error) { return executable, nil }
	inspected, err := ok.inspectExecutable("/ignored/indexer")
	if err != nil || inspected.resolved != executable || inspected.info == nil {
		t.Fatalf("inspected=%+v err=%v", inspected, err)
	}
}

func TestHkCovRevalidateRejectsEveryIdentityChange(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	executable := hkCovWriteFile(t, filepath.Join(t.TempDir(), "indexer.exe"), "#!/bin/sh\n", 0o755)
	item := pending{event: hkCovEvent(checkout), name: "index", executor: Executor{Run: executable}}
	invocation, err := dispatcher.prepare(item)
	if err != nil {
		t.Fatal(err)
	}

	relative := invocation
	relative.configuredRun = "relative/indexer"
	if err := dispatcher.revalidate(relative); err == nil || !strings.Contains(err.Error(), "revalidate executable") {
		t.Fatalf("relative revalidate error=%v", err)
	}

	brokenCheckout := dispatcher
	brokenCheckout.VerifyCheckout = func(Event) (string, os.FileInfo, error) { return "", nil, errors.New("checkout vanished") }
	if err := brokenCheckout.revalidate(invocation); err == nil || !strings.Contains(err.Error(), "revalidate checkout") {
		t.Fatalf("checkout revalidate error=%v", err)
	}

	swappedDir := invocation
	swappedDir.Dir = filepath.Join(t.TempDir(), "elsewhere")
	if err := dispatcher.revalidate(swappedDir); err == nil || !strings.Contains(err.Error(), "checkout identity changed") {
		t.Fatalf("checkout identity error=%v", err)
	}

	if err := dispatcher.revalidate(invocation); err != nil {
		t.Fatalf("unchanged invocation rejected: %v", err)
	}
}

func TestHkCovRepositoryIdentityFailures(t *testing.T) {
	t.Parallel()
	if _, err := RepositoryIdentity(t.TempDir()); err == nil {
		t.Fatal("expected missing origin to fail")
	}
	repository := hkCovGitRepository(t, "not-a-valid-remote")
	if _, err := RepositoryIdentity(repository); err == nil || !strings.Contains(err.Error(), "remote") {
		t.Fatalf("unparseable origin error=%v", err)
	}
}

func TestHkCovRepositoryIdentityNormalizesSupportedOrigin(t *testing.T) {
	t.Parallel()
	repository := hkCovGitRepository(t, "https://github.com/ACME/App.git")
	identity, err := RepositoryIdentity(repository)
	if err != nil || identity != "github.com/acme/app" {
		t.Fatalf("identity=%q err=%v", identity, err)
	}
}

func hkCovGitRepository(t *testing.T, origin string) string {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"init"}, {"remote", "add", "origin", origin}} {
		command := exec.Command("git", append([]string{"-C", repository}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v: %s", err, output)
		}
	}
	return repository
}

func TestHkCovDefaultReceiptPathHonoursXDGAndHomeFallback(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	if got, want := defaultReceiptPath(), filepath.Join(stateHome, "wb", "lifecycle-hook-events.jsonl"); got != want {
		t.Fatalf("receipt path=%q want %q", got, want)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	if got := defaultReceiptPath(); got != filepath.Join(".wb", "lifecycle-hook-events.jsonl") {
		t.Fatalf("fallback receipt path=%q", got)
	}
}

func TestHkCovDefaultsFillsMissingFields(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	filled := Dispatcher{}.defaults()
	if filled.ConfigPath == "" || filled.ReceiptPath == "" || filled.StateDir == "" {
		t.Fatalf("filled=%+v", filled)
	}
	if filled.Now == nil || filled.Run == nil || filled.EvalSymlinks == nil || filled.LaunchWorker == nil || filled.VerifyCheckout == nil {
		t.Fatalf("injected defaults missing: %+v", filled)
	}

	custom := Dispatcher{ReceiptPath: filepath.Join(t.TempDir(), "custom", "receipts.jsonl")}.defaults()
	if custom.StateDir != filepath.Join(filepath.Dir(custom.ReceiptPath), "lifecycle-hooks") {
		t.Fatalf("custom state dir=%q", custom.StateDir)
	}

	defaulted := Dispatcher{ReceiptPath: defaultReceiptPath()}.defaults()
	if defaulted.StateDir != DefaultDispatcher().StateDir {
		t.Fatalf("default state dir=%q", defaulted.StateDir)
	}
}

func TestHkCovFailureClassDistinguishesExitAndConfiguration(t *testing.T) {
	t.Parallel()
	if got := failureClass(errors.New("bad config")); got != "configuration" {
		t.Fatalf("configuration class=%q", got)
	}
	command := exec.Command(os.Args[0], "-hk-cov-definitely-unknown-flag")
	err := command.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Skipf("test binary did not produce an exit error: %v", err)
	}
	if got := failureClass(err); got != "exit" {
		t.Fatalf("exit class=%q for %v", got, err)
	}
}

func TestHkCovLaunchWorkerSpawnsDetachedRunPending(t *testing.T) {
	root := t.TempDir()
	argvPath := filepath.Join(root, "argv.txt")
	t.Setenv("HKCOV_LAUNCH_WORKER_CHILD", "1")
	t.Setenv("HKCOV_LAUNCH_WORKER_ARGV", argvPath)
	request := WorkerRequest{ConfigPath: "/tmp/wb.yaml", StateDir: "/tmp/state", ReceiptPath: "/tmp/receipts.jsonl"}
	if err := launchWorker(request); err != nil {
		t.Fatalf("launch worker: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	var recorded string
	for {
		raw, err := os.ReadFile(argvPath)
		if err == nil {
			recorded = strings.TrimSpace(string(raw))
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker child did not record its arguments: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	arguments := strings.Split(recorded, "\n")
	if len(arguments) != 10 {
		t.Fatalf("worker arguments=%v, want executable plus 9 arguments", arguments)
	}
	want := strings.Join([]string{"hooks", "lifecycle", "run-pending", "--config", request.ConfigPath, "--state-dir", request.StateDir, "--receipt", request.ReceiptPath}, " ")
	if got := strings.Join(arguments[1:], " "); got != want {
		t.Fatalf("worker arguments=%q, want %q", got, want)
	}
	if arguments[0] == "" {
		t.Fatal("worker child did not report its executable path")
	}
}
