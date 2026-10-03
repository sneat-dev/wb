// Package cmdstream constructs all stream verbs with isolated operation bindings.
package cmdstream

import (
	"fmt"
	"strings"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/streamrun"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/spf13/cobra"
)

// streamEnvelope is the machine-readable result shape every stream verb emits
// under --format json.
//
// LANE SEAM: `verbs-share-an-exit-code-and-envelope-contract` gives every WB
// verb one envelope, delivered with the exit-code contract row of the P0 plan.
// The stream verbs emit that shape now through this local type so their JSON
// is stable from the first release; adopting the shared implementation is a
// type swap, not an output change.
type streamEnvelope struct {
	Version     int    `json:"v"`
	Verb        string `json:"verb"`
	Outcome     string `json:"outcome"`
	RefusalCode string `json:"refusal_code,omitempty"`
	// SanctionedCommand is the first command that satisfies the guard — a
	// single runnable string, as the spec's envelope field is singular.
	SanctionedCommand string `json:"sanctioned_command,omitempty"`
	// SanctionedCommands carries every alternative, since more than one
	// command can satisfy a guard and joining them into one string produces a
	// value that is not runnable.
	SanctionedCommands []string `json:"sanctioned_commands,omitempty"`
	Evidence           any      `json:"evidence,omitempty"`
}

const (
	outcomeSuccess  = "success"
	outcomeFindings = "findings"
	outcomeRefused  = "refused"
)

func New(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	command := &cobra.Command{
		Use:     "stream",
		Aliases: []string{"streams"},
		Short:   "Run one named cross-repository unit of work across a library and its consumers",
		Long: `A stream is one named unit of work spanning a library and the consumers that
must change with it.

Each member gets one worktree on branch stream/<name> with a DRAFT pull request
to its base, so CI runs on every push and the stream's true state is always
visible. Agents branch from stream/<name> into their own worktrees and open
pull requests against the stream branch, never against main.

Consume the library through 'wb deps propagate local'; the orchestrator runs
'wb deps propagate remote' at the end. End with 'wb stream end'.

Stream membership, roles, leases and live links live in WB-owned state beside
the Work Log — never inside a member repository, so 'git status' in every
member stays clean and the stream survives an interrupted session.

A repository carries at most one open stream at a time. The refusal names
'wb stream join', which is the sanctioned way to add a repository to the stream
that already holds it.

Exit codes follow the WB contract: 0 success, 1 findings (the verb ran and
reported something that needs attention — a red base, a missing CI concurrency
group), 2 refusal or usage error. A findings exit does not mean the stream was
not created; read the report or the JSON envelope.`,
	}
	command.AddCommand(
		newStreamStartCmd(runtime, deps),
		newStreamJoinCmd(runtime, deps),
		newStreamSyncCmd(runtime, deps),
		newStreamStatusCmd(runtime, deps),
		newStreamEndCmd(runtime, deps),
		newStreamDeleteCmd(runtime, deps),
	)
	setDiscoveryTerms(command, "stream library consumer cross-repository propagate link draft pull request lease")
	return command
}

// streamEngineOptions carries the flags every stream verb shares.
type streamEngineOptions struct {
	format string
}

