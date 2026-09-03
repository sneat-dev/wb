package streamabsorb

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/streamsync"
)

// Commit is one source commit on the agent branch.
type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	PatchID string `json:"patch_id,omitempty"`
	// Files are the paths it touches, used to decide whether the change is a
	// mechanical bump.
	Files []string `json:"files,omitempty"`
}

// Short renders the abbreviated SHA used in the aggregated message.
func (commit Commit) Short() string {
	if len(commit.SHA) <= 7 {
		return commit.SHA
	}
	return commit.SHA[:7]
}

// Git is the local Git surface absorb needs beyond the sync engine's.
type Git interface {
	streamsync.Git
	// CommitsNotIn lists the commits on branch whose patch the upstream does
	// not already carry, each with its stable patch id.
	CommitsNotIn(ctx context.Context, dir, branch, upstream string) ([]Commit, error)
	// SquashOnto collapses branch into ONE commit on top of upstream and
	// returns its SHA. It must not create a merge commit.
	SquashOnto(ctx context.Context, dir, branch, upstream, message string) (string, error)
	// BuildCheck proves one commit compiles, for --keep-commits.
	BuildCheck(ctx context.Context, dir, sha string) error
}

// Options is one `wb stream absorb` invocation.
type Options struct {
	Stream string
	// AgentWorktree is the checkout holding the work.
	AgentWorktree string
	// AgentBranch is the branch being absorbed.
	AgentBranch string
	// StreamWorktree and StreamBranch are what it is absorbed into.
	StreamWorktree string
	StreamBranch   string
	Repository     string
	// Title and Summary form the aggregated squash message.
	Title   string
	Summary string
	// KeepCommits names source commits to preserve instead of squashing.
	// Reason is mandatory with it.
	KeepCommits []string
	Reason      string
	// Verify runs the batch verification over the absorbed result.
	Verify  bool
	Timeout time.Duration
}

// Result is the absorb report.
type Result struct {
	Stream     string `json:"stream"`
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	// PatchSet is what was absorbed, and what the approval was keyed on.
	PatchSet PatchSet `json:"patch_set"`
	// Approval is the record that cleared it, absent for a mechanical bump.
	Approval *Record `json:"approval,omitempty"`
	// Mechanical is true when the diff touches only dependency manifests and
	// lockfiles, which skips the ledger exactly as it does at `pr land`.
	Mechanical bool `json:"mechanical,omitempty"`
	// Commit is the single squashed commit, or empty under --keep-commits.
	Commit  string   `json:"commit,omitempty"`
	Kept    []string `json:"kept,omitempty"`
	Message string   `json:"message,omitempty"`
	// Batch is the verification over the absorbed result.
	Batch *streamsync.BatchResult `json:"batch,omitempty"`
	// Pushed is always false. Absorb never pushes; it is recorded so a caller
	// reading the report cannot conclude otherwise.
	Pushed bool     `json:"pushed"`
	Errors []string `json:"errors,omitempty"`
}

// Failed reports whether the absorb needs attention.
func (result Result) Failed() bool {
	if len(result.Errors) > 0 {
		return true
	}
	return result.Batch != nil && !result.Batch.Passed
}

// Refusal codes are contract.
const (
	// RefusalUnapprovedPatchSet fires when no APPROVE exists for exactly the
	// patch set about to be absorbed.
	RefusalUnapprovedPatchSet = "unapproved-patch-set"
	// RefusalKeepWithoutReason fires when --keep-commits carries no reason.
	RefusalKeepWithoutReason = "keep-without-reason"
	// RefusalNothingToAbsorb fires when the branch carries no new patch.
	RefusalNothingToAbsorb = "nothing-to-absorb"
	// RefusalKeptCommitDoesNotBuild fires when a kept commit breaks the build.
	RefusalKeptCommitDoesNotBuild = "kept-commit-does-not-build"
)

// Refusal is a guard that fired.
type Refusal struct {
	Code       string
	Message    string
	Sanctioned []string
}

func (refusal *Refusal) Error() string {
	if len(refusal.Sanctioned) == 0 {
		return refusal.Message
	}
	return refusal.Message + "; run: " + strings.Join(refusal.Sanctioned, " or ")
}

// Engine performs absorb against injected ports.
type Engine struct {
	Git    Git
	Ledger Ledger
	// Sync supplies the shared batch verifier. Absorb composes it rather than
	// re-implementing prefix re-apply.
	Sync   *streamsync.Engine
	Events streamsync.Events
}

