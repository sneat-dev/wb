// This file declares internal/orchestrate's own narrow git port
// (spec/plans/coverage-to-100 task-8's "Git operations as narrow ports",
// task-17's migration of this package's call sites), the same shape
// internal/streams/ports.go, internal/streamsync/ports.go and
// internal/locallink/ports.go already use: a small interface naming exactly
// the git operations this package's landing and merge verbs need, backed in
// production by internal/gitcli over internal/runner, and substituted in a
// unit test by internal/gitcli/gitclitest.Fake.
//
// Every method's argv matches, byte for byte, the direct exec.CommandContext
// (or runGit-helper) call it replaces, so migrating a call site onto it
// changes nothing observable -- error text, argv and behaviour stay
// identical (spec/plans/coverage-to-100 rule 1).
package orchestrate

import (
	"context"

	"github.com/sneat-dev/wb/internal/gitcli"
	"github.com/sneat-dev/wb/internal/runner"
)

// Git is the local git surface `wb pr land` and `wb worktree merge` use.
type Git interface {
	// RevParse resolves rev to a commit SHA. Used both for "rev-parse HEAD"
	// and for "rev-parse <remote-tracking ref>".
	RevParse(ctx context.Context, dir, rev string) (string, error)
	// WorktreeRemoveForce removes worktree from dir's repository.
	WorktreeRemoveForce(ctx context.Context, dir, worktree string) error
	// WorktreeAddDetached adds a new worktree at path, detached at revision.
	WorktreeAddDetached(ctx context.Context, dir, path, revision string) error
	// CherryPickNoCommit replays shas without writing a commit.
	CherryPickNoCommit(ctx context.Context, dir string, shas ...string) error
	// CommitNoVerify writes a commit from the staged state without hooks.
	CommitNoVerify(ctx context.Context, dir, message string) error
	// CherryPick replays sha onto the current commit and commits it.
	CherryPick(ctx context.Context, dir, sha string) error
	// PushForceWithLeaseHead publishes HEAD to ref under a lease on
	// leaseSHA.
	PushForceWithLeaseHead(ctx context.Context, dir, ref, leaseSHA string) error
	// FetchRefs refreshes remote in dir for exactly refs.
	FetchRefs(ctx context.Context, dir, remote string, refs ...string) error
	// RevListReverseRange lists the commits in from..to, oldest first.
	RevListReverseRange(ctx context.Context, dir, from, to string) (string, error)
	// StatusPorcelain reports dir's porcelain-v1 status.
	StatusPorcelain(ctx context.Context, dir string) (string, error)
	// BranchShowCurrent reports dir's checked-out branch, empty when
	// detached.
	BranchShowCurrent(ctx context.Context, dir string) (string, error)
	// MergeBaseIsAncestorStrict errors whenever ancestor is not an ancestor
	// of descendant (or the check cannot be answered at all).
	MergeBaseIsAncestorStrict(ctx context.Context, dir, ancestor, descendant string) error
	// BranchSetUpstreamTo points branch's upstream at upstream.
	BranchSetUpstreamTo(ctx context.Context, dir, upstream, branch string) error
	// MergeTreeWriteTree performs a real-merge write-tree of a and b and
	// returns the resulting tree id.
	MergeTreeWriteTree(ctx context.Context, dir, a, b string) (string, error)
	// ShowTreeFormat resolves commit's tree id.
	ShowTreeFormat(ctx context.Context, dir, commit string) (string, error)
	// CommitObjectExists reports whether sha's commit object already
	// exists in dir's object database.
	CommitObjectExists(ctx context.Context, dir, sha string) bool
}

// orchestrateGit is production's Git port. A unit test replaces it with a
// *gitclitest.Fake.
var orchestrateGit Git = gitcli.New(runner.New())

// orchestrateRunner is production's generic command runner, for this
// package's non-git-port external calls (pr_land_keep.go's kept-commit
// build verification and patch-identity shell pipeline). command.go's own
// retrying runCommand keeps calling exec.CommandContext directly rather
// than through this seam -- see its doc comment. A unit test replaces this
// var with a runnertest.Fake.
var orchestrateRunner runner.Runner = runner.New()

var _ Git = gitcli.Client{}