func newStreamStartCmd(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var (
		options                                                                                 streamEngineOptions
		library, base                                                                           string
		mode                                                                                    string
		effortID, runID, initiator, agentID, agentRuntime, model, cli, provider, originalPrompt string
	)
	command := &cobra.Command{
		Use:   "start <name> <owner/repository>...",
		Short: "Create one stream worktree and draft pull request per repository",
		Long: `Create a stream: one worktree per repository on branch stream/<name>, each with
a DRAFT pull request open to its base.

The name is the worktree task name, so a stream introduces no second identity
for the same work, and every checkout is created by the existing
'wb worktree create' path with its branch policy, prompt archival and
fleet-wide claim.

The first repository is the library — the one whose published artifacts the
others resolve — unless --library names another. Propagation direction is not
symmetric, so the role is recorded rather than re-derived by later verbs.

Before anything is created, start proves the fleet is ready per member:
'wb hooks check', an npm provider-identity scan, a red-base check, and a check
that each member's pull-request workflow carries a concurrency group keyed to
the ref with cancel-in-progress: true.

Refusals (exit 2):
  stream-exists          the name is taken — 'wb stream status <name>'
  repository-in-stream   the repository already carries an open stream —
                         'wb stream join <holder> <owner/repository>'
  preflight-failed       hooks or npm provider identity — 'wb hooks repair'

Findings (exit 1): the stream was created, and a member was reported rather
than refused — a red base, a missing CI concurrency group, or a check WB could
not establish. Nothing is ever silently passed.`,
		Example: `# Start a stream over a library and two consumers
printf '%s\n' 'the exact task request' | \
  wb stream start checkout-rewrite acme/library acme/app acme/site \
  --mode manual --initiator me@example.com --model unknown \
  --original-prompt-file -

# Name the library explicitly when it is not the first repository
wb stream start checkout-rewrite acme/app acme/library --library acme/library \
  --original-prompt-file ./prompt.txt --mode manual --initiator me@example.com --model unknown`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(options.format, "text", "json"); err != nil {
				return err
			}
			// The name is validated before the Work Log reserves anything, so
			// an unusable stream name is refused with the stream's own rule
			// rather than with a Work Log path-segment error.
			if err := streams.ValidateName(args[0]); err != nil {
				return streamUsage(runtime, command, "stream start", options.format, err.Error())
			}
			workLog, sessionMode, err := streamWorkLog(runtime, deps, command, args[0], workLogFlags{
				mode: mode, effortID: effortID, runID: runID, initiator: initiator,
				agentID: agentID, agentRuntime: agentRuntime, model: model,
				cli: cli, provider: provider, originalPrompt: originalPrompt,
			})
			if err != nil {
				return streamUsage(runtime, command, "stream start", options.format, err.Error())
			}
			result, err := deps.Start(command.Context(), streamrun.Creation{ProjectsRoot: runtime.Flags().ProjectsRoot, WorkLog: workLog, SessionRequired: sessionMode, Base: base}, streams.StartOptions{
				Name: args[0], Repositories: args[1:], Library: library, Base: base,
			})
			if err != nil {
				return streamFailure(runtime, command, "stream start", options.format, err)
			}
			return streamStartOutput(runtime, command, "stream start", options.format, result)
		},
	}
	command.Flags().StringVar(&options.format, "format", "text", "stdout format: text or json")
	command.Flags().StringVar(&library, "library", "", "the member whose published artifacts the others resolve (default: the first repository)")
	command.Flags().StringVar(&base, "base", "", "branch the draft pull requests target (default: each repository's own default branch)")
	addStreamWorkLogFlags(command, &mode, &effortID, &runID, &initiator, &agentID, &agentRuntime, &model, &cli, &provider, &originalPrompt)
	setDiscoveryTerms(command, "stream start create begin cross-repository library consumer draft pull request worktree")
	return command
}

