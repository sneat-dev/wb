// Command wb is the workbench CLI: fleet-wide operations across the user's
// GitHub repositories, plus repo-sync.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

// Exit codes are part of wb's contract. Agents and CI branch on them, so they
// are documented in the root help and must not be renumbered.
const (
	exitOK       = 0 // the command ran and reported nothing that needs attention
	exitFindings = 1 // the command ran and reported failures, drift, or findings
	exitUsage    = 2 // the invocation was rejected before any work started
)

var (
	projectsRoot   string
	filterFlag     string
	extraOrgs      []string
	nonInteractive bool

	// commandStarted records that cobra accepted the invocation and began
	// running a command. See the PersistentPreRunE in newRootCmd.
	commandStarted bool
)

const rootLongHelp = `Workbench CLI — fleet-wide operations across your GitHub repositories.

wb is meant to be driven by scripts and AI agents as well as by people. Every
command terminates, writes results to stdout and diagnostics to stderr, and
never waits for input that was not explicitly piped in. Reporting commands
accept a machine-readable output format.

Exit codes:
  0  success  — the command ran and reported nothing that needs attention
  1  findings — the command ran and reported failures, drift, or policy findings
  2  usage    — the invocation was rejected before any work started

Terminal-only behaviour, such as the live sync progress UI, activates only when
stdout is a terminal. Pass --non-interactive, or set WB_NON_INTERACTIVE=1, to
force plain line-buffered output even when a terminal is attached.`

func newRootCmd() *cobra.Command {
	home, _ := os.UserHomeDir()
	root := &cobra.Command{
		Use:           "wb",
		Short:         "Workbench CLI — fleet-wide operations across your GitHub repositories",
		Long:          rootLongHelp,
		SilenceErrors: true,
		SilenceUsage:  true,
		// Without this, cobra runs the root command for a bare `wb` and prints
		// nothing at all. An agent reads an empty successful run as "the tool
		// works and there is nothing to do", so show the help instead.
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
		// Cobra validates flags and arguments before any PersistentPreRun, so
		// reaching this hook proves the invocation itself was accepted. That is
		// what separates a usage error (exit 2) from a command that ran and
		// found problems (exit 1) — far more reliable than matching on the text
		// of cobra's error messages. No subcommand may define its own
		// PersistentPreRun, which would shadow this one.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			commandStarted = true
			return nil
		},
	}
	root.PersistentFlags().StringVar(&projectsRoot, "projects-root", filepath.Join(home, "projects"), "root dir containing {org}/{repo}")
	root.PersistentFlags().StringVar(&filterFlag, "filter", "", "only repos whose org/name contains this substring")
	root.PersistentFlags().StringArrayVar(&extraOrgs, "org", nil, "additional GitHub owner to query (repeatable)")
	root.PersistentFlags().BoolVar(&nonInteractive, "non-interactive", false, "never use a terminal UI or wait for input, even on a terminal")

	// --version is what people and agents reach for first; `wb version` carries
	// the same information and adds --json for programmatic use.
	root.Flags().BoolP("version", "v", false, "print the wb version and exit")

	root.AddCommand(newVersionCmd())
	root.AddCommand(newSyncCmd())
	root.AddCommand(newRunCmd())
	root.AddCommand(newMigrateCmd())
	root.AddCommand(newDepsCmd())
	root.AddCommand(newCICmd())
	root.AddCommand(newHooksCmd())
	root.AddCommand(newCoverageCmd())
	root.AddCommand(newVerifyCmd())
	root.AddCommand(newCheckCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newWorktreeCmd())
	root.AddCommand(newSelfUpdateCmd())

	return root
}

func main() {
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
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the CLI and maps the outcome onto a documented exit code. It is
// separate from main so tests can drive the whole command surface without
// spawning a process or terminating the test binary.
func run(args []string, stdout, stderr io.Writer) int {
	// Handled before Execute so `wb --version` answers even when a later flag
	// is invalid, and so it never depends on subcommand wiring.
	if hasVersionFlag(args) {
		return printVersion(stdout, false)
	}

	commandStarted = false
	root := newRootCmd()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	err := root.Execute()
	if err == nil {
		return exitOK
	}
	_, _ = fmt.Fprintln(stderr, "error:", err)
	code := exitCodeFor(err, commandStarted)
	if code == exitUsage {
		_, _ = fmt.Fprintln(stderr, "run `wb --help` to see the available commands and flags.")
	}
	return code
}

// exitCodeFor maps the outcome of cobra's Execute onto a documented exit code.
// A command that ran and found problems reports its own code; anything rejected
// before work started — a bad flag, a bad argument, an unknown command — is a
// usage error, which an agent must be able to tell apart from a real finding
// so it retries with a corrected invocation rather than reporting a failure.
func exitCodeFor(err error, started bool) int {
	if err == nil {
		return exitOK
	}
	var coded *exitError
	if errors.As(err, &coded) {
		return coded.code
	}
	if !started {
		return exitUsage
	}
	return exitFindings
}

// hasVersionFlag reports whether the root-level version flag was requested. It
// stops at the first non-flag token so a subcommand's own -v is never taken as
// a request for the wb version.
func hasVersionFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--version" || arg == "-v" {
			return true
		}
		if len(arg) == 0 || arg[0] != '-' {
			return false
		}
	}
	return false
}
