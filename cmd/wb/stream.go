package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktrees"
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

func newStreamCmd() *cobra.Command {
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
		newStreamStartCmd(),
		newStreamJoinCmd(),
		newStreamSyncCmd(),
		newStreamAbsorbCmd(),
		newStreamStatusCmd(),
		newStreamEndCmd(),
		newStreamDeleteCmd(),
	)
	setDiscoveryTerms(command, "stream library consumer cross-repository propagate link draft pull request lease")
	return command
}

// streamEngineOptions carries the flags every stream verb shares.
type streamEngineOptions struct {
	format string
}

func newStreamEngine(command *cobra.Command, workLog worktrees.WorkLogOptions, sessionMode bool, base string) (*streams.Engine, error) {
	store, err := streams.Open(projectsRoot)
	if err != nil {
		return nil, err
	}
	login, machine := streamLeaseIdentity(defaultRemoteDeps(), projectsRoot)
	return &streams.Engine{
		Store:        store,
		Git:          streams.ExecGit{},
		GitHub:       streams.ExecGitHub{},
		Worktrees:    &streamWorktrees{projectsRoot: projectsRoot, workLog: workLog, sessionMode: sessionMode, base: base},
		ProjectsRoot: projectsRoot,
		HooksCheck:   streams.InstalledHooksChecker(hookExecutable(), projectsRoot),
		Login:        login,
		Machine:      machine,
		Session:      streamSessionIdentity(),
	}, nil
}

func newStreamStartCmd() *cobra.Command {
	var (
		shared                                                                                  streamEngineOptions
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
			if err := requireOutputFormat(shared.format, "text", "json"); err != nil {
				return err
			}
			// The name is validated before the Work Log reserves anything, so
			// an unusable stream name is refused with the stream's own rule
			// rather than with a Work Log path-segment error.
			if err := streams.ValidateName(args[0]); err != nil {
				return streamUsage(command, "stream start", shared.format, err.Error())
			}
			workLog, sessionMode, err := streamWorkLog(command, args[0], workLogFlags{
				mode: mode, effortID: effortID, runID: runID, initiator: initiator,
				agentID: agentID, agentRuntime: agentRuntime, model: model,
				cli: cli, provider: provider, originalPrompt: originalPrompt,
			})
			if err != nil {
				return streamUsage(command, "stream start", shared.format, err.Error())
			}
			engine, err := newStreamEngine(command, workLog, sessionMode, base)
			if err != nil {
				return err
			}
			repositories := args[1:]
			transitive, proposed, err := proposedTransitiveConsumers(projectsRoot, repositories)
			if err != nil {
				return err
			}
			result, err := engine.Start(command.Context(), streams.StartOptions{
				Name: args[0], Repositories: repositories, Library: library, Base: base,
			}, transitive)
			if err != nil {
				return streamFailure(command, "stream start", shared.format, err)
			}
			if !proposed {
				result.Reported = append(result.Reported, streams.PreflightFinding{
					Check:  "transitive-membership",
					Status: streams.PreflightUnknown,
					Detail: "no dependency graph evidence on this machine; run `wb deps graph --fleet --format json` so a transitive consumer left out of the stream is named",
				})
			}
			return streamStartOutput(command, "stream start", shared.format, result)
		},
	}
	command.Flags().StringVar(&shared.format, "format", "text", "stdout format: text or json")
	command.Flags().StringVar(&library, "library", "", "the member whose published artifacts the others resolve (default: the first repository)")
	command.Flags().StringVar(&base, "base", "", "branch the draft pull requests target (default: each repository's own default branch)")
	addStreamWorkLogFlags(command, &mode, &effortID, &runID, &initiator, &agentID, &agentRuntime, &model, &cli, &provider, &originalPrompt)
	setDiscoveryTerms(command, "stream start create begin cross-repository library consumer draft pull request worktree")
	return command
}

