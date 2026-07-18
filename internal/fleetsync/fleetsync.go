// Package fleetsync decides and performs the sync action for a single repo:
// clone or pull an active repo, or remove/keep an archived one's local
// clone. It has no TUI or terminal output of its own — callers (e.g. the
// wb sync worker pool) drive it and render results.
package fleetsync

import (
	"os"
	"path/filepath"

	"github.com/trakhimenok/workbench/wb/internal/discover"
	"github.com/trakhimenok/workbench/wb/internal/gitops"
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
	default:
		return "unknown"
	}
}

// Result is the outcome of syncing one repo.
type Result struct {
	Repo   discover.Repo
	Status Status
	Detail gitops.RepoStatus
	Err    error
}

// Sync reconciles a single repo's local clone with its GitHub state: clone
// if missing, pull if present and clean, skip if the working tree is dirty.
// For archived repos: remove the local clone if it is safe to (clean, no
// stash, nothing unpushed), otherwise keep it and report why. Forks and
// repos not owned by the authenticated user or their orgs (repo.Remote ==
// false) are left untouched (NoOp). In dryRun mode no mutation happens;
// Status still reports what would be done.
func Sync(repo discover.Repo, projectsRoot string, dryRun bool) Result {
	res := Result{Repo: repo}

	if !repo.Remote || repo.IsFork {
		res.Status = NoOp
		return res
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
		res.Status = Failed
		res.Err = err
		return res
	}
	res.Status = Pulled
	return res
}
