package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/streamabsorb"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/streamsync"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
)

func newStreamAbsorbCmd() *cobra.Command {
	var (
		format, title, summary, reason, streamName string
		keepCommits                                []string
		verify                                     bool
		timeout                                    time.Duration
	)
	command := &cobra.Command{
		Use:   "absorb <agent-worktree>",
		Short: "Land an agent's work on the stream branch, locally, as one reviewed commit",
		Long: `Absorb an agent's work into the stream branch — locally.

THERE ARE NO PULL REQUESTS BELOW THE STREAM. An agent branches from
stream/<name>, works, and its change reaches the stream branch through this
verb: a rebase and a squash, entirely local. Absorb never pushes, and never
opens, updates or merges a pull request. The only pull request per repository
per stream is the draft stream pull request.

The work is still reviewed and still lands as one reviewed commit; what
disappears is the pull request that carried it. The review therefore hangs on
the CONTENT: absorb refuses without a recorded APPROVE for exactly the
patch-identity set it is about to absorb, computed with 'git patch-id --stable'
over the commits the stream branch does not already carry.

A content-identical rebase carries the approval forward, because the patch set
is unchanged. Any content change invalidates it and needs a new review round.
A mechanical bump — a diff touching only dependency manifests and lockfiles —
skips the ledger, exactly as it does at landing.

The squash message is AGGREGATED, never defaulted: the title as subject, the
summary as body, one line per source commit, and the reviewed patch set. A
squash that kept only a title would discard every message the branch carried,
and 'git log' could no longer answer what the change contained.

--keep-commits preserves the named commits instead of squashing. It requires
--reason, and every kept commit must build on its own — keeping commits is only
better than squashing if the history stays bisectable.

Refusals (exit 2):
  unapproved-patch-set        no APPROVE for this exact patch set —
                              'wb review request' / 'wb review record'
  keep-without-reason         --keep-commits without --reason
  kept-commit-does-not-build  a kept commit does not compile on its own
  nothing-to-absorb           the branch carries no commit the stream lacks`,
		Example: `# Absorb an approved agent worktree as one commit
wb stream absorb /path/to/agent-worktree --title "feat(checkout): accept saved cards" --verify

# Keep the commits, and say why
wb stream absorb /path/to/agent-worktree --keep-commits abc1234,def5678 --reason "two independent migrations"`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			// The live-link guard runs before anything: absorbing a worktree
			// that builds against an unpublished library would put that
			// content on the stream branch.
			if err := refuseLinkedWorktrees([]string{args[0]}); err != nil {
				return err
			}
			worktree, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			store, err := streams.Open(projectsRoot)
			if err != nil {
				return err
			}
			resolved, member, err := resolveAbsorbTarget(store, streamName, worktree)
			if err != nil {
				return err
			}
			git := streamabsorb.ExecGit{ExecGit: streamsync.ExecGit{Timeout: timeout}}
			agentBranch, err := git.CurrentBranch(command.Context(), worktree)
			if err != nil {
				return err
			}
			engine := &streamabsorb.Engine{
				Git:    git,
				Ledger: streamabsorb.EventLedger{Log: store.EventLog(resolved.Name)},
				Sync: &streamsync.Engine{
					Git: streamsync.ExecGit{Timeout: timeout}, Verifier: batchVerifier{timeout: timeout},
					CI: workflowMechanisms{}, Events: streamEventSink{log: store.EventLog(resolved.Name)},
				},
				Events: streamEventSink{log: store.EventLog(resolved.Name)},
			}
			result, err := engine.Absorb(command.Context(), streamabsorb.Options{
				Stream: resolved.Name, AgentWorktree: worktree, AgentBranch: agentBranch,
				StreamWorktree: member.Worktree, StreamBranch: member.Branch,
				Repository: member.Repository, Title: title, Summary: summary,
				KeepCommits: splitCommas(keepCommits), Reason: reason,
				Verify: verify, Timeout: timeout,
			})
			if err != nil {
				var refusal *streamabsorb.Refusal
				if errors.As(err, &refusal) {
					return &exitError{code: exitUsage, message: refusal.Error()}
				}
				return err
			}
			return printAbsorb(command, format, result)
		},
	}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().StringVar(&title, "title", "", "subject of the squashed commit (default: the first source commit's subject)")
	command.Flags().StringVar(&summary, "summary", "", "body of the squashed commit")
	command.Flags().StringArrayVar(&keepCommits, "keep-commits", nil, "preserve these commits instead of squashing; requires --reason")
	command.Flags().StringVar(&reason, "reason", "", "why the commits are kept (required by --keep-commits)")
	command.Flags().StringVar(&streamName, "stream", "", "stream to absorb into (default: the one holding the worktree)")
	command.Flags().BoolVar(&verify, "verify", false, "run the batch verification over the absorbed result")
	command.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "maximum duration per external command")
	markLandingGuard(command, landingGuardByWorktree)
	setDiscoveryTerms(command, "stream absorb land agent work squash rebase local review approve patch set no pull request")
	return command
}