func newStreamJoinCmd() *cobra.Command {
	var (
		shared                                                                                  streamEngineOptions
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

Joining a repository that is already a member is a no-op.

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
			if err := requireOutputFormat(shared.format, "text", "json"); err != nil {
				return err
			}
			if err := streams.ValidateName(args[0]); err != nil {
				return streamUsage(command, "stream join", shared.format, err.Error())
			}
			memberRole := streams.RoleConsumer
			switch role {
			case "", "consumer":
			case "library":
				memberRole = streams.RoleLibrary
			default:
				return streamUsage(command, "stream join", shared.format,
					fmt.Sprintf("unsupported role %q; use library or consumer", role),
					"wb stream join "+args[0]+" "+args[1]+" --role consumer")
			}
			workLog, sessionMode, err := streamWorkLog(command, args[0], workLogFlags{
				mode: mode, effortID: effortID, runID: runID, initiator: initiator,
				agentID: agentID, agentRuntime: agentRuntime, model: model,
				cli: cli, provider: provider, originalPrompt: originalPrompt,
			})
			if err != nil {
				return streamUsage(command, "stream join", shared.format, err.Error())
			}
			engine, err := newStreamEngine(command, workLog, sessionMode, base)
			if err != nil {
				return err
			}
			result, err := engine.Join(command.Context(), streams.JoinOptions{
				Name: args[0], Repository: args[1], Role: memberRole, Base: base,
			})
			if err != nil {
				return streamFailure(command, "stream join", shared.format, err)
			}
			return streamStartOutput(command, "stream join", shared.format, result)
		},
	}
	command.Flags().StringVar(&shared.format, "format", "text", "stdout format: text or json")
	command.Flags().StringVar(&role, "role", "consumer", "member role: consumer or library")
	command.Flags().StringVar(&base, "base", "", "branch the draft pull request targets (default: the repository's own default branch)")
	addStreamWorkLogFlags(command, &mode, &effortID, &runID, &initiator, &agentID, &agentRuntime, &model, &cli, &provider, &originalPrompt)
	setDiscoveryTerms(command, "stream join add member repository consumer library second stream refusal")
	return command
}

func newStreamStatusCmd() *cobra.Command {
	var shared streamEngineOptions
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
			if err := requireOutputFormat(shared.format, "text", "json"); err != nil {
				return err
			}
			engine, err := newStreamEngine(command, worktrees.WorkLogOptions{}, false, "")
			if err != nil {
				return err
			}
			if len(args) == 0 {
				return streamListOutput(command, shared.format, engine)
			}
			status, err := engine.Status(command.Context(), args[0])
			if err != nil {
				return streamFailure(command, "stream status", shared.format, err)
			}
			return streamStatusOutput(command, shared.format, status)
		},
	}
	command.Flags().StringVar(&shared.format, "format", "text", "stdout format: text or json")
	setDiscoveryTerms(command, "stream status report gaps linked untagged behind consumers agent pull requests")
	return command
}

func newStreamEndCmd() *cobra.Command {
	var (
		shared           streamEngineOptions
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
			if err := requireOutputFormat(shared.format, "text", "json"); err != nil {
				return err
			}
			engine, err := newStreamEngine(command, worktrees.WorkLogOptions{}, false, "")
			if err != nil {
				return err
			}
			result, err := engine.End(command.Context(), streams.EndOptions{
				Name: args[0], Apply: apply, Retarget: retarget,
				ForceUnabsorbed: forceUnabsorbed, Reason: reason,
				KeepRemoteBranch: keepRemoteBranch,
			})
			if err != nil {
				return streamFailure(command, "stream end", shared.format, err)
			}
			return streamEndOutput(command, shared.format, result)
		},
	}
	command.Flags().StringVar(&shared.format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&apply, "apply", false, "perform the retirement; without it nothing is changed")
	command.Flags().BoolVar(&retarget, "retarget", false, "retarget still-open agent pull requests onto the base instead of closing them")
	command.Flags().BoolVar(&forceUnabsorbed, "force-unabsorbed", false, "proceed past the absorption guard; requires --reason, and both are recorded in the event log")
	command.Flags().StringVar(&reason, "reason", "", "why the absorption guard is being stepped over (required by --force-unabsorbed)")
	command.Flags().BoolVar(&keepRemoteBranch, "keep-remote-branch", false, "leave origin/stream/<name> in place instead of deleting it")
	setDiscoveryTerms(command, "stream end finish retire cleanup close draft pull request lease worktree remote branch")
	return command
}

func newStreamDeleteCmd() *cobra.Command {
	var shared streamEngineOptions
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
			if err := requireOutputFormat(shared.format, "text", "json"); err != nil {
				return err
			}
			store, err := streams.Open(projectsRoot)
			if err != nil {
				return err
			}
			if err := store.Delete(args[0]); err != nil {
				if strings.Contains(err.Error(), "still open") {
					return streamUsage(command, "stream delete", shared.format, err.Error(),
						"wb stream end "+args[0]+" --apply")
				}
				return streamFailure(command, "stream delete", shared.format, err)
			}
			if shared.format == "json" {
				return writeStreamJSON(command.OutOrStdout(), streamEnvelope{
					Version: 1, Verb: "stream delete", Outcome: outcomeSuccess,
					Evidence: map[string]string{"stream": args[0]},
				})
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "deleted stream %s\n", args[0])
			return err
		},
	}
	command.Flags().StringVar(&shared.format, "format", "text", "stdout format: text or json")
	setDiscoveryTerms(command, "stream delete remove purge ended archived record event log")
	return command
}

