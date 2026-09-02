// Package fleetsync decides and performs the sync action for a single repo:
// clone or pull an active repo, or — only when the caller explicitly opts
// in — remove an archived one's local clone. It has no TUI or terminal
// output of its own — callers (e.g. the wb sync worker pool) drive it and
// render results.
package fleetsync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sneat-dev/wb/internal/archiveprune"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/gitops"
)

// Status is the outcome fleetsync.Sync took for a single repo.
type Status int

const (
	Cloned Status = iota
	Pulled
	SkippedDirty
	RemovedArchived
	KeptArchived
	AbsentArchived
	NoOp
	Failed
	// SkippedIgnored, EmptyRemote, Diverged and NoUpstream are appended last
	// deliberately: Status is rendered only through String() and never
	// persisted numerically, but appending keeps the existing values stable
	// regardless.
	SkippedIgnored
	EmptyRemote
	// Diverged: the branch and its upstream each hold commits the other
	// lacks, so no fast-forward is possible and sync must not guess a
	// reconciliation.
	Diverged
	// NoUpstream: the checked-out branch tracks nothing, so there is nowhere
	// to pull from.
	NoUpstream
	// Unpushed: the pull succeeded, but the clone holds commits that exist on
	// no remote. Nothing is wrong with the sync; the work has simply never
	// left this machine.
	Unpushed
	// ArchivedUnlandable: an archived clone holding unpushed commits. The
	// remote is read-only, so those commits can never be pushed — unlike
	// KeptArchived, this state cannot resolve itself and needs a decision.
	ArchivedUnlandable
)

func (s Status) String() string {
	switch s {
	case Cloned:
		return "cloned"
	case Pulled:
		return "pulled"
	case SkippedDirty:
		return "skipped (dirty)"
	case RemovedArchived:
		return "removed archived"
	case KeptArchived:
		return "kept archived"
	case AbsentArchived:
		return "archived, absent"
	case NoOp:
		return "noop"
	case Failed:
		return "failed"
	case SkippedIgnored:
		return "skipped (ignored)"
	case EmptyRemote:
		return "empty remote"
	case Diverged:
		return "diverged"
	case NoUpstream:
		return "no upstream"
	case Unpushed:
		return "unpushed commits"
	case ArchivedUnlandable:
		return "archived, holds unpushed commits"
	default:
		return "unknown"
	}
}

// Result is the outcome of syncing one repo.
type Result struct {
	Repo   discover.Repo
	Status Status
	// PullPlanned is true only for a dry-run existing clone. PullAttempted and
	// PullSucceeded describe the real action independently of Status, which may
	// still end as Unpushed after a successful pull. Updated is a successful
	// pull whose checked-out commit moved forward.
	PullPlanned   bool
	PullAttempted bool
	PullSucceeded bool
	Updated       bool
	Detail        gitops.RepoStatus
	// Tracking is filled in only for Diverged and NoUpstream, whose reports
	// are meaningless without the branch names and ahead/behind counts.
	Tracking gitops.TrackingState
	Err      error

	// Archived mirrors repo.Archived, for callers that render results without
	// keeping the original discover.Repo alongside them.
	Archived bool
	// ArchivedNotPruned is true when Archived is true but the caller did not
	// request pruning (Sync's pruneArchived parameter was false): this
	// repository was pulled or left alone exactly like any other clone,
	// never evaluated for deletion. It exists so an archived repository is
	// never silently indistinguishable from an ordinary one in a report —
	// the whole point of making pruning opt-in is defeated if turning it off
	// also makes archived repositories invisible.
	ArchivedNotPruned bool
	// Reason explains, in prose, exactly why an archived repository was or
	// was not eligible for removal when pruning was requested. It is the
	// verbatim explanation from internal/archiveprune.Evaluate — the same
	// safety predicate wb archive clean uses — never a re-derived summary.
	Reason string
	// HeadSHA is the clone's HEAD when Sync finished with it, empty when
	// there was no clone to read (never cloned, or just removed). A report is
	// read and acted on later — sometimes hours later, by an agent that
	// cannot see the fleet — and a remedy like "reset the clone to its
	// upstream" is unrecoverable if the clone has moved since. Recording the
	// commit the finding was made against lets a reader prove the state it
	// describes still holds before mutating anything.
	HeadSHA string
	// ReceiptPath is where the deletion receipt for this clone was written,
	// set only when --prune-archived actually removed (or tried to remove)
	// it. A removal with no path here is a removal with no evidence, which
	// this package refuses to perform.
	ReceiptPath string
}