func newStreamJoinCmd(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var (
		options                                                                                 streamEngineOptions
		role, base                                                                              string
		mode                                                                                    string
		effortID, runID, initiator, agentID, agentRuntime, model, cli, provider, originalPrompt string
	)
	command := &cobra.Command{
		Use:   "join <name> <owner/repository>",
		Short: "Add a repository to an existing stream",
		Long: `Add a repository to an existing stream, creating its stream worktree,
stream/<name> branch and draft pull request exactly as 'wb stream start' does,
and recording it so status, propagate and end treat it as a member from that
point on.

Join is the sanctioned answer to the one-stream-per-repository refusal: two
concurrent streams on one repository are out of scope, because landing one
rewrites the base under the other and every already-approved agent branch would
need re-rebasing.

Joining a repository that is already a member reconciles a missing draft pull
request and repairs WB's exact legacy-generated pull-request title. Any
user-authored title is preserved; otherwise the join is a no-op.

Re-running join for a member whose draft pull request never opened retries
exactly that effect, rather than no-opping: it is the recovery path a failed
publication leaves behind.

Refusals (exit 2):
  repository-in-stream   the repository is a member of a different open stream
  library-exists         the stream already has a library — join as a consumer
  stream-ended           the stream has ended — start a new one
  usage                  an ambiguous invocation (an unknown --role, a bad name)`,
		Example: `wb stream join checkout-rewrite acme/reports \
  --original-prompt-file ./prompt.txt --mode manual --initiator me@example.com --model unknown`,
		Args: cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(options.format, "text", "json"); err != nil {
				return err
			}
			if err := streams.ValidateName(args[0]); err != nil {
				return streamUsage(runtime, command, "stream join", options.format, err.Error())
			}
			memberRole := streams.RoleConsumer
			switch role {
			case "", "consumer":
			case "library":
				memberRole = streams.RoleLibrary
			default:
				return streamUsage(runtime, command, "stream join", options.format,
					fmt.Sprintf("unsupported role %q; use library or consumer", role),
					"wb stream join "+args[0]+" "+args[1]+" --role consumer")
			}
			workLog, sessionMode, err := streamWorkLog(runtime, deps, command, args[0], workLogFlags{
				mode: mode, effortID: effortID, runID: runID, initiator: initiator,
				agentID: agentID, agentRuntime: agentRuntime, model: model,
				cli: cli, provider: provider, originalPrompt: originalPrompt,
			})
			if err != nil {
				return streamUsage(runtime, command, "stream join", options.format, err.Error())
			}
			result, err := deps.Join(command.Context(), streamrun.Creation{ProjectsRoot: runtime.Flags().ProjectsRoot, WorkLog: workLog, SessionRequired: sessionMode, Base: base}, streams.JoinOptions{
				Name: args[0], Repository: args[1], Role: memberRole, Base: base,
			})
			if err != nil {
				return streamFailure(runtime, command, "stream join", options.format, err)
			}
			return streamStartOutput(runtime, command, "stream join", options.format, result)
		},
	}
	command.Flags().StringVar(&options.format, "format", "text", "stdout format: text or json")
	command.Flags().StringVar(&role, "role", "consumer", "member role: consumer or library")
	command.Flags().StringVar(&base, "base", "", "branch the draft pull request targets (default: the repository's own default branch)")
	addStreamWorkLogFlags(command, &mode, &effortID, &runID, &initiator, &agentID, &agentRuntime, &model, &cli, &provider, &originalPrompt)
	setDiscoveryTerms(command, "stream join add member repository consumer library second stream refusal")
	return command
}