// streamFailure renders a refusal or a failure and selects the exit code.
// A guard that fired is exit 2 with its stable code and sanctioned command; a
// failure is exit 1. A caller must be able to tell them apart without parsing
// prose.
func streamFailure(command *cobra.Command, verb, format string, err error) error {
	refusal, refused := streams.Refused(err)
	if format == "json" {
		envelope := streamEnvelope{Version: 1, Verb: verb, Outcome: outcomeFindings}
		if refused {
			envelope.Outcome = outcomeRefused
			envelope.RefusalCode = refusal.Code
			if len(refusal.Sanctioned) > 0 {
				envelope.SanctionedCommand = refusal.Sanctioned[0]
				envelope.SanctionedCommands = refusal.Sanctioned
			}
			envelope.Evidence = map[string]string{"message": streams.RedactString(refusal.Message)}
		} else {
			envelope.Evidence = map[string]string{"message": streams.RedactString(err.Error())}
		}
		if encodeErr := writeStreamJSON(command.OutOrStdout(), envelope); encodeErr != nil {
			return encodeErr
		}
	}
	if refused {
		return &exitError{code: exitUsage, message: refusal.Error()}
	}
	// Every other failure still reaches stderr through cobra, so it is
	// redacted here rather than at the point it is printed.
	return errors.New(streams.RedactString(err.Error()))
}

// streamUsage turns an invocation WB rejected into the same envelope and exit
// code a guard produces. A caller that asked for --format json must never get
// an empty stdout, and an ambiguous invocation must not be reported with the
// code that means "the work is broken".
func streamUsage(command *cobra.Command, verb, format, message string, sanctioned ...string) error {
	return streamFailure(command, verb, format, &streams.Refusal{
		Code: streams.RefusalUsage, Message: message, Sanctioned: sanctioned,
	})
}

func streamStartOutput(command *cobra.Command, verb, format string, result streams.StartResult) error {
	outcome := outcomeSuccess
	if len(result.Reported) > 0 {
		outcome = outcomeFindings
	}
	if format == "json" {
		if err := writeStreamJSON(command.OutOrStdout(), streamEnvelope{
			Version: 1, Verb: verb, Outcome: outcome, Evidence: result,
		}); err != nil {
			return err
		}
	} else {
		out := command.OutOrStdout()
		if _, err := fmt.Fprintf(out, "stream %s on %s\n", result.Stream.Name, streams.Branch(result.Stream.Name)); err != nil {
			return err
		}
		for _, member := range result.Stream.Members {
			pullRequest := fmt.Sprintf("#%d", member.PullRequest)
			if member.PullRequest == 0 {
				pullRequest = "no draft PR: " + member.PullRequestError
			}
			if _, err := fmt.Fprintf(out, "  %-8s %s  %s  %s\n", member.Role, member.Repository, member.Worktree, pullRequest); err != nil {
				return err
			}
		}
		if err := printStreamFindings(out, result.Reported); err != nil {
			return err
		}
		for _, omitted := range result.TransitiveOmissions {
			if _, err := fmt.Fprintf(out, "  ! %s consumes a stream member but is not in the stream; remote propagation bumps only members\n", omitted); err != nil {
				return err
			}
		}
	}
	if outcome == outcomeFindings {
		return &exitError{code: exitFindings, message: "the stream was created and reported findings; see the report above"}
	}
	return nil
}

func printStreamFindings(out io.Writer, findings []streams.PreflightFinding) error {
	for _, finding := range findings {
		repository := finding.Repository
		if repository == "" {
			repository = "(stream)"
		}
		if _, err := fmt.Fprintf(out, "  ! %s %s [%s] %s\n", repository, finding.Check, finding.Status, finding.Detail); err != nil {
			return err
		}
	}
	return nil
}