// PullSummary renders the pull action independently of the final repository
// status. Empty means this result did not concern an existing active clone.
func (r Result) PullSummary() string {
	switch {
	case r.PullPlanned:
		return "planned (dry-run)"
	case r.Updated:
		return "updated from remote"
	case r.PullSucceeded:
		return "already current"
	case r.PullAttempted:
		return "failed"
	default:
		return ""
	}
}

// Sync reconciles a single repo's local clone with its GitHub state: clone
// if missing, pull if present and clean, skip if the working tree is dirty.
// Forks and repos not owned by the authenticated user or their orgs
// (repo.Remote == false) are left untouched (NoOp). Repos marked with
// `wb repo ignore` are left untouched too (SkippedIgnored), including
// archived ones. In dryRun mode no mutation happens; Status still reports
// what would be done.
//
// An archived repository is never deleted unless pruneArchived is true. With
// pruneArchived false — the default for `wb sync` — an archived repository
// with a local clone is pulled exactly like any other clone (its Status may
// be Pulled, SkippedDirty, Unpushed, and so on); one with no local clone yet
// is reported AbsentArchived, since cloning it here would be an unrequested
// behavior change and it carries no risk either way. Every archived result
// still carries Archived and, when not pruning, ArchivedNotPruned, so a
// report can never make an archived repository indistinguishable from an
// ordinary one just because pruning was left off.
//
// With pruneArchived true, an archived repository's local clone is removed
// only when it passes internal/archiveprune.Evaluate — the exact safety
// predicate `wb archive clean` uses (live-confirmed archived status, no
// uncommitted/untracked changes, no stash, no unpushed commits on any
// branch, no local-only branch, no unpushed tag, no linked worktree, no
// non-terminal WB Work Log claim, not marked wb.skip-sync). That predicate is
// called verbatim, not re-derived, so this path and `wb archive clean` can
// never drift into different definitions of "safe to delete".
func Sync(ctx context.Context, repo discover.Repo, projectsRoot string, dryRun, pruneArchived bool) Result {
	return withHeadSHA(classify(ctx, repo, projectsRoot, dryRun, pruneArchived))
}

// withHeadSHA stamps the clone's HEAD onto a finished result. It is read after
// every mutation this run performed, so it is the commit the finding actually
// describes. A clone that does not exist — never cloned, or removed as an
// archived repository — has nothing to read and keeps an empty HeadSHA; a
// failure to read it is not worth failing a sync over, so it is discarded.
func withHeadSHA(res Result) Result {
	if res.Repo.Path == "" || res.Status == RemovedArchived {
		return res
	}
	if sha, err := gitops.HeadSHA(res.Repo.Path); err == nil {
		res.HeadSHA = sha
	}
	return res
}

func classify(ctx context.Context, repo discover.Repo, projectsRoot string, dryRun, pruneArchived bool) Result {
	res := Result{Repo: repo, Archived: repo.Archived}

	if !repo.Remote || repo.IsFork {
		res.Status = NoOp
		return res
	}

	// Checked before the archived branch on purpose: the marker must also
	// protect the clone from archived-repo cleanup. wb never deletes a
	// checkout the user explicitly told it to leave alone. The cost is that a
	// marked-then-archived repo reports as skipped rather than appearing in
	// the archived kept/removed/absent tallies.
	if repo.Path != "" {
		skip, err := gitops.SkipSync(repo.Path)
		if err != nil {
			res.Status = Failed
			res.Err = err
			return res
		}
		if skip {
			res.Status = SkippedIgnored
			return res
		}
	}

	if repo.Archived {
		if !pruneArchived {
			return syncArchivedWithoutPruning(repo, projectsRoot, res, dryRun)
		}
		return syncArchived(ctx, repo, projectsRoot, res, dryRun)
	}
	return syncActive(repo, projectsRoot, res, dryRun)
}

