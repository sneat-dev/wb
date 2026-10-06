package lifecyclehooks_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/cmdhooks"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/spf13/cobra"
)

func newHooksCmd(inv *testInvocation) *cobra.Command {
	return cmdhooks.New(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: inv.projectsRoot, Filter: inv.filterFlag} }}, cmdhooks.GitOperations{}, cmdhooks.LifecycleOperations{
		Check: func(config string) (lifecyclehooks.CheckReport, error) {
			d := lifecyclehooks.DefaultDispatcher()
			if config != "" {
				d.ConfigPath = config
			}
			return d.Check()
		},
		Status: func(limit int) (lifecyclehooks.Status, error) {
			return lifecyclehooks.DefaultDispatcher().Status(limit)
		},
		Resume: func() (lifecyclehooks.ResumeReport, error) { return lifecyclehooks.DefaultDispatcher().Resume() },
		Retry:  func(id string) (lifecyclehooks.Report, error) { return lifecyclehooks.DefaultDispatcher().Retry(id) },
		GC: func(options lifecyclehooks.GCOptions) (lifecyclehooks.GCReport, error) {
			return lifecyclehooks.DefaultDispatcher().GC(options)
		},
		Backfill: func(ctx context.Context, root, filter string, apply bool) (lifecyclehooks.BackfillPlan, error) {
			return lifecyclehooks.Backfill(ctx, root, filter, lifecyclehooks.DefaultDispatcher(), apply)
		},
		Drain: func(ctx context.Context, options cmdhooks.DrainOptions) (lifecyclehooks.Report, error) {
			d := lifecyclehooks.DefaultDispatcher()
			d.ConfigPath = options.ConfigPath
			d.StateDir = options.StateDir
			d.ReceiptPath = options.ReceiptPath
			return d.Drain(ctx, options.Parallel)
		},
	}, cmdhooks.AgentOperations{})
}

type testInvocation struct{ projectsRoot, filterFlag string }

func selected(inv *testInvocation, path ...string) *cobra.Command {
	cmd := newHooksCmd(inv)
	for _, name := range path {
		for _, child := range cmd.Commands() {
			if child.Name() == name {
				cmd = child
				break
			}
		}
	}
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	if parent := cmd.Parent(); parent != nil {
		parent.RemoveCommand(cmd)
	}
	return cmd
}

func newHooksLifecycleCheckCmd() *cobra.Command {
	return selected(&testInvocation{}, "lifecycle", "check")
}
func newHooksLifecycleStatusCmd() *cobra.Command {
	return selected(&testInvocation{}, "lifecycle", "status")
}
func newHooksLifecycleResumeCmd() *cobra.Command {
	return selected(&testInvocation{}, "lifecycle", "resume")
}

func newHooksLifecycleGCCmd() *cobra.Command { return selected(&testInvocation{}, "lifecycle", "gc") }
func run(args []string, out, errOut io.Writer) int {
	projects := ""
	clean := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--projects-root" {
			i++
			projects = args[i]
			continue
		}
		if args[i] == "--non-interactive" {
			continue
		}
		clean = append(clean, args[i])
	}
	cmd := newHooksCmd(&testInvocation{projectsRoot: projects})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	if len(clean) > 0 && clean[0] == "hooks" {
		clean = clean[1:]
	}
	cmd.SetArgs(clean)
	if cmd.Execute() != nil {
		return 1
	}
	return 0
}

const exitOK = 0

func initTestRepository(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	testenv.Git(t, path, "init", "-b", "main")
	testenv.Git(t, path, "config", "user.email", "wb@example.test")
	testenv.Git(t, path, "config", "user.name", "WB Test")
	testenv.Git(t, path, "commit", "--allow-empty", "-m", "init")
	return path
}
func initOriginRepository(t *testing.T, path, slug string) string {
	initTestRepository(t, path)
	testenv.Git(t, path, "remote", "add", "origin", "https://github.com/"+slug+".git")
	return path
}

//nolint:paralleltest // Process-wide environment changes in TestLifecycleStatusCommandShowsPrivateDiagnosticsAndRetry; these rows share their parent environment and remain sequential.
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
func TestLifecycleCheckCommandReportsTrustedExecutor(t *testing.T) {
	t.Parallel()
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

//nolint:paralleltest // Process-wide environment changes in TestLifecycleResumeAndGCAreSafeOnEmptyState; these rows share their parent environment and remain sequential.
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
func TestLifecycleBackfillPlansAndAppliesOnlyMatchingCanonicalRepositories(t *testing.T) {
	t.Parallel()
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
	plan, err := lifecyclehooks.Backfill(context.Background(), root, "", dispatcher, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Apply || plan.Scanned != 2 || len(plan.Executions) != 1 || plan.Executions[0].Event.Repository != "github.com/acme/app" || plan.Executions[0].Event.Cause != "lifecycle-backfill" {
		t.Fatalf("plan=%+v", plan)
	}
	applied, err := lifecyclehooks.Backfill(context.Background(), root, "", dispatcher, true)
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
	if err := testenv.WriteExecutableFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
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
func TestLifecycleCheckTextDistinguishesMissingAndTrustedConfiguration(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	missing := filepath.Join(root, "missing.yaml")
	for _, test := range []struct {
		name, config, want string
	}{
		{"missing", missing, "Lifecycle hooks not configured"},
		{"trusted", lifecycleTestConfig(t, root, lifecycleTestExecutable(t, root)), "code-index"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cmd := newHooksLifecycleCheckCmd()
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetArgs([]string{"--config", test.config})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("check output %q lacks %q", output.String(), test.want)
			}
		})
	}
}

//nolint:paralleltest // Process-wide environment changes in TestLifecycleBackfillTextNamesPlannedExecutionAndPreviewAction; these rows share their parent environment and remain sequential.
func TestLifecycleBackfillTextNamesPlannedExecutionAndPreviewAction(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	initOriginRepository(t, filepath.Join(projects, "acme", "app"), "acme/app")
	configHome := filepath.Join(root, "config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	executable := lifecycleTestExecutable(t, root)
	config := filepath.Join(configHome, "wb", "wb.yaml")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(lifecycleConfigContents(executable)), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	args := []string{"hooks", "lifecycle", "backfill", "--projects-root", projects}
	if code := run(args, &out, &diagnostic); code != exitOK {
		t.Fatalf("run(%q) = %d: %s", args, code, diagnostic.String())
	}
	for _, want := range []string{"planned 1 execution(s) from 1 repositories", "github.com/acme/app@", "Re-run with --apply"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("backfill output %q lacks %q", out.String(), want)
		}
	}
}

//nolint:paralleltest // Process-wide environment changes in TestLifecycleGCTextReportsEmptyPreview; these rows share their parent environment and remain sequential.
func TestLifecycleGCTextReportsEmptyPreview(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	cmd := newHooksLifecycleGCCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "would remove 0 of 0 receipts") {
		t.Fatalf("GC preview = %q", out.String())
	}
}