// resolveAbsorbTarget finds the stream and the member worktree the agent
// checkout belongs to.
func resolveAbsorbTarget(store *streams.Store, name, worktree string) (streams.Stream, streams.Member, error) {
	all, unreadable, err := store.List()
	if err != nil {
		return streams.Stream{}, streams.Member{}, err
	}
	if len(unreadable) > 0 {
		names := make([]string, 0, len(unreadable))
		for _, broken := range unreadable {
			names = append(names, broken.Name)
		}
		return streams.Stream{}, streams.Member{}, fmt.Errorf(
			"stream state is unreadable for %s; absorb must know which stream this work belongs to", strings.Join(names, ", "))
	}
	for _, stream := range all {
		if !stream.Open() || (name != "" && stream.Name != name) {
			continue
		}
		// The agent worktree lives under the stream's task directory, so the
		// member is the one whose repository the agent checkout mirrors.
		for _, member := range stream.Members {
			if member.Worktree == "" {
				continue
			}
			if strings.HasPrefix(worktree, filepath.Dir(filepath.Dir(member.Worktree))) ||
				filepath.Base(filepath.Dir(worktree)) == filepath.Base(filepath.Dir(member.Worktree)) {
				return stream, member, nil
			}
		}
	}
	return streams.Stream{}, streams.Member{}, fmt.Errorf(
		"no open stream holds a member matching %s; absorb needs the stream branch to land onto — pass --stream, or `wb stream join` the repository", worktree)
}

func splitCommas(values []string) []string {
	var split []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				split = append(split, trimmed)
			}
		}
	}
	return split
}

func printAbsorb(command *cobra.Command, format string, result streamabsorb.Result) error {
	if format == "json" {
		encoder := json.NewEncoder(command.OutOrStdout())
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(result); err != nil {
			return err
		}
	} else {
		out := command.OutOrStdout()
		if _, err := fmt.Fprintf(out, "absorbed %s into %s\n", result.Branch, result.Repository); err != nil {
			return err
		}
		if result.Mechanical {
			if _, err := fmt.Fprintln(out, "  mechanical bump: the ledger does not apply"); err != nil {
				return err
			}
		} else if result.Approval != nil {
			if _, err := fmt.Fprintf(out, "  approved: round %d by %s\n", result.Approval.Round, result.Approval.By); err != nil {
				return err
			}
		}
		if result.Commit != "" {
			if _, err := fmt.Fprintf(out, "  one commit: %s\n", result.Commit); err != nil {
				return err
			}
		}
		for _, kept := range result.Kept {
			if _, err := fmt.Fprintf(out, "  kept: %s\n", kept); err != nil {
				return err
			}
		}
		if result.Batch != nil {
			if err := printBatch(out, *result.Batch); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(out, "  nothing was pushed: the stream's own landing owns that"); err != nil {
			return err
		}
		for _, failure := range result.Errors {
			if _, err := fmt.Fprintf(out, "  ! %s\n", failure); err != nil {
				return err
			}
		}
	}
	if result.Failed() {
		return &exitError{code: exitFindings, message: "stream absorb reported findings; see the report above"}
	}
	return nil
}

func newReviewCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "review",
		Short: "Record review verdicts against a local patch set",
		Long: `A review below the stream has no pull request to hang on, so it hangs on the
content: the set of 'git patch-id --stable' values over the commits a branch
carries that the stream branch does not.

'wb stream absorb' refuses without a recorded APPROVE for exactly that set. A
content-identical rebase carries the approval forward; any content change
invalidates it and needs a new round.`,
	}
	command.AddCommand(newReviewRecordCmd())
	setDiscoveryTerms(command, "review record verdict approve reject ledger patch set local")
	return command
}