// syncArchivedWithoutPruning is the default path for an archived repository:
// wb sync treats it exactly like an ordinary clone, pulling it if it exists
// and is clean and never deleting it, so an archived repository's local
// clone is retired only on the explicit request pruneArchived represents.
func syncArchivedWithoutPruning(repo discover.Repo, projectsRoot string, res Result, dryRun bool) Result {
	if repo.Path == "" {
		res.Status = AbsentArchived
		return res
	}
	result := syncActive(repo, projectsRoot, res, dryRun)
	result.ArchivedNotPruned = true
	return result
}

// syncArchived is reached only when the caller passed pruneArchived. The
// deletion decision comes entirely from archiveprune.Evaluate; everything
// below it is cosmetic classification of an ineligible result and never
// changes whether the clone is removed.
func syncArchived(ctx context.Context, repo discover.Repo, projectsRoot string, res Result, dryRun bool) Result {
	if repo.Path == "" {
		res.Status = AbsentArchived
		return res
	}
	evaluation := archiveprune.Evaluate(ctx, projectsRoot, repo)
	res.Reason = evaluation.Reason
	if !evaluation.Eligible {
		// This classification is cosmetic only: it distinguishes "commits
		// that can never be pushed because the remote is read-only"
		// (ArchivedUnlandable, a standing reminder that never clears on its
		// own) from every other refusal reason, without affecting the
		// safety decision above in any way.
		if status, err := gitops.Status(repo.Path); err == nil {
			res.Detail = status
			if len(status.Unpushed) > 0 {
				res.Status = ArchivedUnlandable
				return res
			}
		}
		res.Status = KeptArchived
		return res
	}
	if dryRun {
		res.Status = RemovedArchived
		return res
	}

	// The commit is read before the clone is destroyed, because afterwards
	// there is nothing left to ask. The repository still exists on GitHub —
	// archived and read-only, but intact — so slug plus SHA is what makes the
	// deletion undoable.
	head, _ := gitops.HeadSHA(repo.Path)
	receipt := RemovalReceipt{
		SchemaVersion: removalReceiptSchemaVersion,
		Phase:         PhasePlanned,
		Repository:    repo.Slug(),
		ClonePath:     repo.Path,
		HeadSHA:       head,
		Reason:        evaluation.Reason,
		CreatedAt:     time.Now().UTC(),
	}
	receiptPath, err := writeRemovalReceipt(projectsRoot, receipt)
	if err != nil {
		// Refusing to delete is the whole point. A clone removed without a
		// receipt is removed without a record, and this is the one WB
		// operation that cannot be undone from local state — so an
		// unwritable receipt blocks the deletion rather than being warned
		// about and stepped over.
		res.Status = Failed
		res.Err = fmt.Errorf("refusing to remove %s: its deletion receipt could not be written: %w", repo.Slug(), err)
		res.HeadSHA = head
		return res
	}
	res.ReceiptPath = receiptPath

	if err := os.RemoveAll(repo.Path); err != nil {
		receipt.Phase = PhaseFailed
		receipt.Error = err.Error()
		_ = overwriteRemovalReceipt(receiptPath, receipt)
		res.Status = Failed
		res.Err = err
		res.HeadSHA = head
		return res
	}
	removedAt := time.Now().UTC()
	receipt.Phase = PhaseRemoved
	receipt.RemovedAt = &removedAt
	// The clone is already gone; a failed phase update leaves the receipt at
	// "planned", which still records what was removed and from where.
	_ = overwriteRemovalReceipt(receiptPath, receipt)
	res.Status = RemovedArchived
	res.HeadSHA = head
	return res
}

