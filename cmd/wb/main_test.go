package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestMain(m *testing.M) {
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
	os.Exit(m.Run())
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
