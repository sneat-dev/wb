package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/progress"
)

type worktreeMergeFlags struct {
	target, route, onFailure, format       string
	rebatchReceipt                         string
	model, runtime, agentID, cli, provider string
	cleanup                                bool
	progress                               bool
	stopBeforeMerge                        bool
	timeout                                time.Duration
	retry                                  int
	interval                               time.Duration
}

func newWorktreeMergeCmd() *cobra.Command {
	var flags worktreeMergeFlags
	command := &cobra.Command{
		Use:   "merge <source-worktree...>",
		Short: "Prepare and mechanically land compatible WB worktrees",
		Long: `Prepare an isolated integration candidate, then land it through an
authoritatively permitted direct push or pull request. Phase 1 is prepared locally, not landed,
and its immutable SHA can unblock dependent agents. WB will never force-push. Landing is
proved against the exact remote target before optional receipt-gated cleanup. Clean target
drift rebases an unpublished candidate and reruns validation; a conflict or already-published
candidate stops without rewriting it. Candidate validation consumes tracked .wb/quality.yaml
policy; an exact target baseline runs only when candidate failure needs comparison. A landed failure retains before/after evidence for a
forward revert. If exact post-target CI fails and the same source advances with a clean
forward repair, rerunning merge advances the retained candidate onto the landed target,
records the failed landing, and opens a new repair PR without rewriting history.
Use acknowledge-landed-failed only for an audited historical
validation_failed or landed_post_target_ci_failed receipt whose exact candidate
is already contained in the current remote target; it writes a separate
acknowledgement rather than rewriting the failed receipt. Use
acknowledge-stranded-landing only for a land conflict receipt whose published
pull request is proved MERGED and still contained in the current remote target
using only GitHub's own state -- for exactly the case where the candidate
worktree that acknowledge-landed-failed would otherwise need is already gone.
Use seal-validation-failed to prepare a target-tree-identical ancestry-only
replacement when an audited squash landing broke the historical graph. Use
supersede-validation-failed only for a prepare failure that did not land: it
binds a separately proved replacement candidate without rewriting history.`,
		Example: `# Finish one compatible worktree end to end
wb worktree merge . --route auto --cleanup

# Split preparation from landing for a resumable handoff
wb worktree merge prepare /path/to/worktree --format json
wb worktree merge land /path/to/landing-receipt --cleanup

# Free a stale lane only after proving the failed candidate already landed
wb worktree merge acknowledge-landed-failed /path/to/merge-receipt --apply --actor operator --reason "audited landed validation failure"

# Free a stale lane after proving a stranded published PR landed, using only GitHub's remote state
wb worktree merge acknowledge-stranded-landing /path/to/merge-receipt --apply --actor operator --reason "audited stranded landing"

# Prepare an ancestry-only replacement without changing the target tree
wb worktree merge seal-validation-failed /path/to/merge-receipt --apply --actor operator --reason "audited squash recovery"

# Replace a stale prepare failure only after proving every immutable root
wb worktree merge supersede-validation-failed /path/to/merge-receipt /path/to/replacement --apply --actor operator --reason "audited replacement candidate"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			// --route stream is an alias of `wb stream absorb`: inside a
			// stream there are no pull requests below the stream branch, so
			// "merge this worktree" means absorb it locally. Routing here
			// rather than duplicating the logic keeps one implementation.
			if flags.route == "stream" {
				return runStreamAbsorbAlias(command, args)
			}
			if err := validateWorktreeMergeFlags(flags); err != nil {
				return err
			}
			// A live local link builds this worktree against an unpublished
			// working tree, so it must never be pushed or landed. The guard
			// runs before any candidate is prepared.
			if err := refuseLinkedWorktrees(args); err != nil {
				return err
			}
			campaign := newWorktreeMergeProgress(command, flags)
			receipt, err := orchestrate.RunWorktreeMerge(command.Context(), prepareMergeOptions(flags, args, campaign.reporter()), landMergeOptions(flags, "", campaign.reporter()))
			finishWorktreeMergeProgress(campaign, receipt, err)
			if writeErr := writeWorktreeMergeReceipt(command.OutOrStdout(), flags.format, receipt); writeErr != nil && err == nil {
				return writeErr
			}
			return err
		},
	}
	setDiscoveryTerms(command, "finish work merge land deliver ship integrate complete cleanup agent worktree branch pull request main")
	markLandingGuard(command, landingGuardByWorktree)
	bindWorktreeMergeFlags(command, &flags, true, true)
	command.AddCommand(newWorktreeMergePrepareCmd(), newWorktreeMergeLandCmd("land"), newWorktreeMergeLandCmd("resume"), newWorktreeMergeRevertCmd())
	command.AddCommand(newWorktreeMergeAcknowledgeLandedFailedCmd(), newWorktreeMergeAcknowledgeStrandedLandingCmd(), newWorktreeMergeAcknowledgeReceiptCollisionCmd(), newWorktreeMergeSealValidationFailedCmd(), newWorktreeMergeSupersedeValidationFailedCmd(), newWorktreeMergeCorrectSelfSupersessionCmd(), newWorktreeMergePreparePublishedForwardRepairCmd())
	return command
}

func newWorktreeMergeSealValidationFailedCmd() *cobra.Command {
	var apply bool
	var actor, reason, format string
	var model, runtime, agentID, cli, provider string
	var timeout time.Duration
	var retry int
	command := &cobra.Command{
		Use:   "seal-validation-failed <merge-receipt>",
		Short: "Prepare a target-tree-identical ancestry seal for a failed receipt",
		Long: `Prepare a fresh WB-managed replacement candidate at the exact current
remote target for one historical prepare validation_failed receipt. Any missing
immutable failed-candidate claim base, receipt target, current remote target,
and receipted source ancestry is added with a no-content merge. WB then requires
the final candidate tree to equal the fetched target tree exactly and rechecks
every source, target, claim, and clean-worktree boundary. The failed receipt and
all existing Work Logs remain immutable. This is a dry-run by default; --apply
requires --actor and --reason. The resulting candidate must still be recorded
separately with supersede-validation-failed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			identity, releaseAdmission, err := requireMutationAdmission(command, apply)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			if strings.TrimSpace(model) == "" {
				model = identity.Model
			}
			if strings.TrimSpace(runtime) == "" {
				runtime = identity.Runtime
			}
			if strings.TrimSpace(agentID) == "" {
				agentID = identity.AgentID
			}
			seal, err := orchestrate.PrepareValidationFailedWorktreeMergeSeal(command.Context(), orchestrate.WorktreeMergeValidationFailureSealOptions{
				ProjectsRoot: projectsRoot, Receipt: args[0], Apply: apply, Actor: actor, Reason: reason,
				Model: model, AgentRuntime: runtime, AgentID: agentID, Initiator: mutationInitiator(command), CLI: cli, Provider: provider,
				SessionRequired: identity.Registered, Timeout: timeout, Retry: retry,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(seal)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "status: %s\nreceipt: %s\ncurrent-target: %s\ntarget-tree: %s\ncandidate: %s\n",
				seal.Status, seal.ReceiptPath, seal.CurrentTargetSHA, seal.TargetTreeSHA, seal.Candidate.Worktree)
			if !apply {
				_, _ = fmt.Fprintln(command.OutOrStdout(), "dry-run only, pass --apply to create the ancestry seal candidate")
			}
			return err
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "create the WB-managed ancestry seal candidate")
	command.Flags().StringVar(&actor, "actor", "", "required with --apply: trusted operator or agent identity")
	command.Flags().StringVar(&reason, "reason", "", "required with --apply: bounded audited recovery reason")
	command.Flags().StringVar(&model, "model", "", "model identity recorded in the replacement Work Log")
	command.Flags().StringVar(&runtime, "agent-runtime", "", "agent runtime recorded in the replacement Work Log")
	command.Flags().StringVar(&agentID, "agent-id", "", "agent identity recorded in the replacement Work Log")
	command.Flags().StringVar(&cli, "cli", "wb", "CLI identity recorded in the replacement Work Log")
	command.Flags().StringVar(&provider, "provider", "", "routing or billing provider identity, never a credential")
	command.Flags().DurationVar(&timeout, "timeout", 8*time.Minute, "bounded Git operation timeout")
	command.Flags().IntVar(&retry, "retry", 0, "retry transient Git command failures")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	addMutationAdmissionFlags(command)
	return command
}

func newWorktreeMergePrepareCmd() *cobra.Command {
	var flags worktreeMergeFlags
	command := &cobra.Command{
		Use: "prepare <source-worktree...>", Short: "Create and validate an isolated local integration candidate",
		Args: cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := validateWorktreeMergeFlags(flags); err != nil {
				return err
			}
			if err := refuseLinkedWorktrees(args); err != nil {
				return err
			}
			campaign := newWorktreeMergeProgress(command, flags)
			receipt, err := orchestrate.PrepareWorktreeMerge(command.Context(), prepareMergeOptions(flags, args, campaign.reporter()))
			finishWorktreeMergeProgress(campaign, receipt, err)
			if writeErr := writeWorktreeMergeReceipt(command.OutOrStdout(), flags.format, receipt); writeErr != nil && err == nil {
				return writeErr
			}
			return err
		},
	}
	markLandingGuard(command, landingGuardByWorktree)
	bindWorktreeMergeFlags(command, &flags, true, false)
	command.Flags().StringVar(&flags.rebatchReceipt, "rebatch-receipt", "", "immutable prepared receipt to replace with an additive source-set rebatch")
	return command
}