func syncActive(repo discover.Repo, projectsRoot string, res Result, dryRun bool) Result {
	if repo.Path == "" {
		if dryRun {
			res.Status = Cloned
			return res
		}
		dest := filepath.Join(projectsRoot, repo.Org, repo.Name)
		if err := gitops.Clone(repo.CloneURL, dest); err != nil {
			res.Status = Failed
			res.Err = err
			return res
		}
		res.Repo.Path = dest
		res.Status = Cloned
		return res
	}

	status, err := gitops.Status(repo.Path)
	if err != nil {
		res.Status = Failed
		res.Err = err
		return res
	}
	if status.WorkingTreeDirty() {
		res.Status = SkippedDirty
		res.Detail = status
		return res
	}
	if dryRun {
		// A detached clone cannot be pulled. Apply mode discovers this when
		// git pull fails and the tracking probe classifies the refusal as
		// NoUpstream; preview must make the same decision without attempting a
		// mutating command.
		if track, trackErr := gitops.Tracking(repo.Path); trackErr == nil && track.Branch == "" {
			res.Status = NoUpstream
			res.Detail = status
			res.Tracking = track
			return res
		}
		res.Status = Pulled
		res.PullPlanned = true
		return res
	}
	beforeHead, beforeHeadErr := gitops.HeadSHA(repo.Path)
	res.PullAttempted = true
	if err := gitops.Pull(repo.Path); err != nil {
		// A remote publishing no branches at all has nothing to pull, so the
		// failure is expected rather than a fault: the repository was created
		// on GitHub and never pushed to. Reporting it as an error would make
		// every sync red until someone pushes.
		//
		// Probed only after a pull has already failed, so the extra round trip
		// costs nothing on the normal path. A remote that does have branches,
		// just not the tracked one, stays an error — that is a renamed or
		// deleted branch and needs a human.
		if hasBranches, probeErr := gitops.RemoteHasBranches(repo.Path); probeErr == nil && !hasBranches {
			res.Status = EmptyRemote
			return res
		}
		// The other two expected refusals. A divergence and a branch that
		// tracks nothing are states the fleet owner has to decide about, not
		// faults sync can fix, and neither should turn a whole run red or
		// bury the real failures under git's multi-line reconciliation hint.
		// Read after the failed pull, which has already fetched, so the
		// counts are current and cost no extra round trip.
		//
		// A branch that IS configured to track a ref the remote no longer
		// publishes is deliberately not absorbed here: that is a renamed or
		// deleted branch, and it keeps failing loudly.
		if track, probeErr := gitops.Tracking(repo.Path); probeErr == nil {
			switch {
			case track.Diverged():
				res.Status = Diverged
				res.Tracking = track
				return res
			case !track.Configured:
				res.Status = NoUpstream
				res.Tracking = track
				return res
			}
		}
		res.Status = Failed
		res.Err = err
		return res
	}
	res.PullSucceeded = true
	if afterHead, afterHeadErr := gitops.HeadSHA(repo.Path); beforeHeadErr == nil && afterHeadErr == nil && afterHead != beforeHead {
		res.Updated = true
	}
	// A successful pull says the clone is not BEHIND. It says nothing about
	// what the clone is holding that origin has never seen: an ahead-only
	// branch pulls cleanly and reports "Already up to date", so unlanded work
	// used to be indistinguishable from a clean sync. strongo/slices hid a
	// commit that way for five weeks; its sibling strongo/gamp was only
	// noticed because origin happened to move underneath it.
	//
	// Re-read rather than reusing the pre-pull Status: the pull may have just
	// landed some of those commits from elsewhere. One local git command.
	if unpushed, unpushedBranches, err := gitops.UnpushedWork(repo.Path); err == nil && len(unpushed) > 0 {
		res.Status = Unpushed
		res.Detail.Unpushed = unpushed
		res.Detail.UnpushedBranches = unpushedBranches
		return res
	}
	res.Status = Pulled
	return res
}
