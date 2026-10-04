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
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

// TestRunRecipeCommandThreadsExtraOrgsIntoFleetDiscoveryInProcess proves that
// running a named recipe (not --list) reaches runRun's fleet discovery with
// the real invocation's extraOrgs, not a hardcoded empty one: a fake gh
// records every argv it is called with, and the distinguishing org set only
// on inv.extraOrgs appears in that log only if runRun actually received it.
func TestRunRecipeCommandThreadsExtraOrgsIntoFleetDiscoveryInProcess(t *testing.T) {
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "gh-calls.log")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + logPath + "\n" +
		`if [ "$1" = "api" ] && [ "$2" = "user" ]; then printf 'HTTP/2 200 OK\n\n{"login":"cwcov-user"}\n'; exit 0; fi` + "\n" +
		`if [ "$1" = "api" ] && [ "$2" = "user/orgs" ]; then printf 'HTTP/2 200 OK\n\n[]\n'; exit 0; fi` + "\n" +
		`if [ "$1" = "repo" ] && [ "$2" = "list" ]; then printf '[]\n'; exit 0; fi` + "\n" +
		`printf '{"total_count":0,"items":[]}\n'` + "\n" +
		"exit 0\n"
	if err := testenv.WriteExecutableFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("WB_HOME", t.TempDir())

	configPath := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(configPath, []byte("recipes:\n  refresh-ci:\n    type: command\n    command: \"true\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	projectsRoot := t.TempDir()
	inv := &invocation{projectsRoot: projectsRoot, extraOrgs: []string{"target-org-9f3"}}
	if _, _, err := cwCovExec(t, projectsRoot, func() *cobra.Command { return newRunCmd(inv) },
		"refresh-ci", "--config", configPath); err != nil {
		t.Fatalf("wb run refresh-ci: %v", err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read gh call log: %v", err)
	}
	if !strings.Contains(string(log), "target-org-9f3") {
		t.Fatalf("gh calls = %q, want a repo list call naming the invocation's extraOrgs", log)
	}
}

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

func TestDaemonRawSubmitReportsAdministratorOptInWithoutWritingPolicy(t *testing.T) {
	root := t.TempDir()
	policyPath := filepath.Join(t.TempDir(), "daemon-raw-exec.json")
	t.Chdir(root)

	deps := daemonTestDependencies(t, root)
	deps.RawPolicy = func(root string) (bool, string, error) {
		allowed, err := daemon.LoadRawExecutionPolicy(policyPath, root)
		return allowed, policyPath, err
	}
	command := newDaemonOperationSubmitCmd(&invocation{}, deps)
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
