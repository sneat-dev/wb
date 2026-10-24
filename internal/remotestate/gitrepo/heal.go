package gitrepo

import (
	"fmt"
	"strings"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/syncreport"
)

// ownedPrefixes are the store paths this provider writes itself: claims,
// machine snapshots, the README it seeds, and the sync-report collection.
// An uncommitted change under one of them can only be WB's own write that
// never made it into a commit, so discarding it loses nothing the store
// cannot re-derive — the next claim/publish (and the local outbox replay)
// writes it again.
var ownedPrefixes = []string{"claims/", "machines/", "README.md", ".ingitdb/", syncreport.CollectionPath + "/"}

func ownedPath(path string) bool {
	for _, prefix := range ownedPrefixes {
		if path == prefix || (strings.HasSuffix(prefix, "/") && strings.HasPrefix(path, prefix)) {
			return true
		}
	}
	return false
}

// healClone makes the clone safe to rebase before every sync
// (sneat-dev/wb#899). Staged or modified files confined to owned paths — the
// residue of a publish that wrote or staged files but never committed them —
// are returned to HEAD (a staged new file is removed). A staged or modified
// file anywhere else is someone's work: it is never touched, and the sync is
// refused with an error naming the paths and the remedy. Untracked files are
// left alone and do not block the sync, exactly as before. The caller holds
// the clone lock.
func (p *Provider) healClone() error {
	changed, err := gitops.ChangedPaths(p.opts.ClonePath)
	if err != nil {
		return p.syncError(err)
	}
	var owned, foreign []string
	for _, entry := range changed {
		switch {
		case entry.Untracked():
			// Never blocks a rebase on its own and is left alone, as before.
		case ownedPath(entry.Path):
			owned = append(owned, entry.Path)
		default:
			foreign = append(foreign, entry.Path)
		}
	}
	if len(foreign) > 0 {
		return &remotestate.SyncError{
			Store:  p.opts.ClonePath,
			Err:    fmt.Errorf("uncommitted changes outside the paths wb writes: %s", strings.Join(foreign, ", ")),
			Remedy: fmt.Sprintf("wb will not discard them; inspect them with `git -C %s status`, commit or move them aside, then retry", p.opts.ClonePath),
		}
	}
	if len(owned) == 0 {
		return nil
	}
	if err := gitops.DiscardPaths(p.opts.ClonePath, owned...); err != nil {
		return p.syncError(fmt.Errorf("discard uncommitted wb-owned files %s: %w", strings.Join(owned, ", "), err))
	}
	return nil
}

// syncError wraps a failure to refresh the clone with the generic remedy.
func (p *Provider) syncError(err error) error {
	return &remotestate.SyncError{
		Store:  p.opts.ClonePath,
		Err:    err,
		Remedy: fmt.Sprintf("this machine's claims and snapshots are not reaching other machines; retry with `wb remote status`, and if it keeps failing inspect `git -C %s status`", p.opts.ClonePath),
	}
}