func streamStatusOutput(command *cobra.Command, format string, status streams.Status) error {
	if format == "json" {
		return writeStreamJSON(command.OutOrStdout(), streamEnvelope{
			Version: 1, Verb: "stream status", Outcome: outcomeSuccess, Evidence: status,
		})
	}
	out := command.OutOrStdout()
	if _, err := fmt.Fprintf(out, "stream %s (%s) on %s\n", status.Stream, status.Phase, status.Branch); err != nil {
		return err
	}
	for _, member := range status.Members {
		if _, err := fmt.Fprintf(out, "  %-8s %-28s unabsorbed=%d links=%d lease=%s\n",
			member.Role, member.Repository, member.Unabsorbed, member.LiveLinks, member.LeaseHolder); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(out, "\nlinked consumers (gap 1):"); err != nil {
		return err
	}
	if len(status.LinkedConsumers) == 0 {
		if _, err := fmt.Fprintln(out, "  none"); err != nil {
			return err
		}
	}
	for _, linked := range status.LinkedConsumers {
		if _, err := fmt.Fprintf(out, "  %s → %s via %s (%s, was %s, content-hash %s)\n",
			linked.Repository, linked.Library, linked.Mechanism, linked.Identity, linked.PreviousVersion, linked.ContentHash); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(out, "\nmerged but untagged (gap 2):"); err != nil {
		return err
	}
	if status.MergedUntagged == nil {
		if _, err := fmt.Fprintln(out, "  none"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(out, "  %s: %d commit(s) on %s after %s\n",
			status.MergedUntagged.Repository, len(status.MergedUntagged.Commits),
			status.MergedUntagged.Base, status.MergedUntagged.LatestTag); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(out, "\nconsumers behind (gap 3):"); err != nil {
		return err
	}
	if len(status.ConsumersBehind) == 0 {
		if _, err := fmt.Fprintln(out, "  none"); err != nil {
			return err
		}
	}
	for _, behind := range status.ConsumersBehind {
		if _, err := fmt.Fprintf(out, "  %s declares %s %s in %s; published is %s\n",
			behind.Repository, behind.Identity, behind.Declared, behind.Manifest, behind.Published); err != nil {
			return err
		}
	}
	if len(status.OpenAgentPullRequests) > 0 {
		if _, err := fmt.Fprintln(out, "\nopen agent pull requests against the stream branch:"); err != nil {
			return err
		}
		for _, pullRequest := range status.OpenAgentPullRequests {
			if _, err := fmt.Fprintf(out, "  %s #%d %s (%s)\n", pullRequest.Repository, pullRequest.Number, pullRequest.Title, pullRequest.Head); err != nil {
				return err
			}
		}
	}
	if len(status.Unknowns) > 0 {
		if _, err := fmt.Fprintln(out, "\ncould not establish:"); err != nil {
			return err
		}
		for _, unknown := range status.Unknowns {
			if _, err := fmt.Fprintf(out, "  ? %s\n", unknown); err != nil {
				return err
			}
		}
	}
	return nil
}

func streamListOutput(command *cobra.Command, format string, engine *streams.Engine) error {
	all, unreadable, err := engine.Store.List()
	if err != nil {
		return err
	}
	if format == "json" {
		return writeStreamJSON(command.OutOrStdout(), streamEnvelope{
			Version: 1, Verb: "stream status", Outcome: outcomeSuccess,
			Evidence: map[string]any{"streams": all, "unreadable": unreadable},
		})
	}
	if len(all) == 0 && len(unreadable) == 0 {
		_, err := fmt.Fprintln(command.OutOrStdout(), "no streams")
		return err
	}
	for _, stream := range all {
		state := string(stream.Lifecycle())
		repositories := make([]string, 0, len(stream.Members))
		for _, member := range stream.Members {
			repositories = append(repositories, member.Repository)
		}
		if _, err := fmt.Fprintf(command.OutOrStdout(), "%-24s %-9s %s\n", stream.Name, state, strings.Join(repositories, " ")); err != nil {
			return err
		}
	}
	for _, broken := range unreadable {
		if _, err := fmt.Fprintf(command.OutOrStdout(), "%-24s %-9s unreadable: %s\n", broken.Name, "?", broken.Reason); err != nil {
			return err
		}
	}
	return nil
}

func streamEndOutput(command *cobra.Command, format string, result streams.EndResult) error {
	outcome := outcomeSuccess
	if len(result.Errors) > 0 {
		outcome = outcomeFindings
	}
	if format == "json" {
		if err := writeStreamJSON(command.OutOrStdout(), streamEnvelope{
			Version: 1, Verb: "stream end", Outcome: outcome, Evidence: result,
		}); err != nil {
			return err
		}
	} else {
		out := command.OutOrStdout()
		verb := "would end"
		if result.Applied {
			verb = "ended"
		}
		if _, err := fmt.Fprintf(out, "%s stream %s\n", verb, result.Stream); err != nil {
			return err
		}
		for _, member := range result.Members {
			if _, err := fmt.Fprintf(out, "  %-28s worktree_removed=%t draft=%s %s\n",
				member.Repository, member.WorktreeRemoved, member.DraftAction, member.Detail); err != nil {
				return err
			}
		}
		for _, pullRequest := range result.AgentPullRequests {
			if _, err := fmt.Fprintf(out, "  agent PR %s #%d: %s %s\n",
				pullRequest.Repository, pullRequest.Number, pullRequest.Action, pullRequest.Detail); err != nil {
				return err
			}
		}
		for _, failure := range result.Errors {
			if _, err := fmt.Fprintf(out, "  ! %s\n", failure); err != nil {
				return err
			}
		}
		if !result.Applied {
			if _, err := fmt.Fprintln(out, "nothing was changed; re-run with --apply"); err != nil {
				return err
			}
		}
	}
	if outcome == outcomeFindings {
		return &exitError{code: exitFindings, message: "stream end reported findings"}
	}
	return nil
}

func writeStreamJSON(out io.Writer, envelope streamEnvelope) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(envelope)
}

// workLogFlags groups the provenance flags a stream verb passes straight
// through to the existing worktree creation path.
type workLogFlags struct {
	mode, effortID, runID, initiator, agentID, agentRuntime, model, cli, provider, originalPrompt string
}

func addStreamWorkLogFlags(command *cobra.Command, mode, effortID, runID, initiator, agentID, agentRuntime, model, cli, provider, originalPrompt *string) {
	command.Flags().StringVar(mode, "mode", "auto", "execution mode: auto, agent, or manual")
	command.Flags().StringVar(effortID, "effort", "", "effort identifier recorded in the Work Log")
	command.Flags().StringVar(runID, "run", "", "run identifier recorded in the Work Log")
	command.Flags().StringVar(initiator, "initiator", "", "human who asked for this work (required by --mode manual)")
	command.Flags().StringVar(agentID, "agent", "", "agent identifier recorded in the Work Log")
	command.Flags().StringVar(agentRuntime, "agent-runtime", "", "agent harness recorded in the Work Log")
	command.Flags().StringVar(model, "model", "", "exact child model, or the explicit value unknown")
	command.Flags().StringVar(cli, "cli", "", "CLI recorded in the Work Log")
	command.Flags().StringVar(provider, "provider", "", "routing or billing provider identity, never a credential")
	command.Flags().StringVar(originalPrompt, "original-prompt-file", "", "file holding the exact task request, or - for stdin (required)")
}

// streamWorkLog prepares the same Work Log options `wb worktree create`
// requires, including the mandatory private prompt archive. A stream is a
// task, so it carries a task's provenance; nothing here is stream-specific.
func streamWorkLog(command *cobra.Command, task string, flags workLogFlags) (worktrees.WorkLogOptions, bool, error) {
	if flags.mode != "" && flags.mode != "auto" && flags.mode != "agent" && flags.mode != "manual" {
		return worktrees.WorkLogOptions{}, false, fmt.Errorf("unsupported execution mode %q; use auto, agent, or manual", flags.mode)
	}
	agentMode := flags.mode == "agent" || (flags.mode == "auto" && (strings.TrimSpace(flags.agentID) != "" || strings.TrimSpace(flags.agentRuntime) != ""))
	if flags.mode == "manual" && strings.TrimSpace(flags.initiator) == "" {
		return worktrees.WorkLogOptions{}, false, errors.New("manual execution mode requires --initiator so the non-agent mutation is auditable")
	}
	if agentMode {
		if _, ok := worktrees.RegisteredIdentity(); !ok {
			return worktrees.WorkLogOptions{}, false, errors.New("agent-mode stream creation requires a live registered session; register before the first mutation with `wb session register --pid $PPID --runtime <harness> --model <model>`, or select --mode manual --initiator <human>")
		}
	}
	workLog := worktrees.WorkLogOptions{
		EffortID: flags.effortID, RunID: flags.runID, Initiator: flags.initiator,
		AgentID: flags.agentID, AgentRuntime: flags.agentRuntime, Model: flags.model,
		CLI: flags.cli, Provider: flags.provider, OriginalPrompt: flags.originalPrompt,
		RequireOriginalPrompt: true,
	}
	if flags.originalPrompt == "-" {
		stdinBytes, err := io.ReadAll(command.InOrStdin())
		if err != nil {
			return worktrees.WorkLogOptions{}, false, fmt.Errorf("read --original-prompt-file - from stdin: %w", err)
		}
		workLog, err = workLog.WithOriginalPromptFromStdin(stdinBytes)
		if err != nil {
			return worktrees.WorkLogOptions{}, false, err
		}
	}
	prepared, err := worktrees.PrepareWorkLogOptions(projectsRoot, task, workLog)
	if err != nil {
		return worktrees.WorkLogOptions{}, false, err
	}
	return prepared, agentMode, nil
}