// Absorb rebases the agent branch onto the stream branch and squashes it to one
// commit — locally, never pushing, never opening or touching a pull request.
//
// The approval gate comes FIRST and is keyed on the patch-identity set, not on
// a SHA. A content-identical rebase carries the approval forward because the
// set is unchanged; any content change invalidates it and needs a new round.
func (engine *Engine) Absorb(ctx context.Context, options Options) (Result, error) {
	result := Result{Stream: options.Stream, Repository: options.Repository, Branch: options.AgentBranch}

	if len(options.KeepCommits) > 0 && strings.TrimSpace(options.Reason) == "" {
		return result, &Refusal{
			Code: RefusalKeepWithoutReason,
			Message: "--keep-commits requires --reason: keeping several commits is a deliberate exception to " +
				"one-commit-per-reviewed-change, so the history has to say why",
			Sanctioned: []string{`wb stream absorb <worktree> --keep-commits <sha,...> --reason "<text>"`},
		}
	}
	if clean, err := engine.Git.IsClean(ctx, options.AgentWorktree); err != nil {
		return result, err
	} else if !clean {
		return result, &Refusal{
			Code:       "dirty-worktree",
			Message:    options.AgentWorktree + " has uncommitted changes; absorb rebases and squashes, so it will not run over them",
			Sanctioned: []string{"commit the work, then re-run absorb"},
		}
	}

	commits, err := engine.Git.CommitsNotIn(ctx, options.AgentWorktree, options.AgentBranch, options.StreamBranch)
	if err != nil {
		return result, err
	}
	if len(commits) == 0 {
		return result, &Refusal{
			Code:       RefusalNothingToAbsorb,
			Message:    options.AgentBranch + " carries no commit the stream branch does not already have",
			Sanctioned: []string{"wb stream status " + options.Stream},
		}
	}
	head, err := engine.Git.Head(ctx, options.AgentWorktree, options.AgentBranch)
	if err != nil {
		return result, err
	}
	patchIDs := make([]string, 0, len(commits))
	for _, commit := range commits {
		patchIDs = append(patchIDs, commit.PatchID)
	}
	result.PatchSet = NewPatchSet(head, patchIDs)

	// A mechanical bump skips the ledger exactly as it does at `pr land`: a
	// diff touching only dependency manifests and lockfiles carries no
	// judgment to review.
	result.Mechanical = IsMechanical(commits)
	if !result.Mechanical {
		record, found, err := engine.Ledger.Approval(options.Stream, result.PatchSet.Fingerprint())
		if err != nil {
			return result, err
		}
		if !found || !Approved(record) {
			return result, engine.unapproved(options, result.PatchSet, record, found)
		}
		result.Approval = &record
	}

	// Rebase, then squash. Never a merge.
	conflicts, err := engine.Git.Rebase(ctx, options.AgentWorktree, options.AgentBranch, options.StreamBranch)
	if err != nil {
		_ = engine.Git.AbortRebase(ctx, options.AgentWorktree)
		return result, fmt.Errorf("rebase %s onto %s: %w", options.AgentBranch, options.StreamBranch, err)
	}
	if len(conflicts) > 0 {
		_ = engine.Git.AbortRebase(ctx, options.AgentWorktree)
		result.Errors = append(result.Errors,
			"conflicts with "+options.StreamBranch+": "+strings.Join(conflicts, ", ")+"; resolve them on the agent branch and re-run absorb")
		return result, nil
	}

	if len(options.KeepCommits) > 0 {
		// Each kept commit has to build on its own: the point of keeping them
		// is that the history stays bisectable, and a commit that does not
		// compile makes it worse than one squashed commit would have been.
		for _, sha := range options.KeepCommits {
			if err := engine.Git.BuildCheck(ctx, options.AgentWorktree, sha); err != nil {
				return result, &Refusal{
					Code: RefusalKeptCommitDoesNotBuild,
					Message: fmt.Sprintf("kept commit %s does not build, so keeping it would make the history less bisectable than squashing: %v",
						sha, err),
					Sanctioned: []string{
						"wb stream absorb " + options.AgentWorktree + " (squash to one commit)",
						"fix the commit and re-run",
					},
				}
			}
		}
		result.Kept = options.KeepCommits
		engine.record(options, "absorb", "success", "kept "+fmt.Sprint(len(options.KeepCommits))+" commit(s): "+options.Reason, map[string]string{
			"reason": options.Reason, "kept": strings.Join(options.KeepCommits, ","),
		})
	} else {
		message := AggregateMessage(options.Title, options.Summary, commits, result.PatchSet.Fingerprint())
		result.Message = message
		sha, err := engine.Git.SquashOnto(ctx, options.AgentWorktree, options.AgentBranch, options.StreamBranch, message)
		if err != nil {
			return result, err
		}
		result.Commit = sha
		engine.record(options, "absorb", "success", "squashed to one commit", map[string]string{
			"commit": sha, "sources": fmt.Sprint(len(commits)), "fingerprint": result.PatchSet.Fingerprint(),
		})
	}

	if options.Verify && engine.Sync != nil {
		elements := []streamsync.Element{{
			Name: options.AgentBranch, SHA: result.Commit,
			Description: "absorbed " + options.AgentBranch,
		}}
		batch, err := engine.Sync.VerifyBatch(ctx, streamsync.Options{
			Stream: options.Stream, Worktree: options.StreamWorktree,
			Repository: options.Repository, Branch: options.StreamBranch, Timeout: options.Timeout,
		}, elements)
		if err != nil {
			return result, err
		}
		result.Batch = &batch
	}
	// Absorb never pushes; the stream's own landing does, on its trigger.
	result.Pushed = false
	return result, nil
}

func (engine *Engine) unapproved(options Options, set PatchSet, record Record, found bool) error {
	detail := "no review has been recorded for it"
	if found {
		detail = fmt.Sprintf("the newest record for it is %s (round %d)", record.Verdict, record.Round)
	}
	return &Refusal{
		Code: RefusalUnapprovedPatchSet,
		Message: fmt.Sprintf(
			"%s carries %d commit(s) with no APPROVE for this exact patch set (%s…): %s. "+
				"A review below the stream hangs on the content, so a content change needs its own round",
			options.AgentBranch, len(set.IDs), shortFingerprint(set.Fingerprint()), detail),
		Sanctioned: []string{
			"wb review request " + options.AgentWorktree,
			`wb review record --worktree ` + options.AgentWorktree + ` --verdict APPROVE --round 1`,
		},
	}
}

func shortFingerprint(fingerprint string) string {
	if len(fingerprint) <= 12 {
		return fingerprint
	}
	return fingerprint[:12]
}

func (engine *Engine) record(options Options, phase, outcome, detail string, evidence map[string]string) {
	if engine.Events == nil {
		return
	}
	_ = engine.Events.Append(streamsync.Event{
		Stream: options.Stream, Verb: "stream absorb", Phase: phase,
		Repository: options.Repository, Outcome: outcome, Detail: detail, Evidence: evidence,
	})
}
