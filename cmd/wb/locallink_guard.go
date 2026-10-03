package main

import (
	"github.com/sneat-dev/wb/internal/landingcontext"
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
func refuseLinkedWorktrees(inv *invocation, paths []string) error {
	return landingGuardError(landingcontext.CheckWorktrees(inv.projectsRoot, paths))
}

// refuseLinkedReceiptWorktrees is the land/resume entry point.
//
// `merge-refuses-a-linked-worktree` says merge must refuse to "push or land",
// and the land verbs are the ones that actually push. They are addressed by a
// receipt, so the worktrees to guard are read out of it. A receipt WB cannot
// read is not a reason to skip the guard — it is a reason to say so and stop.
func refuseLinkedReceiptWorktrees(inv *invocation, path string) error {
	return landingGuardError(landingcontext.CheckReceipt(inv.projectsRoot, path))
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
	"wb land":                   landingGuardByWorktree,
	"wb worktree land":          landingGuardByWorktree,
	"wb worktree merge":         landingGuardByWorktree,
	"wb worktree merge prepare": landingGuardByWorktree,
	"wb worktree merge land":    landingGuardByReceipt,
	"wb worktree merge resume":  landingGuardByReceipt,
	"wb pr land":                landingGuardByPullRequest,
}
