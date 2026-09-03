// Command wb is the workbench CLI: fleet-wide operations across the user's
// GitHub repositories, plus repo-sync.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"charm.land/fang/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
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

The fastest agent workflow is:

  Start isolated work   wb worktree create <task> <owner/repository>
  Inspect progress      wb worktree summary <task>
  Land and clean up     wb worktree merge <worktree> --route auto --cleanup

Not sure which command matches an intent? Search the structured catalog:

  wb commands --search "finish work" --format json

wb is designed for scripts and AI agents as well as people. Every command
terminates, writes results to stdout and diagnostics to stderr, and never waits
for input that was not explicitly piped in. Reporting commands accept a
machine-readable output format.

Exit codes:
  0  success  — the command ran and reported nothing that needs attention
  1  findings — the command ran and reported failures, drift, or policy findings
  2  usage    — the invocation was rejected before any work started

Terminal-only behaviour, including styled help and live progress reporting,
activates only when its output stream is a terminal. Pass --non-interactive, or
set WB_NON_INTERACTIVE=1, to suppress terminal styling, UIs, and progress lines
even when a terminal is attached.`

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
			if err := rejectIgnoredPersistentFlags(cmd, args); err != nil {
				return err
			}
			commandStarted = true
			// Publish which command is running so anything it writes into a
			// worktree records what touched it, without each call site having
			// to thread the name through.
			worktrees.SetInvokedCommand(persistentCommandID(cmd))
			maybeWarnSkillsDrift(cmd)
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

	configureRootHelp(root)
	root.AddCommand(
		groupedRootCommand(newWorktreeCmd(), rootGroupAgent),
		groupedRootCommand(newBranchCmd(), rootGroupAgent),
		groupedRootCommand(newSessionCmd(), rootGroupAgent),
		groupedRootCommand(newStreamCmd(), rootGroupChange),
		groupedRootCommand(newStatusCmd(), rootGroupFleet),
		groupedRootCommand(newFleetCmd(), rootGroupFleet),
		groupedRootCommand(newSyncCmd(), rootGroupFleet),
		groupedRootCommand(newRepoCmd(), rootGroupFleet),
		groupedRootCommand(newCoverageCmd(), rootGroupQuality),
		groupedRootCommand(newVerifyCmd(), rootGroupQuality),
		groupedRootCommand(newCheckCmd(), rootGroupQuality),
		groupedRootCommand(newCICmd(), rootGroupQuality),
		groupedRootCommand(newHooksCmd(), rootGroupQuality),
		groupedRootCommand(newDepsCmd(), rootGroupChange),
		groupedRootCommand(newMigrateCmd(), rootGroupChange),
		groupedRootCommand(newRunCmd(), rootGroupChange),
		groupedRootCommand(newRemoteCmd(), rootGroupMaintain),
		groupedRootCommand(newLayoutCmd(), rootGroupMaintain),
		groupedRootCommand(newArchiveCmd(), rootGroupMaintain),
		groupedRootCommand(newSelfUpdateCmd(), rootGroupLearn),
		groupedRootCommand(newSkillsCmd(), rootGroupLearn),
		groupedRootCommand(newVersionCmd(), rootGroupLearn),
		groupedRootCommand(newCommandsCmd(), rootGroupLearn),
	)

	return root
}

// persistentFlagSupport is an executable counterpart to
// docs/cli-flag-matrix.md. A persistent flag must either affect the selected
// command or be rejected before that command starts; accepting a flag and
// silently ignoring it is unsafe for scripts and agents.
var persistentFlagSupport = map[string]map[string]bool{
	"projects-root": {
		"sync": true, "run": true, "migrate": true,
		"deps graph": true, "deps set": true, "deps bump": true, "deps publish npm": true, "deps drift": true,
		"deps propagate local": true,
		"ci audit":             true,
		"hooks install":        true, "hooks check": true, "hooks repair": true, "hooks run": true,
		"hooks agent pre-tool-use": true, "hooks agent install": true,
		"coverage": true, "verify": true, "check": true, "status": true,
		"verify receipt": true,
		"fleet":          true, "fleet overview": true, "fleet stats": true, "fleet status": true, "remote publish": true,
		"remote status": true, "remote machines": true,
		"remote claim": true, "remote release": true, "remote claims": true,
		"layout audit": true, "layout clean": true, "archive clean": true,
		"worktree abort": true, "worktree create": true, "worktree guard": true, "worktree marker": true, "worktree rescue": true,
		"worktree list": true, "worktree cleanup": true, "worktree gc": true, "worktree rename": true,
		"worktree merge": true, "worktree merge prepare": true, "worktree merge land": true, "worktree merge resume": true, "worktree merge revert": true, "worktree merge acknowledge-landed-failed": true, "worktree merge acknowledge-stranded-landing": true, "worktree merge seal-validation-failed": true, "worktree merge supersede-validation-failed": true,
		"worktree orphans": true, "worktree backfill": true, "worktree log": true, "worktree info": true,
		"worktree own": true,
		"stream start": true, "stream join": true, "stream status": true, "stream end": true, "stream delete": true,
		"session register": true, "session list": true, "session prune": true, "session move": true, "session receive": true, "session receive-park": true, "session park": true, "session resume": true,
		"session send": true, "session request-handoff": true, "session receive-message": true,
		"branch list": true, "branch cleanup": true,
		"worktree log init": true, "worktree log steer": true, "worktree log show": true,
		"worktree log checkpoint": true, "worktree log refresh": true, "worktree log integrate": true,
		"worktree log handoff": true, "worktree log recover": true, "worktree log finalize": true,
		"worktree log sync": true, "worktree log archive": true,
		"worktree summary": true,
	},
	"filter": {
		"sync": true, "run": true,
		"deps graph": true, "deps set": true, "deps bump": true, "deps publish npm": true, "deps drift": true,
		"deps propagate local": true,
		"ci audit":             true,
		"hooks install":        true, "hooks check": true, "hooks repair": true,
		"coverage": true, "verify": true, "check": true, "status": true,
		"fleet": true, "fleet overview": true, "fleet stats": true, "fleet status": true, "remote publish": true,
		"worktree list": true, "worktree cleanup": true, "worktree gc": true, "worktree rename": true,
		"worktree summary": true, "worktree abort": true, "worktree marker": true, "worktree rescue": true,
		"branch list": true, "branch cleanup": true,
		"archive clean": true,
	},
	"org": {"sync": true, "run": true, "deps graph": true, "deps set": true, "deps bump": true, "deps publish npm": true, "deps drift": true, "fleet prs": true},
	// This is a root rendering/input-safety guarantee. Commands without a TUI
	// still consume it by inheriting the non-blocking contract; rejecting it
	// would make scripts need command-specific conditionals for no benefit.
	"non-interactive": {"*": true},
}

func rejectIgnoredPersistentFlags(cmd *cobra.Command, args []string) error {
	commandID := persistentCommandID(cmd)
	for flag, supported := range persistentFlagSupport {
		// Inspect only the root flag. A command-local flag may intentionally
		// shadow a persistent name (sync --org restricts owners); treating that
		// local flag as the root contract previously rejected valid invocations.
		candidate := cmd.Root().PersistentFlags().Lookup(flag)
		if candidate == nil || !candidate.Changed {
			continue
		}
		if !supported[commandID] && !supported["*"] {
			return fmt.Errorf("--%s is not supported by %s; see docs/cli-flag-matrix.md", flag, commandID)
		}
		// Some leaves have both direct and fleet modes. A root selector cannot
		// truthfully affect a named path, so accept it only when the invocation
		// selected the fleet. Status is fleet-by-default when no path is given;
		// the other leaves use an explicit --fleet flag.
		if persistentFlagNeedsFleet(flag, commandID) && !persistentCommandSelectedFleet(cmd, commandID, args) {
			if commandID == "status" {
				return fmt.Errorf("--%s is not supported by status with repository-path; omit the path to select the --projects-root fleet", flag)
			}
			if commandID == "repo status" {
				return fmt.Errorf("--%s is not supported by repo status; use wb fleet status for the --projects-root fleet", flag)
			}
			return fmt.Errorf("--%s requires --fleet for %s; see docs/cli-flag-matrix.md", flag, commandID)
		}
	}
	return nil
}

func persistentFlagNeedsFleet(flag, commandID string) bool {
	if flag == "org" && strings.HasPrefix(commandID, "deps ") {
		return true
	}
	switch flag {
	case "filter":
		switch commandID {
		case "ci audit", "hooks install", "hooks check", "hooks repair", "coverage", "verify", "check", "status",
			"fleet", "fleet overview", "fleet stats", "fleet status":
			return true
		}
	case "projects-root":
		switch commandID {
		case "ci audit", "coverage", "verify", "check", "status",
			"fleet", "fleet overview", "fleet stats", "fleet status":
			return true
		}
	}
	return false
}

func persistentCommandSelectedFleet(cmd *cobra.Command, commandID string, args []string) bool {
	switch commandID {
	case "status":
		return len(args) == 0
	case "fleet", "fleet overview", "fleet stats", "fleet status":
		return true
	case "repo status":
		return false
	}
	fleet := cmd.Flags().Lookup("fleet")
	return fleet != nil && fleet.Value.String() == "true"
}

func persistentCommandID(cmd *cobra.Command) string {
	var parts []string
	for current := cmd; current != nil && current.Parent() != nil; current = current.Parent() {
		parts = append([]string{current.Name()}, parts...)
	}
	return strings.Join(parts, " ")
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == sessionlaunch.PrivateLauncherArgument {
		os.Exit(sessionlaunch.RunPrivateLauncher(os.Args[2:]))
	}
	installSessionResolver()
	if err := propagateRuntimeWBExecutable(os.LookupEnv, os.Executable, os.Setenv); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "wb: establish runtime executable for child Git hooks:", err)
		os.Exit(exitFindings)
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
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// propagateRuntimeWBExecutable gives Git hooks started by this WB process a
// transient, invocation-scoped route back to the same executable. Managed hook
// files deliberately contain no installer path: a caller-provided override is
// preserved, while a normal CLI invocation supplies its current executable to
// child Git processes through the inherited environment.
func propagateRuntimeWBExecutable(
	lookupEnv func(string) (string, bool),
	executable func() (string, error),
	setEnv func(string, string) error,
) error {
	if _, configured := lookupEnv("WB_EXECUTABLE"); configured {
		return nil
	}
	path, err := executable()
	if err != nil {
		return fmt.Errorf("locate current executable: %w", err)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("current executable path is not absolute: %q", path)
	}
	if err := setEnv("WB_EXECUTABLE", filepath.Clean(path)); err != nil {
		return fmt.Errorf("export WB_EXECUTABLE: %w", err)
	}
	return nil
}

// run executes the CLI and maps the outcome onto a documented exit code. It is
// separate from main so tests can drive the whole command surface without
// spawning a process or terminating the test binary.
func run(args []string, stdout, stderr io.Writer) int {
	return runWithStdin(args, nil, stdout, stderr)
}

// runWithStdin behaves exactly like run but additionally wires stdin, so a
// test can drive a command that reads from it (worktree create
// --original-prompt-file -) without touching the real process's standard
// input. A nil stdin falls back to the real os.Stdin, matching run's
// production behavior exactly.
func runWithStdin(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// Handled before Execute so `wb --version` answers even when a later flag
	// is invalid, and so it never depends on subcommand wiring.
	if hasVersionFlag(args) {
		return printBareVersion(stdout)
	}

	commandStarted = false
	// Keep the in-process test/embedding runner on the same admission and
	// attribution path as the production main entrypoint. The resolver is
	// read-only until a command explicitly mutates state.
	installSessionResolver()
	root := newRootCmd()
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if terminalPresentationDisabled(args) {
		root.SetOut(ansiStrippingWriter{Writer: stdout})
	}

	prepareHelpPresentation(root, args)
	err := executeWithFang(root)
	// Always on stderr, and always after the command's own output, so a
	// --format json or yaml document on stdout stays machine-parseable.
	reportUndeclaredOwners(stderr)
	if err == nil {
		return exitOK
	}
	_, _ = fmt.Fprintln(stderr, "error:", err)
	code := exitCodeFor(err, commandStarted)
	if code == exitUsage {
		_, _ = fmt.Fprintln(stderr, usageRecoveryHint(root, args))
	}
	return code
}

type ansiStrippingWriter struct {
	io.Writer
}

func (writer ansiStrippingWriter) Write(data []byte) (int, error) {
	if _, err := io.WriteString(writer.Writer, ansi.Strip(string(data))); err != nil {
		return 0, err
	}
	return len(data), nil
}

func terminalPresentationDisabled(args []string) bool {
	if console.Disabled() {
		return true
	}
	for _, arg := range args {
		if arg == "--non-interactive" || arg == "--non-interactive=true" || arg == "--non-interactive=1" {
			return true
		}
	}
	return false
}

func executeWithFang(root *cobra.Command) error {
	return fang.Execute(
		context.Background(),
		root,
		fang.WithoutVersion(),
		fang.WithoutManpage(),
		fang.WithErrorHandler(func(io.Writer, fang.Styles, error) {}),
	)
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

// reportUndeclaredOwners tells an agent how to identify itself, once per
// worktree this invocation wrote to without a declared owner. It is the only
// way WB can learn who is working where: WB is short-lived, so its own process
// id is dead moments after it writes, and only the driving session knows its
// identity.
func reportUndeclaredOwners(stderr io.Writer) {
	for _, worktree := range worktrees.TakeOwnerWarnings() {
		_, _ = fmt.Fprintln(stderr, worktrees.UndeclaredOwnerWarning(worktree))
	}
}