func newWorktreeMergeLandCmd(name string) *cobra.Command {
	var flags worktreeMergeFlags
	command := &cobra.Command{
		Use: name + " <candidate-worktree-or-receipt>", Short: "Resume a receipt and land its exact integration candidate",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := validateWorktreeMergeFlags(flags); err != nil {
				return err
			}
			// The land verbs are the ones that push, so the live-link guard
			// has to run here too — it used to cover only merge/prepare, so
			// preparing before linking and then landing the receipt walked a
			// linked worktree straight past it.
			if err := refuseLinkedReceiptWorktrees(args[0]); err != nil {
				return err
			}
			campaign := newWorktreeMergeProgress(command, flags)
			receipt, err := orchestrate.ResumeWorktreeMerge(command.Context(), landMergeOptions(flags, args[0], campaign.reporter()))
			finishWorktreeMergeProgress(campaign, receipt, err)
			if writeErr := writeWorktreeMergeReceipt(command.OutOrStdout(), flags.format, receipt); writeErr != nil && err == nil {
				return writeErr
			}
			return err
		},
	}
	markLandingGuard(command, landingGuardByReceipt)
	bindWorktreeMergeFlags(command, &flags, false, true)
	if name == "resume" {
		command.Flags().BoolVar(&flags.stopBeforeMerge, "stop-before-merge", false, "PR-only: validate and publish the exact candidate, prove the open PR, then stop before checks or merge")
	}
	return command
}

