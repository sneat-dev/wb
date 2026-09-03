package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sneat-dev/wb/internal/locallink"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
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
// This is the hook every landing verb shares — `merge`, `merge prepare`,
// `merge land` and `merge resume`. The land verbs take a RECEIPT rather than a
// worktree path, so they resolve the sources from it and guard those; without
// that, preparing before linking and then landing the receipt pushed a linked
// worktree straight past the guard.
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

// refuseLinkedReceiptWorktrees is the land/resume entry point.
//
// `merge-refuses-a-linked-worktree` says merge must refuse to "push or land",
// and the land verbs are the ones that actually push. They are addressed by a
// receipt, so the worktrees to guard are read out of it. A receipt WB cannot
// read is not a reason to skip the guard — it is a reason to say so and stop.
func refuseLinkedReceiptWorktrees(receiptPath string) error {
	worktrees, err := worktreeMergeReceiptWorktrees(receiptPath)
	if err != nil {
		// A path that is a worktree rather than a receipt is the documented
		// second form of the argument; guard it directly.
		if info, statErr := os.Stat(receiptPath); statErr == nil && info.IsDir() {
			return refuseLinkedWorktrees([]string{receiptPath})
		}
		return err
	}
	return refuseLinkedWorktrees(worktrees)
}

// worktreeMergeReceiptWorktrees reads every source and candidate worktree a
// merge receipt names.
func worktreeMergeReceiptWorktrees(receiptPath string) ([]string, error) {
	contents, err := os.ReadFile(receiptPath)
	if err != nil {
		return nil, err
	}
	var receipt struct {
		Sources []struct {
			Worktree string `json:"worktree"`
		} `json:"sources"`
		Candidate struct {
			Worktree string `json:"worktree"`
		} `json:"candidate"`
	}
	if err := json.Unmarshal(contents, &receipt); err != nil {
		return nil, fmt.Errorf("read merge receipt %s: %w", receiptPath, err)
	}
	seen := map[string]bool{}
	var worktrees []string
	for _, source := range receipt.Sources {
		if source.Worktree != "" && !seen[source.Worktree] {
			seen[source.Worktree] = true
			worktrees = append(worktrees, source.Worktree)
		}
	}
	if receipt.Candidate.Worktree != "" && !seen[receipt.Candidate.Worktree] {
		worktrees = append(worktrees, receipt.Candidate.Worktree)
	}
	return worktrees, nil
}

// landingGuardAnnotation marks a command that pushes, lands or absorbs work,
// and therefore MUST refuse a worktree carrying a live local link.
//
// It exists so the guard is a declared contract rather than a call site
// somebody has to remember. `merge land`/`merge resume` were added to the
// landing surface after the guard and simply never called it, which let a
// prepare-then-link-then-land sequence push a linked worktree. A new verb —
// `wb stream absorb` in the local-integration rows — inherits the requirement
// by carrying this annotation, and TestEveryLandingVerbRefusesALiveLink fails
// if it carries the annotation without honouring it.
//
// The value says how the command is addressed, because that determines what
// the guard resolves: "worktree" for a path argument, "receipt" for a merge
// receipt naming its sources.
const landingGuardAnnotation = "wb.dev/landing-guard"

const (
	landingGuardByWorktree = "worktree"
	landingGuardByReceipt  = "receipt"
	// landingGuardByPullRequest addresses a landing by `owner/repository#N`.
	// The worktrees to guard are every open stream member of that repository:
	// landing a pull request while the stream's checkout still builds against
	// an unpublished tree publishes a commit whose CI ran against something the
	// registry never carried, which is the same defect the other two modes
	// guard, arriving through a different door.
	landingGuardByPullRequest = "pull-request"
)

// markLandingGuard declares that a command must refuse a live local link.
func markLandingGuard(command *cobra.Command, addressing string) *cobra.Command {
	if command.Annotations == nil {
		command.Annotations = map[string]string{}
	}
	command.Annotations[landingGuardAnnotation] = addressing
	return command
}

// landingSurface is the fixed list of verbs that push, land or absorb work.
//
// It is maintained INDEPENDENTLY of the annotation on purpose. Deriving the
// expected set from the annotation makes the check circular: a landing verb
// that simply never declares the annotation passes, which is exactly how
// `merge land` and `merge resume` sat on the landing surface unguarded. This
// list is the claim; the annotation is the implementation; the test compares
// them and fails on either a verb missing its annotation or an annotation on a
// verb not listed here.
//
// A new landing or absorb verb — `wb stream absorb` in the local-integration
// rows — is added here FIRST, and the test then fails until it declares the
// guard.
var landingSurface = map[string]string{
	"wb worktree merge":         landingGuardByWorktree,
	"wb worktree merge prepare": landingGuardByWorktree,
	"wb worktree merge land":    landingGuardByReceipt,
	"wb worktree merge resume":  landingGuardByReceipt,
	"wb pr land":                landingGuardByPullRequest,
}

// refuseLinkedRepositoryWorktrees is the pull-request entry point.
//
// A landing addressed by `owner/repository#N` names no worktree, so the guard
// resolves them from the open streams that hold this repository. It runs before
// any GitHub call: a refusal that has to reach the network first is a refusal
// that fails differently when the network does.
func refuseLinkedRepositoryWorktrees(repository string) error {
	store, err := streams.Open(projectsRoot)
	if err != nil {
		return err
	}
	stream, found, unreadable, err := store.RepositoryStream(repository)
	if err != nil {
		return err
	}
	if len(unreadable) > 0 {
		// A stream WB cannot read might be the one holding a live link to this
		// repository. "I could not tell" must not be spelled the same way as
		// "there is no link", so the guard says what it could not read and
		// stops — the same rule refuseLinkedWorktrees applies to a store it
		// cannot open.
		names := make([]string, 0, len(unreadable))
		for _, entry := range unreadable {
			names = append(names, entry.Name+" ("+entry.Reason+")")
		}
		return &exitError{code: exitUsage, message: "cannot tell whether " + repository +
			" holds a live local link: these streams are unreadable — " + strings.Join(names, "; ") +
			"; fix or remove them, then rerun"}
	}
	if !found {
		// Outside every stream a hand-written go.work is still a live link, and
		// it is the signal stream state cannot see. Guard every WB worktree of
		// this repository directly.
		return refuseLinkedWorktreesOfRepository(repository)
	}
	worktrees := make([]string, 0, len(stream.Members))
	for _, member := range stream.Members {
		if member.Repository == repository && member.Worktree != "" {
			worktrees = append(worktrees, member.Worktree)
		}
	}
	return refuseLinkedWorktrees(worktrees)
}

// refuseLinkedWorktreesOfRepository guards a repository that belongs to no
// stream.
//
// The two link signals are independent: stream state would miss a hand-written
// `go.work`, and `go.work` would miss an npm link. A repository outside every
// stream has no stream state at all, so the second signal is the only one
// there is — and skipping the guard because the first is empty is exactly the
// "I could not tell" spelled as "there is no link" that this file opens by
// forbidding.
func refuseLinkedWorktreesOfRepository(repository string) error {
	listed, err := worktrees.ListWithDiagnostics(context.Background(), worktrees.ListOptions{
		ProjectsRoot: projectsRoot,
	})
	if err != nil {
		return err
	}
	paths := make([]string, 0, 4)
	for _, entry := range listed.Results {
		if entry.Repository == repository {
			paths = append(paths, entry.WorktreeDir)
		}
	}
	return refuseLinkedWorktrees(paths)
}
