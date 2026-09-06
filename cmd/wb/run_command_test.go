package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/runlog"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestRunCommandPreservesStreamsAndExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the WB fleet runs on macOS and Linux")
	}
	t.Chdir(t.TempDir())

	var stdout, stderr bytes.Buffer
	code := runWithStdin(
		[]string{"run", "--", "/bin/sh", "-c", "read value; printf 'out:%s:%s' \"$value\" \"$WB_OPERATION_ID\"; printf 'err:%s' \"$value\" >&2; exit 7"},
		strings.NewReader("hello\n"),
		&stdout,
		&stderr,
	)
	if code != 7 {
		t.Fatalf("exit code = %d, want child exit code 7; stderr=%s", code, stderr.String())
	}
	if got := stdout.String(); !strings.HasPrefix(got, "out:hello:wbo-") {
		t.Errorf("stdout = %q, want child stdout and operation ID", got)
	}
	if got := stderr.String(); !strings.Contains(got, "err:hello") {
		t.Errorf("stderr = %q, want child stderr", got)
	}
}

func TestRunCommandRejectsRecipeFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--apply", "--", "/bin/true"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "belong to WB modes") {
		t.Errorf("stderr does not explain the incompatible flag: %s", stderr.String())
	}
}

func TestRunAsyncFlagRequiresCommandMode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--async", "recipe-name"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--async requires command mode") {
		t.Errorf("stderr does not explain async command mode: %s", stderr.String())
	}
}

func TestRunAsyncRequiresExplicitStableWorker(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--async", "--", "go", "test"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--async requires --worker <stable-id>") || !strings.Contains(stderr.String(), "never guesses") {
		t.Errorf("stderr does not explain worker binding: %s", stderr.String())
	}
}

func TestRunWorkerFlagRequiresAsyncCommandMode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--worker", "agent-a", "--", "go", "test"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--worker requires --async command mode") {
		t.Errorf("stderr does not explain worker binding: %s", stderr.String())
	}
}

func TestRunIdempotencyKeyRequiresAsyncCommandMode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--idempotency-key", "retry-1", "--", "go", "test"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--idempotency-key requires --async command mode") {
		t.Errorf("stderr does not explain idempotency scope: %s", stderr.String())
	}
}

func TestDaemonRawSubmitReportsAdministratorOptInWithoutWritingPolicy(t *testing.T) {
	root := t.TempDir()
	policyPath := filepath.Join(t.TempDir(), "daemon-raw-exec.json")
	previousRoot := projectsRoot
	projectsRoot = root
	t.Cleanup(func() { projectsRoot = previousRoot })
	t.Chdir(root)

	deps := daemonTestDependencies(t, root)
	deps.rawPolicy = func(root string) (bool, string, error) {
		allowed, err := daemon.LoadRawExecutionPolicy(policyPath, root)
		return allowed, policyPath, err
	}
	command := newDaemonOperationSubmitCmd(deps)
	command.SetArgs([]string{"--", "/bin/echo", "hello"})
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	err := command.Execute()
	if err == nil {
		t.Fatal("expected raw execution policy denial")
	}
	want := "an administrator must create " + policyPath + " with mode 0600 and contents {\"version\":1,\"allow_raw_daemon_execution\":true}"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("async denial = %v, want %q", err, want)
	}
	if _, statErr := os.Stat(policyPath); !os.IsNotExist(statErr) {
		t.Fatalf("CLI wrote raw execution policy: %v", statErr)
	}
}

func TestGovernedEnvironmentCapsChildParallelism(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=readonly")
	environment := governedEnvironment([]string{"PATH=/bin"}, "wbo-test", []string{"go", "test", "./..."}, 2)
	joined := strings.Join(environment, "\n")
	for _, want := range []string{
		"WB_OPERATION_ID=wbo-test",
		"WB_CPU_UNITS=2",
		"GOMAXPROCS=2",
		"NX_PARALLEL=2",
		"GOFLAGS=-mod=readonly -p=1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment is missing %q:\n%s", want, joined)
		}
	}
}

func TestRunHistorySummarizesCurrentWorktree(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "init", "-b", "main")
	git.Dir = root
	if output, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	manifest := worktrees.Manifest{
		Version: 1, EffortID: "run-history", EffortKind: worktrees.EffortKindFeature,
		Repository: "acme/app", Worktree: root, Branch: "run-history", Base: "main",
		BaseSHA: strings.Repeat("a", 40), CreatedAt: time.Now().UTC(),
		RunID: "run-1", ClaimID: strings.Repeat("b", 64), Provenance: worktrees.ProvenanceCreated,
	}
	if err := worktrees.WriteManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	recorder, err := runlog.Begin(root, []string{"go", "test", "./cmd/wb"}, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Finish(0, 10*time.Millisecond, 5*time.Millisecond, time.Now()); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("HOME", t.TempDir())

	var stdout, stderr bytes.Buffer
	if code := run([]string{"run", "--history", "--days", "1"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"operations 1", "go/test", "CPU"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("history does not contain %q:\n%s", want, stdout.String())
		}
	}
}