func newWorktreeMergeRevertCmd() *cobra.Command {
	var flags worktreeMergeFlags
	command := &cobra.Command{
		Use: "revert <landing-receipt>", Short: "Prepare and land a forward revert without rewriting history",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := validateWorktreeMergeFlags(flags); err != nil {
				return err
			}
			campaign := newWorktreeMergeProgress(command, flags)
			progress.Report(campaign.reporter(), progress.Event{Operation: "worktree_merge", Phase: "prepare_revert", State: progress.Started, Detail: args[0]})
			receipt, err := orchestrate.PrepareWorktreeMergeRevert(command.Context(), projectsRoot, args[0], flags.timeout, flags.retry)
			if err == nil {
				receipt, err = orchestrate.LandWorktreeMerge(command.Context(), landMergeOptions(flags, receipt.ReceiptPath, campaign.reporter()))
			}
			finishWorktreeMergeProgress(campaign, receipt, err)
			if writeErr := writeWorktreeMergeReceipt(command.OutOrStdout(), flags.format, receipt); writeErr != nil && err == nil {
				return writeErr
			}
			return err
		},
	}
	bindWorktreeMergeFlags(command, &flags, false, true)
	return command
}

func newWorktreeMergeAcknowledgeLandedFailedCmd() *cobra.Command {
	var apply bool
	var actor, reason, format string
	command := &cobra.Command{
		Use:   "acknowledge-landed-failed <merge-receipt>",
		Short: "Acknowledge a proved landed failure without rewriting its receipt",
		Long: `Prove that either a validation_failed prepare receipt or a
landed_post_target_ci_failed land receipt has its clean candidate and every
receipted source contained in the exact current remote target, then record a
separate audited acknowledgement so a fresh forward repair can own the lane.
Post-target CI receipts must also prove their exact failed landing. The immutable
Work Log and historical merge receipt are never rewritten. This is a dry-run by
default; --apply requires --actor and --reason and writes only the new
acknowledgement artifact. Any missing claim, target, ancestry, or cleanliness
proof refuses closed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, releaseAdmission, err := requireMutationAdmission(command, apply)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			ack, err := orchestrate.AcknowledgeLandedMergeFailure(command.Context(), orchestrate.WorktreeMergeLandedFailureAcknowledgementOptions{
				ProjectsRoot: projectsRoot, Receipt: args[0], Apply: apply, Actor: actor, Reason: reason,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(ack)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "status: %s\nreceipt: %s\ncandidate: %s\ncurrent-target: %s\nacknowledgement: %s\n",
				ack.Status, ack.ReceiptPath, ack.CandidateSHA, ack.CurrentTargetSHA, ack.AcknowledgementPath)
			if !apply {
				_, _ = fmt.Fprintln(command.OutOrStdout(), "dry-run only, pass --apply to write")
			}
			return err
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "write the separate audited acknowledgement artifact")
	command.Flags().StringVar(&actor, "actor", "", "required with --apply: trusted operator or agent identity")
	command.Flags().StringVar(&reason, "reason", "", "required with --apply: bounded audited acknowledgement reason")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	addMutationAdmissionFlags(command)
	return command
}

func newWorktreeMergeAcknowledgeStrandedLandingCmd() *cobra.Command {
	var apply bool
	var actor, reason, format string
	command := &cobra.Command{
		Use:   "acknowledge-stranded-landing <merge-receipt>",
		Short: "Acknowledge a proved stranded pull-request landing without rewriting its receipt",
		Long: `Prove, using only GitHub's own remote state, that a land conflict
receipt's exact published pull request reports MERGED and that its server
merge commit and preserved candidate are both contained in the freshly
fetched current remote target, then record a separate audited acknowledgement
so a fresh candidate can own the lane. This accepts only a land-phase conflict
receipt that never recorded a landing SHA but did publish an exact candidate
in a pull request: the case where a resume's own landing-result read failed
on pure I/O or environment error, typically because the candidate worktree
was already removed. Unlike acknowledge-landed-failed, this proof never reads
or requires the candidate or any receipted source worktree. The immutable
Work Log and historical merge receipt are never rewritten. This is a dry-run
by default; --apply requires --actor and --reason and writes only the new
acknowledgement artifact. A pull request that is not proved MERGED, or a
merge commit or candidate not proved contained in the current remote target,
refuses closed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, releaseAdmission, err := requireMutationAdmission(command, apply)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			ack, err := orchestrate.AcknowledgeStrandedPullRequestLanding(command.Context(), orchestrate.WorktreeMergeStrandedLandingAcknowledgementOptions{
				ProjectsRoot: projectsRoot, Receipt: args[0], Apply: apply, Actor: actor, Reason: reason,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(ack)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "status: %s\nreceipt: %s\ncandidate: %s\nproved-landing: %s\ncurrent-target: %s\nacknowledgement: %s\n",
				ack.Status, ack.ReceiptPath, ack.CandidateSHA, ack.ProvedLandingSHA, ack.CurrentTargetSHA, ack.AcknowledgementPath)
			if !apply {
				_, _ = fmt.Fprintln(command.OutOrStdout(), "dry-run only, pass --apply to write")
			}
			return err
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "write the separate audited acknowledgement artifact")
	command.Flags().StringVar(&actor, "actor", "", "required with --apply: trusted operator or agent identity")
	command.Flags().StringVar(&reason, "reason", "", "required with --apply: bounded audited acknowledgement reason")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	addMutationAdmissionFlags(command)
	return command
}

func newWorktreeMergeAcknowledgeReceiptCollisionCmd() *cobra.Command {
	var apply bool
	var actor, reason, format string
	var receiptSHA, claimSHA, targetSHA, candidateSHA, currentSourceSHA, historicalSourceSHA string
	command := &cobra.Command{
		Use:   "acknowledge-receipt-collision <merge-receipt>",
		Short: "Append an audited acknowledgement for one historical receipt collision",
		Long: `Record the narrowly scoped recovery evidence for a known prepare receipt
collision without rewriting its receipt or Work Log. The caller must explicitly
pin the current receipt SHA256, immutable claim SHA256, current target,
candidate, current source, and historical refresh source. WB re-reads all
evidence under the lane lock, requires an unlanded clean unpublished preparing
candidate, and writes only <receipt>.receipt-collision.ack.json. The historical validation_failed
status is recorded as an operator assertion because no pre-mutation
receipt digest exists. This is a dry-run by default; --apply also requires
--actor and --reason.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, releaseAdmission, err := requireMutationAdmission(command, apply)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			ack, err := orchestrate.AcknowledgeWorktreeMergeReceiptCollision(command.Context(), orchestrate.WorktreeMergeReceiptCollisionAcknowledgementOptions{
				ProjectsRoot: projectsRoot, Receipt: args[0], Apply: apply, Actor: actor, Reason: reason,
				ExpectedReceiptSHA256: receiptSHA, ExpectedImmutableClaimSHA256: claimSHA, ExpectedTargetSHA: targetSHA,
				ExpectedCandidateSHA: candidateSHA, ExpectedCurrentSourceSHA: currentSourceSHA, ExpectedHistoricalRefreshSourceSHA: historicalSourceSHA,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(ack)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "status: %s\nreceipt: %s\nacknowledgement: %s\ncandidate: %s\ncurrent-target: %s\n",
				ack.Status, ack.ReceiptPath, ack.AcknowledgementPath, ack.Candidate.SHA, ack.ExpectedTargetSHA)
			if !apply {
				_, _ = fmt.Fprintln(command.OutOrStdout(), "dry-run only, pass --apply to write")
			}
			return err
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "write the separate audited acknowledgement artifact")
	command.Flags().StringVar(&actor, "actor", "", "required with --apply: trusted operator or agent identity")
	command.Flags().StringVar(&reason, "reason", "", "required with --apply: bounded audited recovery reason")
	command.Flags().StringVar(&receiptSHA, "expected-receipt-sha256", "", "required SHA256 of the current mutated receipt")
	command.Flags().StringVar(&claimSHA, "expected-immutable-claim-sha256", "", "required SHA256 of the immutable candidate Work Log claim")
	command.Flags().StringVar(&targetSHA, "expected-target", "", "required exact current remote target SHA")
	command.Flags().StringVar(&candidateSHA, "expected-candidate", "", "required exact collision candidate SHA")
	command.Flags().StringVar(&currentSourceSHA, "expected-current-source", "", "required exact current source SHA in the receipt")
	command.Flags().StringVar(&historicalSourceSHA, "expected-historical-refresh-source", "", "required exact historical source SHA from source_refreshes")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	addMutationAdmissionFlags(command)
	return command
}

func newWorktreeMergeSupersedeValidationFailedCmd() *cobra.Command {
	var apply bool
	var actor, reason, format string
	command := &cobra.Command{
		Use:   "supersede-validation-failed <merge-receipt> <replacement-worktree>",
		Short: "Supersede an unlanded failed prepare receipt with a proved replacement",
		Long: `Prove that a prepare validation_failed receipt never landed and that
one exact clean replacement candidate contains the immutable failed-candidate
claim base, receipt target, freshly fetched current remote target, and every
exact clean receipted source. The failed candidate itself need not be an
ancestor. This is a dry-run by default; --apply requires --actor and --reason
and writes only a separate append-only supersession acknowledgement. The
historical merge receipt and every Work Log remain immutable. Any missing
identity, active claim, cleanliness, receipt integrity, or ancestry proof
refuses closed.`,
		Args: cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, releaseAdmission, err := requireMutationAdmission(command, apply)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			ack, err := orchestrate.SupersedeValidationFailedWorktreeMerge(command.Context(), orchestrate.WorktreeMergeValidationFailureSupersessionOptions{
				ProjectsRoot: projectsRoot, Receipt: args[0], ReplacementWorktree: args[1], Apply: apply, Actor: actor, Reason: reason,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(ack)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "status: %s\nreceipt: %s\nreplacement: %s\ncurrent-target: %s\nacknowledgement: %s\n",
				ack.Status, ack.ReceiptPath, ack.Replacement.SHA, ack.CurrentTargetSHA, ack.AcknowledgementPath)
			if !apply {
				_, _ = fmt.Fprintln(command.OutOrStdout(), "dry-run only, pass --apply to write")
			}
			return err
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "write the separate audited supersession acknowledgement artifact")
	command.Flags().StringVar(&actor, "actor", "", "required with --apply: trusted operator or agent identity")
	command.Flags().StringVar(&reason, "reason", "", "required with --apply: bounded audited supersession reason")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	addMutationAdmissionFlags(command)
	return command
}

func newWorktreeMergeCorrectSelfSupersessionCmd() *cobra.Command {
	var apply bool
	var actor, reason, format, expectedSupersessionSHA, expectedClaimSHA string
	command := &cobra.Command{
		Use:   "correct-self-supersession <merge-receipt> <replacement-worktree>",
		Short: "Append a correction for one historical self-supersession",
		Long: `Correct only an existing validation_failed supersession acknowledgement
that incorrectly bound the failed candidate as its own replacement. The caller
must pin the exact existing acknowledgement SHA256 and immutable claim SHA256.
WB re-reads the receipt, claim, corrupt acknowledgement, target, sources, and a
distinct clean active-claim replacement under the lane lock. It writes only a
separate create-if-absent correction artifact; neither the receipt, claim, nor
self-supersession acknowledgement is ever changed. This is dry-run by default;
--apply requires --actor and --reason.`,
		Args: cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, releaseAdmission, err := requireMutationAdmission(command, apply)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			correction, err := orchestrate.CorrectValidationFailedSelfSupersession(command.Context(), orchestrate.WorktreeMergeSelfSupersessionCorrectionOptions{
				ProjectsRoot: projectsRoot, Receipt: args[0], ReplacementWorktree: args[1], ExpectedSupersessionSHA256: expectedSupersessionSHA, ExpectedImmutableClaimSHA256: expectedClaimSHA,
				Apply: apply, Actor: actor, Reason: reason,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(correction)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "status: %s\nreceipt: %s\nreplacement: %s\ncorrection: %s\n", correction.Status, correction.ReceiptPath, correction.CorrectedReplacement.SHA, correction.CorrectionPath)
			if !apply {
				_, _ = fmt.Fprintln(command.OutOrStdout(), "dry-run only, pass --apply to write")
			}
			return err
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "write the separate append-only correction artifact")
	command.Flags().StringVar(&actor, "actor", "", "required with --apply: trusted operator or agent identity")
	command.Flags().StringVar(&reason, "reason", "", "required with --apply: bounded audited correction reason")
	command.Flags().StringVar(&expectedSupersessionSHA, "expected-supersession-sha256", "", "required SHA256 of the existing self-supersession acknowledgement")
	command.Flags().StringVar(&expectedClaimSHA, "expected-immutable-claim-sha256", "", "required SHA256 of the immutable failed candidate Work Log claim")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	addMutationAdmissionFlags(command)
	return command
}

func newWorktreeMergePreparePublishedForwardRepairCmd() *cobra.Command {
	var apply bool
	var actor, reason, format, receiptSHA, claimSHA, supersessionSHA, targetSHA string
	var expectedSourceSHAs []string
	var model, runtime, agentID, cli, provider string
	var timeout time.Duration
	var retry int
	command := &cobra.Command{
		Use:   "prepare-published-forward-repair <failed-merge-receipt> <current-source-worktree...>",
		Short: "Create one evidence-pinned candidate to correct a historical self-supersession",
		Long: `Construct one distinct WB-managed candidate only for an existing historical
validation_failed self-supersession that cannot be corrected until a replacement
exists. This is not ordinary prepare or rebatch: it never changes the failed
receipt, its Work Log, the corrupt self-supersession acknowledgement, a receipt
collision acknowledgement, or any published record, and it does not write a
new merge receipt. The caller pins the failed receipt, immutable claim,
self-supersession acknowledgement, current target, and each supplied source.
WB reads every receipted and source-refresh tuple only from the immutable
receipt as a historical ancestry root; those historical worktrees need not remain live. Each
supplied source is instead a current WB-managed worktree and
must have an exact active claim, path, branch, clean HEAD, and pinned SHA.
Every historical root, claim base, receipt target, historical
self-supersession target, current target, and current repair source must be an
ancestor of the resulting clean candidate. The candidate can then be passed to
correct-self-supersession. Dry-run writes nothing; --apply requires --actor
and --reason and creates only the new WB candidate and Work Log.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			identity, releaseAdmission, err := requireMutationAdmission(command, apply)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			if strings.TrimSpace(model) == "" {
				model = identity.Model
			}
			if strings.TrimSpace(runtime) == "" {
				runtime = identity.Runtime
			}
			if strings.TrimSpace(agentID) == "" {
				agentID = identity.AgentID
			}
			repair, err := orchestrate.PreparePublishedValidationFailureForwardRepair(command.Context(), orchestrate.WorktreeMergePublishedForwardRepairOptions{
				ProjectsRoot: projectsRoot, Receipt: args[0], Sources: args[1:], Apply: apply, Actor: actor, Reason: reason,
				ExpectedReceiptSHA256: receiptSHA, ExpectedImmutableClaimSHA256: claimSHA, ExpectedSupersessionSHA256: supersessionSHA,
				ExpectedCurrentTargetSHA: targetSHA, ExpectedSourceSHAs: expectedSourceSHAs,
				Model: model, AgentRuntime: runtime, AgentID: agentID, Initiator: mutationInitiator(command), CLI: cli, Provider: provider,
				SessionRequired: identity.Registered, Timeout: timeout, Retry: retry,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(repair)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "status: %s\nfailed-receipt: %s\ncandidate: %s\ncurrent-target: %s\n", repair.Status, repair.ReceiptPath, repair.Candidate.Worktree, repair.CurrentTargetSHA)
			if !apply {
				_, _ = fmt.Fprintln(command.OutOrStdout(), "dry-run only, pass --apply to create the distinct forward-repair candidate")
			}
			return err
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "create the distinct WB-managed forward-repair candidate")
	command.Flags().StringVar(&actor, "actor", "", "required with --apply: trusted operator or agent identity")
	command.Flags().StringVar(&reason, "reason", "", "required with --apply: bounded audited recovery reason")
	command.Flags().StringVar(&receiptSHA, "expected-receipt-sha256", "", "required SHA256 of the immutable failed receipt")
	command.Flags().StringVar(&claimSHA, "expected-immutable-claim-sha256", "", "required SHA256 of the immutable failed candidate Work Log claim")
	command.Flags().StringVar(&supersessionSHA, "expected-supersession-sha256", "", "required SHA256 of the historical self-supersession acknowledgement")
	command.Flags().StringVar(&targetSHA, "expected-current-target", "", "required exact current remote target SHA")
	command.Flags().StringSliceVar(&expectedSourceSHAs, "expected-source-sha", nil, "required expected SHA for each current source, in argument order")
	command.Flags().StringVar(&model, "model", "", "model identity recorded in the new candidate Work Log")
	command.Flags().StringVar(&runtime, "agent-runtime", "", "agent runtime recorded in the new candidate Work Log")
	command.Flags().StringVar(&agentID, "agent-id", "", "agent identity recorded in the new candidate Work Log")
	command.Flags().StringVar(&cli, "cli", "wb", "CLI identity recorded in the new candidate Work Log")
	command.Flags().StringVar(&provider, "provider", "", "routing or billing provider identity, never a credential")
	command.Flags().DurationVar(&timeout, "timeout", 8*time.Minute, "bounded Git operation timeout")
	command.Flags().IntVar(&retry, "retry", 0, "retry transient Git command failures")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	addMutationAdmissionFlags(command)
	return command
}

func bindWorktreeMergeFlags(command *cobra.Command, flags *worktreeMergeFlags, prepare, land bool) {
	if prepare {
		command.Flags().StringVar(&flags.target, "target", "", "target branch; defaults to the remote default branch")
		command.Flags().StringVar(&flags.model, "model", "unknown", "model identity recorded in the candidate Work Log")
		command.Flags().StringVar(&flags.runtime, "agent-runtime", "wb", "agent runtime recorded in the candidate Work Log")
		command.Flags().StringVar(&flags.agentID, "agent-id", "", "agent identity recorded in the candidate Work Log")
		command.Flags().StringVar(&flags.cli, "cli", "wb", "CLI identity recorded in the candidate Work Log")
		command.Flags().StringVar(&flags.provider, "provider", "", "routing or billing provider identity, never a credential")
	}
	if land {
		command.Flags().StringVar(&flags.route, "route", "auto", "landing route: auto, direct, pr, or stream (an alias of wb stream absorb)")
		command.Flags().BoolVar(&flags.cleanup, "cleanup", false, "after remote receipt and canonical synchronization, retire absorbed managed assets")
		command.Flags().StringVar(&flags.onFailure, "on-failure", "stop", "post-landing failure action: stop or prepare a forward revert")
		command.Flags().DurationVar(&flags.interval, "check-interval", orchestrate.DefaultCheckPollInterval, "foreground interval between exact GitHub check observations (a checks-bearing terminal set's confirming reread waits at most 15s)")
	}
	command.Flags().DurationVar(&flags.timeout, "timeout", 8*time.Minute, "bounded command and check wait duration")
	command.Flags().IntVar(&flags.retry, "retry", 0, "retry transient command failures")
	command.Flags().StringVar(&flags.format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&flags.progress, "progress", false, "show progress on stderr even when it is not a terminal")
}

func validateWorktreeMergeFlags(flags worktreeMergeFlags) error {
	if err := requireOutputFormat(flags.format, "text", "json"); err != nil {
		return err
	}
	switch orchestrate.WorktreeMergeRoute(flags.route) {
	case "", orchestrate.WorktreeMergeRouteAuto, orchestrate.WorktreeMergeRouteDirect, orchestrate.WorktreeMergeRoutePullRequest:
	default:
		return fmt.Errorf("unsupported --route %q; use auto, direct, or pr", flags.route)
	}
	if flags.onFailure != "" && flags.onFailure != "stop" && flags.onFailure != "revert" {
		return fmt.Errorf("unsupported --on-failure %q; use stop or revert", flags.onFailure)
	}
	if flags.stopBeforeMerge && orchestrate.WorktreeMergeRoute(flags.route) != orchestrate.WorktreeMergeRoutePullRequest {
		return fmt.Errorf("--stop-before-merge requires --route pr")
	}
	if flags.stopBeforeMerge && flags.cleanup {
		return fmt.Errorf("--stop-before-merge cannot be combined with --cleanup")
	}
	if flags.retry < 0 || flags.timeout <= 0 {
		return fmt.Errorf("--timeout must be positive and --retry must not be negative")
	}
	return nil
}

func prepareMergeOptions(flags worktreeMergeFlags, sources []string, reporter progress.Reporter) orchestrate.WorktreeMergePrepareOptions {
	return orchestrate.WorktreeMergePrepareOptions{ProjectsRoot: projectsRoot, Sources: sources, Target: flags.target,
		Model: flags.model, AgentRuntime: flags.runtime, AgentID: flags.agentID, CLI: flags.cli, Provider: flags.provider,
		Timeout: flags.timeout, Retry: flags.retry, Progress: reporter, ProgressRequested: flags.progress, RebatchReceipt: flags.rebatchReceipt}
}

func landMergeOptions(flags worktreeMergeFlags, receipt string, reporter progress.Reporter) orchestrate.WorktreeMergeLandOptions {
	return orchestrate.WorktreeMergeLandOptions{ProjectsRoot: projectsRoot, Receipt: receipt,
		Route: orchestrate.WorktreeMergeRoute(flags.route), Cleanup: flags.cleanup, OnFailure: flags.onFailure,
		Timeout: flags.timeout, Retry: flags.retry, CheckPollInterval: flags.interval, Progress: reporter, ProgressRequested: flags.progress,
		StopBeforeMerge: flags.stopBeforeMerge}
}

func newWorktreeMergeProgress(command *cobra.Command, flags worktreeMergeFlags) *campaignProgress {
	interactive := console.Interactive(command.ErrOrStderr(), nonInteractive)
	out := command.ErrOrStderr()
	heartbeat := time.Second
	if flags.progress && !interactive {
		out = &worktreeMergeLineWriter{out: out}
		heartbeat = 30 * time.Second
	}
	return newCampaignProgressWithHeartbeat(out, flags.progress || interactive, "worktree merge", heartbeat)
}

// worktreeMergeLineWriter turns the live renderer's carriage-return updates
// into newline-delimited diagnostics for non-terminal agent tools. Those tools
// can otherwise buffer a healthy stage until the final newline and recreate
// the silence --progress is meant to remove.
type worktreeMergeLineWriter struct {
	out     io.Writer
	mu      sync.Mutex
	started bool
}

func (writer *worktreeMergeLineWriter) Write(payload []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	text := string(payload)
	if strings.HasPrefix(text, "\r") {
		text = strings.TrimPrefix(text, "\r")
		if writer.started {
			text = "\n" + text
		}
		writer.started = true
	}
	if text == "\n" {
		writer.started = false
	}
	if _, err := io.WriteString(writer.out, text); err != nil {
		return 0, err
	}
	return len(payload), nil
}

func finishWorktreeMergeProgress(campaign *campaignProgress, receipt orchestrate.WorktreeMergeReceipt, err error) {
	if err != nil {
		status := strings.TrimSpace(string(receipt.Status))
		if status == "" {
			status = "failed"
		}
		campaign.finish(status)
		return
	}
	status := strings.TrimSpace(string(receipt.Status))
	if status == "" {
		status = "completed"
	}
	campaign.finish(status)
}

func writeWorktreeMergeReceipt(writer io.Writer, format string, receipt orchestrate.WorktreeMergeReceipt) error {
	if format == "json" {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(receipt)
	}
	_, err := fmt.Fprintf(writer, "status: %s\nrepository: %s\ntarget: %s\ncandidate: %s\nreceipt: %s\nresume: wb %s\n",
		receipt.Status, receipt.Repository, receipt.Target, receipt.Candidate.SHA, receipt.ReceiptPath, strings.Join(receipt.ResumeArgs, " "))
	return err
}