func newStreamStatusCmd(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var options streamEngineOptions
	command := &cobra.Command{
		Use:   "status [name]",
		Short: "Report a stream's three gaps, its members, and its open agent pull requests",
		Long: `Report the three states in which a stream is incomplete, separately and named
per repository:

  1. consumers holding a live local link, and the library worktree each links to
  2. library changes merged into the base but not yet tagged or published
  3. consumers still declaring a version older than the library's newest tag

It also reports every open pull request targeting a stream branch — the ones
GitHub would silently retarget at the base if the branch were deleted — and
collapses patch-identical unabsorbed commits so N branches carrying one body of
work read as one cluster.

Everything is reconstructed from WB-owned stream state, so status answers after
an interrupted session. Anything WB could not establish is reported under
"could not establish": an empty gap list is never readable as "nothing is
wrong".

With no name, every stream in the store is listed.`,
		Example: `wb stream status checkout-rewrite
wb stream status checkout-rewrite --format json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(options.format, "text", "json"); err != nil {
				return err
			}
			if len(args) == 0 {
				all, unreadable, err := deps.List(command.Context(), runtime.Flags().ProjectsRoot)
				if err != nil {
					return err
				}
				return streamListOutput(command, options.format, all, unreadable)
			}
			status, err := deps.Status(command.Context(), runtime.Flags().ProjectsRoot, args[0])
			if err != nil {
				return streamFailure(runtime, command, "stream status", options.format, err)
			}
			return streamStatusOutput(runtime, command, options.format, status)
		},
	}
	command.Flags().StringVar(&options.format, "format", "text", "stdout format: text or json")
	setDiscoveryTerms(command, "stream status report gaps linked untagged behind consumers agent pull requests")
	return command
}

func newStreamEndCmd(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var (
		options          streamEngineOptions
		apply            bool
		retarget         bool
		forceUnabsorbed  bool
		reason           string
		keepRemoteBranch bool
	)
	command := &cobra.Command{
		Use:   "end <name>",
		Short: "Retire a stream's worktrees, leases and scaffolding without publishing anything",
		Long: `End a stream: close or retarget every still-open pull request against its
stream branches, close each member's own draft pull request, release the stream
leases, and retire every stream worktree through the existing
'wb worktree cleanup' path.

Ending publishes, bumps and merges nothing.

Without --apply the verb reports exactly what it would do and changes nothing,
so an operator sees which pull requests would be closed before any of them are.

Refusals (exit 2):
  live-link          a consumer still resolves an unpublished working tree —
                     the refusal names the exact 'wb deps propagate local
                     ... --undo' per link
  unabsorbed-work    a member's stream branch carries commits the base has not
                     absorbed, named at the content level by patch identity —
                     or the absorption check could not run at all, which
                     refuses too: a check that cannot answer must not pass.
                     --force-unabsorbed --reason "<why>" steps over it and
                     records both in the event log

Still-open pull requests against the stream branch are closed by default. GitHub
auto-retargets such a pull request onto the base when its base branch is
deleted, which silently converts leftover agent work into a pull request against
main; --retarget makes that move deliberate instead.

After the pull requests are settled, end deletes origin/stream/<name> as well as
the local checkout, because leaving the remote branch is scaffolding the verb
claims to remove. --keep-remote-branch leaves it in place.

A stream interrupted while it was being created is retired the same way: its
record carries every member's intended coordinates from before the first side
effect, so end can reach worktrees, branches and pull requests a crash left
behind.`,
		Example: `# See what ending would do
wb stream end checkout-rewrite

# Retire it
wb stream end checkout-rewrite --apply`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(options.format, "text", "json"); err != nil {
				return err
			}
			result, err := deps.End(command.Context(), runtime.Flags().ProjectsRoot, streams.EndOptions{
				Name: args[0], Apply: apply, Retarget: retarget,
				ForceUnabsorbed: forceUnabsorbed, Reason: reason,
				KeepRemoteBranch: keepRemoteBranch,
			})
			if err != nil {
				return streamFailure(runtime, command, "stream end", options.format, err)
			}
			return streamEndOutput(runtime, command, options.format, result)
		},
	}
	command.Flags().StringVar(&options.format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&apply, "apply", false, "perform the retirement; without it nothing is changed")
	command.Flags().BoolVar(&retarget, "retarget", false, "retarget still-open agent pull requests onto the base instead of closing them")
	command.Flags().BoolVar(&forceUnabsorbed, "force-unabsorbed", false, "proceed past the absorption guard; requires --reason, and both are recorded in the event log")
	command.Flags().StringVar(&reason, "reason", "", "why the absorption guard is being stepped over (required by --force-unabsorbed)")
	command.Flags().BoolVar(&keepRemoteBranch, "keep-remote-branch", false, "leave origin/stream/<name> in place instead of deleting it")
	setDiscoveryTerms(command, "stream end finish retire cleanup close draft pull request lease worktree remote branch")
	return command
}

func newStreamDeleteCmd(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var options streamEngineOptions
	command := &cobra.Command{
		Use:   "delete <name>",
		Short: "Remove an ended stream's record and event log",
		Long: `Delete an ended stream's record and its event log.

It refuses an OPEN stream: deleting one would strand its worktrees, branches and
pull requests with no record any verb could reach. End it first.

Deleting is rarely necessary. 'wb stream start' on the name of an ended stream
archives the old record as '<name>.ended-<timestamp>' and proceeds, so a name is
never burned by its first use. Use delete when the archived record itself is no
longer wanted.`,
		Example: `wb stream delete checkout-rewrite.ended-20260903T101500Z`,
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(options.format, "text", "json"); err != nil {
				return err
			}
			if err := deps.Delete(runtime.Flags().ProjectsRoot, args[0]); err != nil {
				if strings.Contains(err.Error(), "still open") {
					return streamUsage(runtime, command, "stream delete", options.format, err.Error(),
						"wb stream end "+args[0]+" --apply")
				}
				return streamFailure(runtime, command, "stream delete", options.format, err)
			}
			if options.format == "json" {
				return writeStreamJSON(command.OutOrStdout(), streamEnvelope{
					Version: 1, Verb: "stream delete", Outcome: outcomeSuccess,
					Evidence: map[string]string{"stream": args[0]},
				})
			}
			_, err := fmt.Fprintf(command.OutOrStdout(), "deleted stream %s\n", args[0])
			return err
		},
	}
	command.Flags().StringVar(&options.format, "format", "text", "stdout format: text or json")
	setDiscoveryTerms(command, "stream delete remove purge ended archived record event log")
	return command
}

func setDiscoveryTerms(command *cobra.Command, terms string) {
	command.Annotations = map[string]string{"wb.dev/discovery-terms": terms}
}
