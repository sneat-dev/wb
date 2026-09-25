package defaultbranch

import (
	"context"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/githubobserver"
)

// The ports below are the target dependency contract for this package, per
// spec/plans/coverage-to-100/README.md task-22 (decision 18): "Each family
// declares consumer-side ports in its destination package, backed at first
// by today's helpers, and switches to task-8's runner when that package's
// slot lands." Today, Run and its helpers still reach git and gh through the
// package-level function vars below ports.go (defaultBranchGit,
// defaultBranchRead, and similar) — those are today's helpers, unchanged by
// this file. Wiring Run to accept these ports as parameters, and providing
// the fakes that let unit tests substitute them, is the next lane's work
// (spec/plans/coverage-to-100/README.md task-22, commit 3 of this file's
// move); this file only fixes the contract shape so that lane does not have
// to design it first. Once task-8's internal/runner and internal/gitcli
// land, a real adapter satisfies Git without exec.CommandContext, and the
// port shape here does not need to change for callers.

// Git is the local git surface this command family needs: renaming a local
// default branch, verifying ancestry before a local reconciliation, and
// reattaching a detached HEAD after a rename. It mirrors the six
// exec.CommandContext call sites the package-level git helpers make today.
type Git interface {
	// Run executes a git subcommand in dir and returns its trimmed combined
	// output, or an error carrying that output when git exits non-zero.
	Run(ctx context.Context, dir string, args ...string) (string, error)
	// IsAncestor reports whether ancestor is reachable from descendant in
	// dir's history, via `git merge-base --is-ancestor`.
	IsAncestor(ctx context.Context, dir, ancestor, descendant string) (bool, error)
	// AtomicRenameRefs renames dir's local branch source to destination in
	// one `git update-ref --stdin` transaction, after detaching HEAD at the
	// verified expected SHA.
	AtomicRenameRefs(ctx context.Context, dir, source, destination, expected string) error
	// AttachHead points dir's HEAD at the local branch destination.
	AttachHead(ctx context.Context, dir, destination string) error
	// RefExists reports whether ref resolves in dir, via
	// `git rev-parse --verify --quiet`.
	RefExists(ctx context.Context, dir, ref string) (bool, error)
}

// GitHub is the gh-backed remote read/execute surface this command family
// uses to inspect and mutate a repository's default branch, Pages source,
// workflow triggers, and archived state.
type GitHub interface {
	// Read performs a read-only `gh api` GET against endpoint.
	Read(ctx context.Context, endpoint string) ([]byte, error)
	// Execute runs an arbitrary `gh` invocation (a mutation, or a GraphQL
	// query `Read` cannot express) and reports its captured result.
	Execute(ctx context.Context, args ...string) githubobserver.CommandResponse
}

// Discovery lists the fleet this command family audits or applies against.
type Discovery interface {
	// AuthUser reports the authenticated GitHub user's login.
	AuthUser() (string, error)
	// MemberOrgs lists the organizations the authenticated user belongs to.
	MemberOrgs() ([]string, error)
	// ListRemote lists owner's accessible repositories.
	ListRemote(owner string) ([]discover.Repo, error)
}

// Clock is the wait/now seam used while polling for a delayed rename to
// become visible on GitHub's read replicas. It never drives a retry of the
// mutation itself — only bounded, read-only re-checks before a deadline.
type Clock interface {
	// Now reports the current time.
	Now() time.Time
	// Wait blocks for d, or until ctx is done, whichever comes first.
	Wait(ctx context.Context, d time.Duration) error
}
