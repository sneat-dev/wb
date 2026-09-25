package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/hostload"
	"github.com/sneat-dev/wb/internal/runqueue"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

// testIsolatedQueueDir records the isolated CPU admission queue directory
// TestMain installs below, so TestTestMainIsolatesTheDefaultProjectsRoot...
// can assert the real installed value directly, rather than only that
// *some* redirection happened.
var testIsolatedQueueDir string

func TestMain(m *testing.M) {
	// Every CLI test in this package that invokes `wb run --` or `wb
	// worktree merge`/`prepare`/`resume` (directly via run()/root.Execute(),
	// in-process) must never depend on the real host load average: GitHub's
	// shared runners routinely report a load average of 8-10 on 4 vCPUs,
	// which used to fail any such test outright (sneat-dev/wb PR #450 run
	// 34124956543). Disable host-load admission by default for this whole
	// test binary; the dedicated tests in hostload_admission_test.go that
	// actually exercise gating behavior set WB_ADMISSION_LOAD_FLOOR back to
	// a positive value themselves — a positive value always wins, even
	// inside CI, so those tests still see real refusal/admission behavior.
	if err := os.Setenv(hostload.EnvLoadFloor, "0"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not disable host-load admission for tests: %v\n", err)
	}
	if len(os.Args) > 1 && os.Args[1] == sessionlaunch.PrivateLauncherArgument {
		os.Exit(sessionlaunch.RunPrivateLauncher(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == worktrees.SecureCleanupGitHelperArgument {
		os.Exit(worktrees.RunSecureCleanupGitHelper(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == hooks.SecureHooksGitHelperArgument {
		os.Exit(hooks.RunSecureHooksGitHelper(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == worktrees.SecureStageGitHelperArgument {
		os.Exit(worktrees.RunSecureStageGitHelper(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == worktrees.SecureCanonicalGitHelperArgument {
		os.Exit(worktrees.RunSecureCanonicalGitHelper(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == worktrees.SecureStageCanonicalGitHelperArgument {
		os.Exit(worktrees.RunSecureStageCanonicalGitHelper(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == worktrees.SecureRenameGitHelperArgument {
		os.Exit(worktrees.RunSecureRenameGitHelper(os.Args[2:]))
	}
	// Isolate the whole test binary from ambient ownership/session-identity
	// state before any test runs: this binary is a subprocess of whichever
	// agent is operating the shell that launched `go test`, and its
	// WB_AGENT_* exports would otherwise leak into every worktree/session
	// assertion below. See internal/testenv and internal/envguard.
	testenv.IsolateProcess()
	// Disable git's detached gc/maintenance for every git this binary
	// starts, including remote_test.go's own fixture clones/pushes and any
	// production git call under test, so no background writer can race
	// t.TempDir() cleanup (task-21). A bare remote pushed to over a local
	// transport still needs its own testenv.ConfigureGitAutoMaintenanceOff
	// call (see remote_test.go's setGitIdentity).
	testenv.GitAutoMaintenanceOffProcess()
	// Give this whole test binary its own CPU admission queue, isolated
	// from the real, machine-wide one (internal/runqueue) rooted at
	// whatever projectsRoot this process would otherwise resolve by
	// default (no explicit --projects-root). Without this, a test that
	// itself invokes `wb run --` in-process with no explicit root (e.g.
	// TestRunCommandAdmitsCPUHeavyWorkBelowFloor) joins the very same
	// queue an outer `wb run -- go test ./cmd/wb/...` already holds a
	// slot in, and waits behind its own outer holder forever — the known
	// deadlock this package's own tests hit on this VM (sneat-dev/wb#623).
	// The override is keyed to defaultProjectsRoot()'s result, captured
	// here before any test runs, so a test that passes its OWN explicit
	// --projects-root (e.g. TestRunCommandReportsQueueVisibilityOnStderr)
	// is unaffected and keeps contending for its own real, unoverridden
	// queue directory (PR #736 review finding B1). See
	// runqueue.SetQueueRootForTest.
	//
	// A failure to create the isolation directory must not let the suite
	// run un-isolated — that silently brings back the deadlock this
	// isolation exists to prevent (a hang, not a clean failure) — so exit
	// non-zero instead of merely warning.
	fromRoot := defaultProjectsRoot()
	queueDir, queueDirErr := os.MkdirTemp("", "wb-test-cpu-queue-")
	if queueDirErr != nil {
		fmt.Fprintf(os.Stderr, "fatal: could not isolate test CPU admission queue: %v\n", queueDirErr)
		os.Exit(1)
	}
	testIsolatedQueueDir = queueDir
	restoreQueueRoot := runqueue.SetQueueRootForTest(fromRoot, queueDir)
	code := m.Run()
	restoreQueueRoot()
	_ = os.RemoveAll(queueDir)
	os.Exit(code)
}

// TestTestMainIsolatesTheDefaultProjectsRootFromTheRealMachineQueue pins
// PR #736 review finding B1 (round 3) directly: nothing else in this
// package asserts that TestMain's own binary-wide isolation is actually in
// effect — TestRunCommandDoesNotDeadlockBehindAnOuterMachineWideCPUAdmissionHolder
// installs its own, separately keyed override for the deadlock test's
// own duration, which proves the runqueue mechanism works in general, but
// says nothing about whether TestMain's installation, for this binary's
// real default projects root, is still the one in effect. If TestMain's
// call became a no-op, or were keyed to anything other than the exact
// root an un-rooted `wb run --` resolves, the real sneat-dev/wb#623
// deadlock would return with no test here going red.
//
// This is a plain assertion, not a goroutine/deadline reproduction: it
// only needs to show that the isolation is wired, not race it.
func TestTestMainIsolatesTheDefaultProjectsRootFromTheRealMachineQueue(t *testing.T) {
	fromRoot := defaultProjectsRoot()
	got := runqueue.QueueDirForTest(fromRoot)
	realMachineQueueDir := filepath.Join(fromRoot, ".wb", "runtime", "cpu")
	if got == realMachineQueueDir {
		t.Fatalf("TestMain's isolation is not in effect: the default projects root %q resolves to the real, machine-wide queue directory %q", fromRoot, got)
	}
	if got != testIsolatedQueueDir {
		t.Fatalf("the default projects root %q resolves to %q, want TestMain's own isolated queue directory %q", fromRoot, got, testIsolatedQueueDir)
	}
}

func TestPropagateRuntimeWBExecutable(t *testing.T) {
	t.Parallel()

	t.Run("exports current executable when unset", func(t *testing.T) {
		t.Parallel()
		var gotName, gotValue string
		err := propagateRuntimeWBExecutable(
			func(string) (string, bool) { return "", false },
			func() (string, error) { return "/opt/wb/current/wb", nil },
			func(name, value string) error {
				gotName, gotValue = name, value
				return nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if gotName != "WB_EXECUTABLE" || gotValue != "/opt/wb/current/wb" {
			t.Fatalf("export = %s=%q", gotName, gotValue)
		}
	})

	t.Run("preserves explicit override", func(t *testing.T) {
		t.Parallel()
		called := false
		err := propagateRuntimeWBExecutable(
			func(name string) (string, bool) { return "/operator/wb", name == "WB_EXECUTABLE" },
			func() (string, error) {
				called = true
				return "", errors.New("must not inspect executable")
			},
			func(string, string) error {
				called = true
				return errors.New("must not replace override")
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if called {
			t.Fatal("explicit WB_EXECUTABLE was not preserved")
		}
	})

	t.Run("fails closed for a nonabsolute runtime path", func(t *testing.T) {
		t.Parallel()
		err := propagateRuntimeWBExecutable(
			func(string) (string, bool) { return "", false },
			func() (string, error) { return "bin/wb", nil },
			func(string, string) error { return nil },
		)
		if err == nil || !strings.Contains(err.Error(), "not absolute") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestPersistentFlagsAreRejectedWhenTheSelectedCommandCannotUseThem(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "filter-create", args: []string{"--filter", "acme", "worktree", "create", "task", "acme/app"}, want: "--filter is not supported by worktree create"},
		{name: "org-list", args: []string{"--org", "acme", "worktree", "list"}, want: "--org is not supported by worktree list"},
		{name: "filter-version", args: []string{"--filter", "acme", "version"}, want: "--filter is not supported by version"},
		{name: "filter-ci-direct", args: []string{"--filter", "acme", "ci", "audit"}, want: "--filter requires --fleet for ci audit"},
		{name: "filter-hooks-direct", args: []string{"--filter", "acme", "hooks", "check"}, want: "--filter requires --fleet for hooks check"},
		{name: "filter-coverage-direct", args: []string{"--filter", "acme", "coverage"}, want: "--filter requires --fleet for coverage"},
		{name: "root-verify-direct", args: []string{"--projects-root", t.TempDir(), "verify"}, want: "--projects-root requires --fleet for verify"},
		{name: "root-ci-direct", args: []string{"--projects-root", t.TempDir(), "ci", "audit"}, want: "--projects-root requires --fleet for ci audit"},
		{name: "filter-status-path", args: []string{"--filter", "acme", "status", "."}, want: "--filter is not supported by status with repository-path"},
		{name: "root-status-path", args: []string{"--projects-root", t.TempDir(), "status", "."}, want: "--projects-root is not supported by status with repository-path"},
		{name: "filter-repo-status", args: []string{"--filter", "acme", "repo", "status", "."}, want: "--filter is not supported by repo status"},
		{name: "root-repo-status", args: []string{"--projects-root", t.TempDir(), "repo", "status", "."}, want: "--projects-root is not supported by repo status"},
		{name: "org-deps-direct", args: []string{"--org", "acme", "deps", "graph"}, want: "--org requires --fleet for deps graph"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(test.args, &stdout, &stderr); code != exitUsage {
				t.Fatalf("run(%q) exit = %d, stderr=%s", test.args, code, stderr.String())
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), test.want)
			}
		})
	}
}

// TestPersistentFlagMatrix exercises every advertised root flag against every
// leaf command without running the command. A supported cell must reach RunE;
// every other cell must fail in PersistentPreRunE rather than be ignored.
func TestPersistentFlagMatrix(t *testing.T) {
	values := map[string]string{
		"projects-root":   t.TempDir(),
		"filter":          "acme",
		"org":             "acme",
		"non-interactive": "true",
	}
	for flag, value := range values {
		for _, commandID := range leafCommandIDs(newRootCmd()) {
			name := flag + "/" + strings.ReplaceAll(commandID, " ", "-")
			t.Run(name, func(t *testing.T) {
				root := newRootCmd()
				command, _, err := root.Find(strings.Fields(commandID))
				if err != nil {
					t.Fatal(err)
				}
				if err := root.PersistentFlags().Set(flag, value); err != nil {
					t.Fatal(err)
				}
				supported := persistentFlagSupport[flag][commandID] || persistentFlagSupport[flag]["*"]
				if supported && persistentFlagNeedsFleet(flag, commandID) {
					if fleet := command.Flags().Lookup("fleet"); fleet != nil {
						if err := command.Flags().Set("fleet", "true"); err != nil {
							t.Fatal(err)
						}
					}
				}
				rejectErr := rejectIgnoredPersistentFlags(command, nil)
				if supported && rejectErr != nil {
					t.Fatalf("supported matrix cell rejected: %v", rejectErr)
				}
				if !supported && rejectErr == nil {
					t.Fatalf("unsupported matrix cell accepted: --%s %s", flag, commandID)
				}
			})
		}
	}
}

func leafCommandIDs(root *cobra.Command) []string {
	var result []string
	var visit func(*cobra.Command)
	visit = func(command *cobra.Command) {
		children := command.Commands()
		if command != root && (command.Runnable() || len(children) == 0) {
			result = append(result, persistentCommandID(command))
		}
		for _, child := range children {
			if child.Name() == "help" || child.Name() == "completion" {
				continue
			}
			visit(child)
		}
	}
	visit(root)
	return result
}

func TestHasVersionFlagRecognisesOnlyRootLevelRequests(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"--version"}, true},
		{[]string{"-v"}, true},
		{[]string{"--non-interactive", "--version"}, true},
		{[]string{"version"}, false},         // the subcommand handles itself
		{[]string{"status", "-v"}, false},    // belongs to the subcommand
		{[]string{"--", "--version"}, false}, // after the terminator it is an argument
		{[]string{"deps", "graph", "--version"}, false},
		{[]string{"self-update", "--version", "v0.24.0"}, false}, // self-update's own --version, not the root's
		{[]string{"update", "--version", "v0.24.0"}, false},      // same via the alias
	}
	for _, test := range tests {
		if got := hasVersionFlag(test.args); got != test.want {
			t.Errorf("hasVersionFlag(%q) = %v, want %v", test.args, got, test.want)
		}
	}
}

// TestRunMapsOutcomesOntoDocumentedExitCodes pins the contract agents branch on.
// Before this, every failure — a typo in a flag as much as a real finding —
// came back as exit 1, and a command's own exit code was discarded entirely.
func TestRunMapsOutcomesOntoDocumentedExitCodes(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"help succeeds", []string{"--help"}, exitOK},
		{"version succeeds", []string{"version"}, exitOK},
		{"bare invocation prints help", nil, exitOK},
		{"unknown flag is a usage error", []string{"--no-such-flag"}, exitUsage},
		{"unknown command is a usage error", []string{"no-such-command"}, exitUsage},
		{"unknown subcommand flag is a usage error", []string{"status", "--no-such-flag"}, exitUsage},
		{"too many arguments is a usage error", []string{"version", "unexpected"}, exitUsage},
		{"a rejected flag value is a usage error", []string{"coverage", "--parallel", "not-a-number"}, exitUsage},
		// A command that starts (PersistentPreRunE runs, so commandStarted
		// becomes true) and then fails is a finding, not a usage error — this
		// is the exit-1 row TestConcurrentInvocationsReportOwnExitCode's
		// discriminating goroutine below relies on, and the plain
		// commandStarted propagation this table did not otherwise exercise.
		{"a started command that fails is a finding", []string{"deps", "graph", "--ecosystem", "bogus", "--non-interactive"}, exitFindings},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run(test.args, &stdout, &stderr); got != test.want {
				t.Errorf("run(%q) = %d, want %d; stderr: %s", test.args, got, test.want, stderr.String())
			}
		})
	}
}

// TestConcurrentInvocationsReportOwnExitCode is the regression test for #733:
// runWithStdin used to record whether a command started in a package-level
// `commandStarted` variable, shared by every concurrent call in this test
// binary. A real `wb` process only ever calls run() once, so that never
// raced in production, but many cmd/wb tests call run()/runWithStdin()
// directly with t.Parallel(), racing the shared variable from multiple
// goroutines in one process (race.yml run 36051478734, 24 DATA RACE blocks
// against the whole package-level `var` block in main.go). Building one
// *invocation per call and closing over it while constructing the command
// tree, instead of writing a package-level var, means two concurrent
// invocations now write to two different structs and so cannot
// cross-contaminate each other's exit code — the outcome this test pins,
// without needing -race (unavailable locally; go-ci's race job does not
// cover cmd/wb, and race.yml's cmd/wb run stays red on the other four
// package-level globals until the last PR in this sequence, so it would add
// no separate signal here) to see it hold.
//
// The two invocations must be chosen so a cross-contaminated commandStarted
// actually flips an exit code:
//   - "--no-such-flag" fails flag parsing before PersistentPreRunE ever runs,
//     so it reads commandStarted without either goroutine having a chance to
//     race a write into it — pairing it with "--help" (also pre-PreRunE, and
//     exitOK returns before commandStarted is read at all) cannot fail even
//     with the shared package-level var restored, because neither goroutine
//     ever sets "started" to observe. This was PR #737's mistake.
//   - The fix pairs the usage-error invocation with one that starts and then
//     fails with a plain (uncoded) error: "deps graph --ecosystem bogus
//     --non-interactive". Its ecosystem check runs inside RunE, after
//     PersistentPreRunE has set commandStarted = true, and returns a plain
//     fmt.Errorf before touching the network or the filesystem — pure,
//     fast, and safe under t.Parallel(). exitCodeFor(err, started) needs
//     started == true to report exitFindings (1) instead of exitUsage (2).
//     With the package-level var restored, a write from one goroutine
//     (start writing true, or the other resetting it to false at the top of
//     the next runWithStdin call) is visible to the other's read, flipping
//     either result: 5–13 of 200 usage runs and 5–11 of 200 findings runs
//     get the wrong code in local reproduction (no -race needed).
func TestConcurrentInvocationsReportOwnExitCode(t *testing.T) {
	t.Parallel()
	const iterations = 200

	usageResults := make([]int, iterations)
	findingsResults := make([]int, iterations)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range iterations {
			var stdout, stderr bytes.Buffer
			usageResults[i] = run([]string{"--no-such-flag"}, &stdout, &stderr)
		}
	}()
	go func() {
		defer wg.Done()
		for i := range iterations {
			var stdout, stderr bytes.Buffer
			findingsResults[i] = run([]string{"deps", "graph", "--ecosystem", "bogus", "--non-interactive"}, &stdout, &stderr)
		}
	}()
	wg.Wait()

	for i, got := range usageResults {
		if got != exitUsage {
			t.Errorf("usage invocation #%d = %d, want %d (cross-contaminated by the concurrent started-and-failed invocation)", i, got, exitUsage)
		}
	}
	for i, got := range findingsResults {
		if got != exitFindings {
			t.Errorf("started-and-failed invocation #%d = %d, want %d (cross-contaminated by the concurrent usage-error invocation)", i, got, exitFindings)
		}
	}
}

func TestEveryJSONShortcutHasCanonicalFormatFlag(t *testing.T) {
	for _, path := range subcommandPaths(newRootCmd(), nil) {
		command, _, err := newRootCmd().Find(path)
		if err != nil {
			t.Fatalf("find wb %s: %v", strings.Join(path, " "), err)
		}
		if command.Flags().Lookup("json") == nil {
			continue
		}
		if command.Flags().Lookup("format") == nil {
			t.Errorf("wb %s offers --json without canonical --format", strings.Join(path, " "))
		}
	}
}

func TestRunSeparatorPreservesChildOutputFlags(t *testing.T) {
	t.Setenv("WB_PROJECTS_ROOT", t.TempDir())
	var stdout, stderr bytes.Buffer
	args := []string{"run", "--", "/usr/bin/printf", "%s|%s", "--format=json", "--json"}
	if code := run(args, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit code = %d; stderr: %s", code, stderr.String())
	}
	if got, want := stdout.String(), "--format=json|--json"; got != want {
		t.Errorf("forwarded output = %q, want %q", got, want)
	}
}

// TestExitCodeForHonoursACommandsOwnCode proves the coded path is wired, not
// just the usage path. Before this change main discarded exitError.code and
// exited 1 for everything, so a command that chose a different code was
// indistinguishable from any other failure.
func TestExitCodeForHonoursACommandsOwnCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		err     error
		started bool
		want    int
	}{
		{"no error", nil, true, exitOK},
		{"coded finding", &exitError{code: exitFindings, message: "findings"}, true, exitFindings},
		{"coded three", &exitError{code: 3, message: "findings"}, true, 3},
		{"coded seven", &exitError{code: 7, message: "findings"}, true, 7},
		{"coded wins over not-started", &exitError{code: 5, message: "findings"}, false, 5},
		{"wrapped coded error", fmt.Errorf("context: %w", &exitError{code: 4, message: "f"}), true, 4},
		{"plain error after start", errors.New("boom"), true, exitFindings},
		{"plain error before start", errors.New("bad flag"), false, exitUsage},
	}
	for _, test := range tests {
		if got := exitCodeFor(test.err, test.started); got != test.want {
			t.Errorf("%s: exitCodeFor = %d, want %d", test.name, got, test.want)
		}
	}
}

// TestUsageErrorsExplainHowToRecover keeps the "errors name the fix" rule
// enforced rather than aspirational.
func TestUsageErrorsExplainHowToRecover(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"no-such-command"}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "wb --help") {
		t.Errorf("stderr does not name the recovery step: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("a rejected invocation wrote %q to stdout", stdout.String())
	}
}

// TestRootHelpDocumentsTheExitCodeContract makes the documentation load-bearing:
// an agent that reads --help must find the codes it is expected to branch on.
func TestRootHelpDocumentsTheExitCodeContract(t *testing.T) {
	t.Parallel()
	for _, fragment := range []string{"Exit codes", "0  success", "1  findings", "2  usage", "WB_NON_INTERACTIVE"} {
		if !strings.Contains(rootLongHelp, fragment) {
			t.Errorf("root help does not mention %q", fragment)
		}
	}
}

func TestVersionAlwaysIdentifiesTheBinary(t *testing.T) {
	t.Parallel()
	info := collectVersion()
	if info.Version == "" {
		t.Error("version is empty; an unstamped build must still report something")
	}
	if info.Go == "" || info.Platform == "" {
		t.Errorf("version info is incomplete: %+v", info)
	}
}
