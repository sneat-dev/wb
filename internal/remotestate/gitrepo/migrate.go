package gitrepo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/repopath"
)

// migrateLegacyClone moves the remote store clone from a legacy location —
// <projects-root>/<owner>/<name> or <projects-root>/github.com/<owner>/<name>,
// where fleet tools mistook it for a canonical source clone (sneat-dev/wb#899)
// — to Options.ClonePath inside WB's private state directory
// (sneat-dev/wb#901). It runs once, from ensureClone, while the caller holds
// the clone lock for ClonePath; a second process waiting on that lock then
// finds the clone already in place and does nothing.
//
// A move keeps everything the legacy clone had: unpushed claim commits, and any
// staged-but-uncommitted wb paths, which Fetch's heal then clears exactly as it
// would have in place. The legacy path is gone afterwards, so nothing is left
// for fleet tools to see and nothing needs retiring. When the clone cannot be
// moved — it has linked worktrees, which a move would orphan, or the rename
// fails — it is copied with a local clone instead (so unpushed commits still
// carry over and push on the next write), and the legacy clone is left in place
// untouched with a one-line notice: WB never deletes a directory that may hold
// someone's work. If even the copy fails, ensureClone clones fresh.
func (p *Provider) migrateLegacyClone() error {
	if len(p.opts.LegacyClonePaths) == 0 || repopath.IsClone(p.opts.ClonePath) {
		return nil
	}
	for _, legacy := range p.opts.LegacyClonePaths {
		if !repopath.IsClone(legacy) {
			continue
		}
		if origin, err := gitops.ConfiguredOriginURL(legacy); err != nil || !sameRemote(origin, p.opts.CloneURL) {
			// Some other repository happens to sit at that path: not ours to move.
			continue
		}
		return p.moveLegacyClone(legacy)
	}
	return nil
}

// moveLegacyClone relocates one verified legacy clone. ClonePath's parent
// directory exists: the caller's clone lock file was created in it. It also takes the
// legacy path's own clone lock, which is what an older WB binary still running
// on this machine locks, so the move never happens under such a process.
func (p *Provider) moveLegacyClone(legacy string) error {
	lock, err := acquireCloneLock(legacy, p.opts.Now, p.opts.Sleep)
	if err != nil {
		return err
	}
	defer func() { _ = lock.release() }()
	reason := ""
	if linkedWorktrees(legacy) {
		reason = "it has linked worktrees"
	} else if err := p.rename(legacy, p.opts.ClonePath); err != nil {
		reason = err.Error()
	} else {
		p.afterMove(legacy)
		p.opts.Notify(fmt.Sprintf("wb: moved the remote store clone from %s to %s (sneat-dev/wb#901)", legacy, p.opts.ClonePath))
		return nil
	}
	if err := p.clone(legacy, p.opts.ClonePath); err != nil {
		p.opts.Notify(fmt.Sprintf("wb: could not move or copy the remote store clone %s (%s); cloning it fresh into %s. Push or copy any unpushed commits from %s, then retire it (sneat-dev/wb#901)", legacy, reason, p.opts.ClonePath, legacy))
		return nil
	}
	if _, err := gitops.GitConfig(p.opts.ClonePath, "remote.origin.url", p.opts.CloneURL); err != nil {
		// A copy still tracking the legacy path would be refused by every later
		// ensureClone. Remove it — it holds nothing the legacy clone lacks — so
		// the next run retries the migration from scratch.
		_ = os.RemoveAll(p.opts.ClonePath)
		return fmt.Errorf("point the copied remote store clone at %s: %w", p.opts.CloneURL, err)
	}
	p.opts.Notify(fmt.Sprintf("wb: copied the remote store clone from %s to %s; %s was left in place because %s and is no longer used: retire it once `git -C %s status` shows nothing you need. Local-only git config there (user.*, url.*.insteadOf, credential helpers) was not copied (sneat-dev/wb#901)", legacy, p.opts.ClonePath, legacy, reason, legacy))
	return nil
}

// afterMove drops what only made sense at the canonical-clone location: the
// generated .worktree.md (it calls the checkout a canonical clone at the old
// path), and a core.hooksPath that points into the old path or at a directory
// that no longer exists — the fleet's managed hooks, which wb hooks install
// put there only because the clone looked like a canonical clone. A
// hooksPath spelled through an unresolved symlink or in another case still
// fails the lexical check, so a missing directory is dropped as well.
// Both are best effort: neither affects what the store holds.
func (p *Provider) afterMove(legacy string) {
	_ = os.Remove(filepath.Join(p.opts.ClonePath, ".worktree.md"))
	hooks, err := gitops.GitConfig(p.opts.ClonePath, "--get", "core.hooksPath")
	if err == nil && (withinPath(hooks, legacy) || !dirExists(hooks)) {
		_, _ = gitops.GitConfig(p.opts.ClonePath, "--unset", "core.hooksPath")
	}
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// linkedWorktrees reports whether the clone at path has linked worktrees,
// whose .git files point into path/.git and would break if path moved.
func linkedWorktrees(path string) bool {
	entries, _ := os.ReadDir(filepath.Join(path, ".git", "worktrees"))
	return len(entries) > 0
}

// withinPath reports whether path is root or lies beneath it.
func withinPath(path, root string) bool {
	path, root = filepath.Clean(path), filepath.Clean(root)
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}
