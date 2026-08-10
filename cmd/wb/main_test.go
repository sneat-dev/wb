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
	"github.com/spf13/cobra"
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
		if len(children) == 0 {
			result = append(result, persistentCommandID(command))
			return
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
