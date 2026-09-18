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
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/wbhome"
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

// defaultProjectsRoot is the root a command uses when --projects-root is not
// given: WB_PROJECTS_ROOT when set, else ~/projects. It is the only place the
// environment is consulted, so the flag always wins over it.
func defaultProjectsRoot() string {
	if override := strings.TrimSpace(os.Getenv(wbhome.EnvOverride)); override != "" {
		return override
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "projects")
}

const rootLongHelp = `Workbench CLI — fleet-wide operations across your GitHub repositories.

The fastest agent workflow is:

  Start isolated work   wb create <task> <owner/repository>...
  Inspect progress      wb worktree summary <task>
  Open fleet dashboard  wb dashboard
  Land and clean up     wb land <worktree>...
  Land an open PR       wb pr land <owner/repo#n>

wb create/wb land are root-level aliases for wb worktree create/wb worktree
land, built from the same code so they never drift apart. wb land takes
worktrees of ONE repository only per call — it refuses worktrees from more
than one repository — so a task spanning several repositories still needs
one wb land call per repository, not one call for the whole task.

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
			// WB_HOME no longer selects anything. Report the ignored value
			// before any work starts, on stderr, without touching the exit
			// code — a retired variable must not become a rejected command.
			warnIgnoredWBHome(cmd)
			commandStarted = true
			id := persistentCommandID(cmd)
			// `wb version` (including --json) MUST stay side-effect-free
			// (cli-install#req:version-json-side-effect-free): any fleet CLI's
			// install/upgrade status probe execs it from inside the caller's own
			// cwd, which may itself be a WB worktree. Recording a heartbeat or
			// invoked-command there on every probe would make a lane a prober
			// merely glanced at look busy, and would misattribute whatever that
			// worktree's write path records next. Skip both for "version" alone
			// — every other command still gets them.
			if id == "version" {
				return nil
			}
			// Publish which command is running so anything it writes into a
			// worktree records what touched it, without each call site having
			// to thread the name through.
			worktrees.SetInvokedCommand(id)
			// A lane working in a worktree is using it, whatever it happens to
			// be running. Recording that here — once, from the working
			// directory the command was run in — is what lets WB tell a
			// checkout someone is sitting in from one nobody has touched for a
			// day, without every verb having to remember to say so. It is
			// deliberately keyed to the current directory: a fleet-wide sweep
			// run from somewhere else must not make every lane look busy.
			worktrees.TouchHeartbeatForCurrentDirectory(id)
			return nil
		},
	}
	root.PersistentFlags().StringVar(&projectsRoot, "projects-root", defaultProjectsRoot(), "root dir containing {host}/{org}/{repo} clones")
	root.PersistentFlags().StringVar(&filterFlag, "filter", "", "only repos whose org/name contains this substring")
	root.PersistentFlags().StringArrayVar(&extraOrgs, "org", nil, "additional GitHub owner to query (repeatable)")
	root.PersistentFlags().BoolVar(&nonInteractive, "non-interactive", false, "never use a terminal UI or wait for input, even on a terminal")

	// --version is what people and agents reach for first; `wb version` carries
	// the same information and adds --json for programmatic use.
	root.Flags().BoolP("version", "v", false, "print the wb version and exit")

	configureRootHelp(root)
	root.AddCommand(
		groupedRootCommand(newWorktreeCmd(), rootGroupAgent),
		groupedRootCommand(newCreateCmd(), rootGroupAgent),
		groupedRootCommand(newLandCmd(), rootGroupAgent),
		groupedRootCommand(newPRCmd(), rootGroupAgent),
		groupedRootCommand(newBranchCmd(), rootGroupAgent),
		groupedRootCommand(newSessionCmd(), rootGroupAgent),
		groupedRootCommand(newAgentCmd(), rootGroupAgent),
		groupedRootCommand(newTaskCmd(), rootGroupAgent),
		groupedRootCommand(newStreamCmd(), rootGroupChange),
		groupedRootCommand(newStatusCmd(), rootGroupFleet),
		groupedRootCommand(newFleetCmd(), rootGroupFleet),
		groupedRootCommand(newSyncCmd(), rootGroupFleet),
		groupedRootCommand(newSyncReportCmd(), rootGroupFleet),
		groupedRootCommand(newRepoCmd(), rootGroupFleet),
		groupedRootCommand(newCoverageCmd(), rootGroupQuality),
		groupedRootCommand(newVerifyCmd(), rootGroupQuality),
		groupedRootCommand(newCheckCmd(), rootGroupQuality),
		groupedRootCommand(newDeadcodeCmd(), rootGroupQuality),
		groupedRootCommand(newDiskCmd(), rootGroupMaintain),
		groupedRootCommand(newCICmd(), rootGroupQuality),
		groupedRootCommand(newWaitCmd(), rootGroupAgent),
		groupedRootCommand(newHooksCmd(), rootGroupQuality),
		groupedRootCommand(newDepsCmd(), rootGroupChange),
		groupedRootCommand(newMigrateCmd(), rootGroupChange),
		groupedRootCommand(newRunCmd(), rootGroupChange),
		groupedRootCommand(newWorkerCmd(defaultDaemonDependencies()), rootGroupMaintain),
		groupedRootCommand(newDashboardCmd(), rootGroupFleet),
		groupedRootCommand(newDaemonCmd(), rootGroupMaintain),
		groupedRootCommand(newRemoteCmd(), rootGroupMaintain),
		groupedRootCommand(newLayoutCmd(), rootGroupMaintain),
		groupedRootCommand(newArchiveCmd(), rootGroupMaintain),
		groupedRootCommand(newSelfUpdateCmd(), rootGroupLearn),
		groupedRootCommand(newInstallCmd(), rootGroupLearn),
		groupedRootCommand(newUpgradeCmd(), rootGroupLearn),
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
		"sync-report publish": true,
		"dashboard":           true,
		"daemon serve":        true, "daemon start": true, "daemon status": true, "daemon stop": true, "daemon restart": true, "daemon recover": true,
		"daemon operation submit": true, "daemon operation get": true, "daemon operation wait": true, "daemon operation cancel": true,
		// The verb-first spellings run the same implementations, so they take
		// the same persistent flags. Keeping them beside their originals makes
		// a divergence obvious.
		"wait operation": true, "wait agent": true,
		"worker connect": true,
		"deps graph":     true, "deps set": true, "deps bump": true, "deps publish npm": true, "deps drift": true,
		"deps propagate local": true,
		"ci audit":             true,
		"hooks install":        true, "hooks check": true, "hooks repair": true, "hooks run": true,
		"hooks measure":            true,
		"hooks lifecycle backfill": true,
		"hooks agent pre-tool-use": true, "hooks agent install": true,
		"coverage": true, "verify": true, "check": true, "status": true, "disk": true,
		"verify receipt": true, "repo transfer cleanup": true,
		"fleet": true, "fleet overview": true, "fleet stats": true, "fleet status": true, "fleet merge-policy": true, "remote publish": true,
		"remote status": true, "remote machines": true, "remote enroll": true,
		"remote claim": true, "remote release": true, "remote claims": true,
		"layout audit": true, "layout clean": true, "layout migrate": true, "archive clean": true,
		"worktree abort": true, "worktree create": true, "create": true, "worktree guard": true, "worktree marker": true, "worktree rescue": true,
		"worktree active": true, "worktree list": true, "worktree cleanup": true, "worktree gc": true, "worktree relocate": true, "worktree rename": true,
		"worktree land": true, "land": true,
		"pr land": true, "pr create": true,
		"worktree merge": true, "worktree merge prepare": true, "worktree merge land": true, "worktree merge resume": true, "worktree merge revert": true, "worktree merge acknowledge-landed-failed": true, "worktree merge acknowledge-stranded-landing": true, "worktree merge acknowledge-absorbed-conflict": true, "worktree merge seal-validation-failed": true, "worktree merge supersede-validation-failed": true, "worktree merge prepare-conflict-replacement": true,
		"worktree orphans": true, "worktree backfill": true, "worktree log": true, "worktree info": true,
		"worktree own": true,
		"stream start": true, "stream join": true, "stream status": true, "stream end": true, "stream delete": true, "stream sync": true,
		"session register": true, "session list": true, "session prune": true, "session move": true, "session receive": true, "session receive-park": true, "session park": true, "session resume": true,
		"agent dispatch": true, "agent status": true, "agent await": true, "agent list": true, "agent logs": true, "agent stop": true,
		"session send": true, "session recall": true, "session receive-message": true,
		"task offload": true, "task park": true, "task pickup": true,
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
		"ci audit":      true,
		"hooks install": true, "hooks check": true, "hooks repair": true,
		"hooks lifecycle backfill": true,
		"coverage":                 true, "verify": true, "check": true, "status": true,
		"fleet": true, "fleet overview": true, "fleet stats": true, "fleet status": true, "fleet merge-policy": true, "remote publish": true,
		"worktree active": true, "worktree list": true, "worktree cleanup": true, "worktree gc": true, "worktree relocate": true, "worktree rename": true,
		"worktree summary": true, "worktree abort": true, "worktree marker": true, "worktree rescue": true,
		"branch list": true, "branch cleanup": true,
		"archive clean": true,
	},
	"org": {"sync": true, "run": true, "deps graph": true, "deps set": true, "deps bump": true, "deps publish npm": true, "deps drift": true, "fleet prs": true, "fleet merge-policy": true},
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
			"fleet", "fleet overview", "fleet stats", "fleet status", "fleet merge-policy":
			return true
		}
	case "projects-root":
		switch commandID {
		case "ci audit", "coverage", "verify", "check", "status",
			"fleet", "fleet overview", "fleet stats", "fleet status", "fleet merge-policy":
			return true
		}
	}
	return false
}

func persistentCommandSelectedFleet(cmd *cobra.Command, commandID string, args []string) bool {
	switch commandID {
	case "status":
		return len(args) == 0
	case "fleet", "fleet overview", "fleet stats", "fleet status", "fleet merge-policy":
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

// main is deliberately only the process exit edge: every decision it used to
// make lives in dispatch, so the hidden protocol entry points and the runtime
// executable handoff are reachable from an in-process test instead of only
// from a subprocess that no coverage profile can observe.
func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// processHandlers are the pre-cobra entry points dispatch routes to. They are
// function values rather than direct calls because the six secure Git helpers
// deliberately operate on inherited file descriptors 3..9: invoking one
// in-process against the test binary's own descriptors corrupts the Go
// runtime, so the routing can only be asserted by substituting the handler.
type processHandlers struct {
	privateLauncher func(args []string) int
	ownerCLI        func(args []string, deps agents.OwnerDeps) int
	agentRemote     func(stdin io.Reader, stdout, stderr io.Writer) int
	// secureGitHelper resolves one hidden argv value to its helper. The second
	// result reports whether the argument named a helper at all; an unknown
	// value falls through to cobra, exactly as the previous if-chain did.
	secureGitHelper func(argument string, args []string) (code int, known bool)
	lookupEnv       func(string) (string, bool)
	executable      func() (string, error)
	setEnv          func(string, string) error
}

// secureGitHelpers maps every hidden argv value that must be resolved before
// cobra to its handler. It is a function rather than a package-level map so the
// table is rebuilt per process, and so a test can assert its membership without
// invoking a helper that expects inherited descriptors 3..9.
func secureGitHelpers() map[string]func([]string) int {
	return map[string]func([]string) int{
		worktrees.SecureCleanupGitHelperArgument:        worktrees.RunSecureCleanupGitHelper,
		hooks.SecureHooksGitHelperArgument:              hooks.RunSecureHooksGitHelper,
		worktrees.SecureStageGitHelperArgument:          worktrees.RunSecureStageGitHelper,
		worktrees.SecureCanonicalGitHelperArgument:      worktrees.RunSecureCanonicalGitHelper,
		worktrees.SecureStageCanonicalGitHelperArgument: worktrees.RunSecureStageCanonicalGitHelper,
		worktrees.SecureRenameGitHelperArgument:         worktrees.RunSecureRenameGitHelper,
	}
}

func defaultProcessHandlers() processHandlers {
	helpers := secureGitHelpers()
	return processHandlers{
		privateLauncher: sessionlaunch.RunPrivateLauncher,
		ownerCLI:        agents.OwnerCLI,
		agentRemote:     RunAgentRemote,
		secureGitHelper: func(argument string, args []string) (int, bool) {
			helper, known := helpers[argument]
			if !known {
				return 0, false
			}
			return helper(args), true
		},
		lookupEnv:  os.LookupEnv,
		executable: os.Executable,
		setEnv:     os.Setenv,
	}
}

func dispatch(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return dispatchWithHandlers(defaultProcessHandlers(), args, stdin, stdout, stderr)
}

// dispatchWithHandlers resolves everything that must be decided before cobra
// parses anything. The hidden protocol arguments are matched as an exact argv
// value, so a remote caller can never reach a flag parser and have its request
// reinterpreted as shell text, and the runtime-executable handoff must happen
// before any child Git hook is spawned. It returns the documented exit code
// rather than exiting, which is what keeps this routing testable in-process.
func dispatchWithHandlers(handlers processHandlers, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// Both the first token and the argument tail are derived once, from a
	// length-checked slice: `wb` with no arguments at all is a valid invocation
	// that must reach cobra, not a slice-bounds panic.
	var first string
	var rest []string
	if len(args) > 0 {
		first = args[0]
		rest = args[1:]
	}
	switch first {
	case sessionlaunch.PrivateLauncherArgument:
		return handlers.privateLauncher(rest)
	case agents.OwnerArgument:
		return handlers.ownerCLI(rest, agents.DefaultOwnerDeps())
	case agents.RemoteArgument:
		// The private remote entry point is a validated protocol value on stdin,
		// not a command line: it is handled here so a remote caller can never
		// reach a flag parser, and so its request cannot be reinterpreted as
		// shell text.
		return handlers.agentRemote(stdin, stdout, stderr)
	}
	installSessionResolver()
	if err := propagateRuntimeWBExecutable(handlers.lookupEnv, handlers.executable, handlers.setEnv); err != nil {
		_, _ = fmt.Fprintln(stderr, "wb: establish runtime executable for child Git hooks:", err)
		return exitFindings
	}
	if code, known := handlers.secureGitHelper(first, rest); known {
		return code
	}
	return run(args, stdout, stderr)
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
