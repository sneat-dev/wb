// Package fleetsync decides and performs the sync action for a single repo:
// clone or pull an active repo, or remove/keep an archived one's local
// clone. It has no TUI or terminal output of its own — callers (e.g. the
// wb sync worker pool) drive it and render results.
package fleetsync

import (
	"os"
	"path/filepath"

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
	Detail gitops.RepoStatus
	// Tracking is filled in only for Diverged and NoUpstream, whose reports
	// are meaningless without the branch names and ahead/behind counts.
	Tracking gitops.TrackingState
	Err      error
}

// Sync reconciles a single repo's local clone with its GitHub state: clone
// if missing, pull if present and clean, skip if the working tree is dirty.
// For archived repos: remove the local clone if it is safe to (clean, no
// stash, nothing unpushed), otherwise keep it and report why. Forks and
// repos not owned by the authenticated user or their orgs (repo.Remote ==
// false) are left untouched (NoOp). Repos marked with `wb repo ignore` are
// left untouched too (SkippedIgnored), including archived ones. In dryRun
// mode no mutation happens; Status still reports what would be done.
func Sync(repo discover.Repo, projectsRoot string, dryRun bool) Result {
	res := Result{Repo: repo}

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
		return syncArchived(repo, res, dryRun)
	}
	return syncActive(repo, projectsRoot, res, dryRun)
}

func syncArchived(repo discover.Repo, res Result, dryRun bool) Result {
	if repo.Path == "" {
		res.Status = AbsentArchived
		return res
	}
	status, err := gitops.Status(repo.Path)
	if err != nil {
		res.Status = Failed
		res.Err = err
		return res
	}
	unpushed, unpushedErr := gitops.UnpushedCommits(repo.Path)
	if unpushedErr == nil && len(unpushed) > 0 {
		// The remote is read-only, so these commits can never be pushed. That
		// makes this the one "kept" reason that will never clear on its own:
		// every future sync reports it again, unchanged, until someone either
		// discards the commits or unarchives the repo. Naming it separately is
		// the difference between a standing reminder and a decision nobody
		// knows is outstanding.
		res.Status = ArchivedUnlandable
		res.Detail = status
		res.Detail.Unpushed = unpushed
		return res
	}
	if status.Dirty() {
		res.Status = KeptArchived
		res.Detail = status
		return res
	}
	if dryRun {
		res.Status = RemovedArchived
		return res
	}
	if err := os.RemoveAll(repo.Path); err != nil {
		res.Status = Failed
		res.Err = err
		return res
	}
	res.Status = RemovedArchived
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
		res.Status = Pulled
		return res
	}
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
	// A successful pull says the clone is not BEHIND. It says nothing about
	// what the clone is holding that origin has never seen: an ahead-only
	// branch pulls cleanly and reports "Already up to date", so unlanded work
	// used to be indistinguishable from a clean sync. strongo/slices hid a
	// commit that way for five weeks; its sibling strongo/gamp was only
	// noticed because origin happened to move underneath it.
	//
	// Re-read rather than reusing the pre-pull Status: the pull may have just
	// landed some of those commits from elsewhere. One local git command.
	if unpushed, err := gitops.UnpushedCommits(repo.Path); err == nil && len(unpushed) > 0 {
		res.Status = Unpushed
		res.Detail.Unpushed = unpushed
		return res
	}
	res.Status = Pulled
	return res
}