func newReviewRecordCmd() *cobra.Command {
	var (
		worktreePath, verdict, by, note, streamName, format string
		round                                               int
		timeout                                             time.Duration
	)
	command := &cobra.Command{
		Use:   "record",
		Short: "Record a review verdict for a worktree's current patch set",
		Long: `Record a verdict against the patch-identity set a worktree currently carries.

The verdict is keyed on the CONTENT, not on a SHA: the set of
'git patch-id --stable' values over the commits the stream branch does not
already have. That is what lets an approval survive a content-identical rebase
and lapse the moment the content changes.

Only APPROVE clears absorption. APPROVE-WITH-FIXES deliberately does not: the
fixes it asks for change the content, which produces a different patch set, and
absorbing the unfixed set would land exactly the code the reviewer asked to
change.

Verdicts are appended to the stream's event log, or to the fleet log for a
worktree outside every stream. Nothing is ever rewritten, so a later round never
erases the one before it and the newest record for a patch set is the answer.`,
		Example: `wb review record --worktree /path/to/agent-worktree --verdict APPROVE --round 1 --by reviewer
wb review record --worktree /path/to/agent-worktree --verdict REJECT --round 2 --note "races on the shared cache"`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			if strings.TrimSpace(worktreePath) == "" {
				return &exitError{code: exitUsage, message: "--worktree is required: a verdict is recorded against a specific checkout"}
			}
			parsed, err := streamabsorb.ParseVerdict(verdict)
			if err != nil {
				return &exitError{code: exitUsage, message: err.Error()}
			}
			if round < 1 {
				return &exitError{code: exitUsage, message: "--round must be 1 or greater, so successive rounds are distinguishable"}
			}
			worktree, err := filepath.Abs(worktreePath)
			if err != nil {
				return err
			}
			store, err := streams.Open(projectsRoot)
			if err != nil {
				return err
			}
			git := streamabsorb.ExecGit{ExecGit: streamsync.ExecGit{Timeout: timeout}}
			branch, err := git.CurrentBranch(command.Context(), worktree)
			if err != nil {
				return err
			}

			log, stream, err := reviewLedgerFor(store, streamName, worktree)
			if err != nil {
				return err
			}
			upstream := "main"
			if stream.Name != "" {
				upstream = streams.Branch(stream.Name)
			}
			commits, err := git.CommitsNotIn(command.Context(), worktree, branch, upstream)
			if err != nil {
				return err
			}
			if len(commits) == 0 {
				return &exitError{code: exitUsage, message: fmt.Sprintf(
					"%s carries no commit %s does not already have; there is nothing to review", branch, upstream)}
			}
			head, err := git.Head(command.Context(), worktree, branch)
			if err != nil {
				return err
			}
			patchIDs := make([]string, 0, len(commits))
			for _, commit := range commits {
				patchIDs = append(patchIDs, commit.PatchID)
			}
			set := streamabsorb.NewPatchSet(head, patchIDs)
			record := streamabsorb.Record{
				Stream: stream.Name, Worktree: worktree, Branch: branch,
				Verdict: parsed, Round: round, By: by, Note: note,
				Fingerprint: set.Fingerprint(), PatchSet: set,
			}
			if err := (streamabsorb.EventLedger{Log: log}).Record(record); err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(record)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(),
				"recorded %s round %d for %d commit(s) on %s (patch set %s)\n",
				parsed, round, len(commits), branch, record.Fingerprint[:12])
			return err
		},
	}
	command.Flags().StringVar(&worktreePath, "worktree", "", "checkout whose patch set is being reviewed (required)")
	command.Flags().StringVar(&verdict, "verdict", "", "APPROVE, APPROVE-WITH-FIXES or REJECT (required)")
	command.Flags().IntVar(&round, "round", 1, "review round, 1 or greater")
	command.Flags().StringVar(&by, "by", "", "who recorded the verdict")
	command.Flags().StringVar(&note, "note", "", "reviewer's note, recorded verbatim")
	command.Flags().StringVar(&streamName, "stream", "", "stream whose log records it (default: the one holding the worktree)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "maximum duration per external command")
	setDiscoveryTerms(command, "review record verdict approve reject round ledger patch identity local head")
	return command
}

// reviewLedgerFor selects the log a verdict is written to: the stream's own
// where there is one, the fleet log otherwise — a review still has to be
// recorded somewhere when the repository is in no stream.
func reviewLedgerFor(store *streams.Store, name, worktree string) (*streams.FileEventLog, streams.Stream, error) {
	all, _, err := store.List()
	if err != nil {
		return nil, streams.Stream{}, err
	}
	for _, stream := range all {
		if !stream.Open() || (name != "" && stream.Name != name) {
			continue
		}
		for _, member := range stream.Members {
			if member.Worktree == "" {
				continue
			}
			if strings.HasPrefix(worktree, filepath.Dir(filepath.Dir(member.Worktree))) {
				return store.EventLog(stream.Name), stream, nil
			}
		}
	}
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return nil, streams.Stream{}, err
	}
	return &streams.FileEventLog{Path: filepath.Join(home, "fleet", "events.jsonl")}, streams.Stream{}, nil
}

// runStreamAbsorbAlias implements `wb worktree merge --route stream`.
//
// Inside a stream there are no pull requests below the stream branch, so
// "merge this worktree" means absorb it. The alias delegates to the same
// command rather than reproducing its guards, so the approval gate, the
// aggregated message and the live-link refusal cannot drift between the two
// spellings.
func runStreamAbsorbAlias(command *cobra.Command, args []string) error {
	if len(args) != 1 {
		return &exitError{
			code:    exitUsage,
			message: "--route stream absorbs exactly one agent worktree; pass one path, or use `wb stream absorb`",
		}
	}
	absorb := newStreamAbsorbCmd()
	absorb.SetOut(command.OutOrStdout())
	absorb.SetErr(command.ErrOrStderr())
	absorb.SetContext(command.Context())
	absorb.SetArgs(args)
	return absorb.Execute()
}
