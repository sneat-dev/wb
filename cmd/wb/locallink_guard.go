package main

import (
	"github.com/sneat-dev/wb/internal/locallink"
	"github.com/sneat-dev/wb/internal/streams"
)

// refuseLinkedWorktrees is the landing guard every push-or-land verb calls
// before it does anything.
//
// A worktree with a live local link builds against an *unpublished* working
// tree. Pushing or landing it publishes a commit whose CI has already run
// against something the registry never carried, so the guard fires before any
// push and names both the offending link and the command that clears it.
//
// The two signals are independent by construction: stream state would miss a
// hand-written `go.work`, and `go.work` would miss an npm link. Both are
// consulted, and either one refuses.
//
// A state store that cannot be read is an error, never an empty result — "I
// could not tell" must not be spelled the same way as "there is no link".
//
// This is the hook the landing verbs share. `wb pr land` calls it exactly as
// `wb worktree merge` does.
//
// Implements: dependency-streams#req:merge-refuses-a-linked-worktree.
func refuseLinkedWorktrees(worktrees []string) error {
	store, err := streams.Open(projectsRoot)
	if err != nil {
		return err
	}
	for _, worktree := range worktrees {
		links, err := locallink.HasLiveLink(store, worktree)
		if err != nil {
			return err
		}
		if len(links) == 0 {
			continue
		}
		return &exitError{code: exitUsage, message: locallink.RefusalMessage(worktree, links)}
	}
	return nil
}
