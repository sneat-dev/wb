// Package streamsync keeps a stream branch current and verifies its batch.
//
// It owns two things rows 8 and 9 of the Feature call for, and both are shared
// with the absorb verb that follows:
//
//   - the REBASE path — bring `stream/<name>` onto a freshly fetched
//     `origin/main` without ever merging, then bring every open agent branch
//     onto the new stream head, reporting conflicts per branch;
//   - the BATCH VERIFICATION path — apply the whole batch, run the suite once,
//     and on failure find the culprit by cumulative prefix re-apply on a local
//     scratch branch that is never pushed.
//
// Nothing here pushes. Under `pushes-are-justified-and-counted` a push happens
// only on one of four named triggers, and a dependency bump is never one of
// them: bumps are local commits on the stream branch, verified once as a batch.
//
// Implements: dependency-streams#req:sync-rebases-and-never-merges,
// dependency-streams#req:sync-is-idempotent-against-landed-bumps,
// dependency-streams#req:dependency-bumps-are-commits-on-the-stream-branch,
// dependency-streams#req:batch-verifies-once,
// dependency-streams#req:the-batch-element-is-defined,
// dependency-streams#req:batch-failure-is-found-by-prefix-re-apply,
// dependency-streams#req:every-verification-run-is-bounded,
// dependency-streams#req:pushes-are-justified-and-counted.
package streamsync

import (
	"context"
	"time"
)

// Git is the local Git surface sync and absorb share.
//
// Every method is deliberately narrow: the engine composes them, so a fake can
// prove the ORDER of operations — rebase before bump is the whole mechanism
// behind idempotence against a bump Renovate already landed.
type Git interface {
	// Fetch refreshes origin. Sync rebases onto a freshly fetched base, never
	// onto whatever the local clone happens to hold.
	Fetch(ctx context.Context, dir string) error
	// CurrentBranch reports the checked-out branch.
	CurrentBranch(ctx context.Context, dir string) (string, error)
	// Rebase replays branch onto upstream. It reports the conflicting paths
	// rather than leaving the worktree mid-rebase: a conflict in one agent's
	// branch must not abort the others, so the caller needs the conflict as
	// data and the tree back in a usable state.
	Rebase(ctx context.Context, dir, branch, upstream string) (conflicts []string, err error)
	// AbortRebase restores the tree after a conflicted rebase.
	AbortRebase(ctx context.Context, dir string) error
	// Head resolves a revision to a SHA.
	Head(ctx context.Context, dir, revision string) (string, error)
	// CommitsAhead counts commits on branch that upstream does not carry.
	CommitsAhead(ctx context.Context, dir, branch, upstream string) (int, error)
	// CommitAll stages every change and writes one commit. It returns the new
	// SHA, and ok=false when there was nothing to commit — which is what makes
	// a re-run of sync produce no new commits.
	CommitAll(ctx context.Context, dir, message string) (sha string, ok bool, err error)
	// CreateBranch points a new branch at a revision, replacing it if it
	// exists. Prefix re-apply runs on one of these and it is never pushed.
	CreateBranch(ctx context.Context, dir, branch, revision string) error
	// Checkout switches the worktree to a branch.
	Checkout(ctx context.Context, dir, branch string) error
	// ResetHard moves the current branch to a revision, discarding changes.
	ResetHard(ctx context.Context, dir, revision string) error
	// CherryPick applies one commit onto the current branch.
	CherryPick(ctx context.Context, dir, sha string) error
	// DeleteBranch removes a local branch.
	DeleteBranch(ctx context.Context, dir, branch string) error
	// IsClean reports whether the worktree has no uncommitted change.
	IsClean(ctx context.Context, dir string) (bool, error)
}

// Bumper applies one library's version bump inside the stream worktree.
//
// It is a port because the bump mechanics already exist in the deps package
// and because the interesting behaviour — writing a commit ONLY where the
// required version is still below the target — has to be provable without a
// real module graph.
type Bumper interface {
	// Required reports the version the consumer currently requires for one
	// library, after the rebase. found=false means the consumer does not
	// declare it at all.
	Required(ctx context.Context, dir string, library Library) (version string, found bool, err error)
	// Apply updates the manifests and lockfiles to target. It must run the
	// toolchain that refreshes go.sum / pnpm-lock.yaml, and it must not
	// commit — the engine owns the commit so the message follows the
	// repository's convention and so a no-op leaves no commit behind.
	Apply(ctx context.Context, dir string, library Library) error
}

// Library is one own-library version the stream is moving to.
type Library struct {
	// Name is the module path or package name.
	Name string `json:"name"`
	// Target is the version the consumer should require.
	Target string `json:"target"`
	// Ecosystem is "go" or "npm"; it selects the commit-message convention.
	Ecosystem string `json:"ecosystem"`
}

// VerificationRun is one batch verification pass.
type VerificationRun struct {
	Passed  bool     `json:"passed"`
	Command string   `json:"command,omitempty"`
	Details []string `json:"details,omitempty"`
	// Skipped names each CI mechanism this run did not execute. It is only
	// ever printed alongside evidence that CI actually carries it.
	Skipped []string `json:"skipped,omitempty"`
	// Duration is the wall time, so a boundary's cost is measurable rather
	// than remembered.
	Duration time.Duration `json:"-"`
}

// Verifier runs the batch verification over a tree.
type Verifier interface {
	// Verify runs the full suite once over the tree as it stands.
	Verify(ctx context.Context, dir string) (VerificationRun, error)
}

// CIMechanisms reports which mechanisms a member's stream-PR workflows carry.
//
// `batch-verification-runs-what-ci-runs` allows naming a mechanism as skipped
// only after proving CI actually runs it. An unverified "CI owns it" is worse
// than no gate, so this is a read of the workflows rather than an assumption.
type CIMechanisms interface {
	Present(dir string) (mechanisms map[string]bool, err error)
}

// Events records what a verb did.
type Events interface {
	Append(event Event) error
}

// Event is one structured record.
type Event struct {
	Stream     string            `json:"stream"`
	Verb       string            `json:"verb"`
	Phase      string            `json:"phase,omitempty"`
	Repository string            `json:"repository,omitempty"`
	Outcome    string            `json:"outcome"`
	Detail     string            `json:"detail,omitempty"`
	Evidence   map[string]string `json:"evidence,omitempty"`
}
