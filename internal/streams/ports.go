package streams

import "context"

// The ports below are the whole surface a stream verb needs from the outside
// world. They exist so the verbs are testable against fakes rather than
// against a real remote: a stream verb that could only be exercised end to end
// would be exactly the verb whose refusals are never proven.

// Git is the local Git surface stream verbs use.
type Git interface {
	// CurrentBranch reports the checked-out branch of a worktree.
	CurrentBranch(ctx context.Context, dir string) (string, error)
	// DefaultBranch reports the repository's default branch from local
	// state. It must never contact the network.
	DefaultBranch(ctx context.Context, dir string) (string, error)
	// Fetch refreshes origin so a verb acts on a live view rather than a
	// session-start snapshot.
	Fetch(ctx context.Context, dir string) error
	// PushBranch publishes branch from dir and sets its upstream. It must
	// verify the pushed ref rather than trust the exit code.
	PushBranch(ctx context.Context, dir, branch string) (sha string, err error)
	// RemoteHead resolves origin/<branch>, reporting ok=false when the
	// branch does not exist on the remote.
	RemoteHead(ctx context.Context, dir, branch string) (sha string, ok bool, err error)
	// LocalHead resolves the worktree's HEAD.
	LocalHead(ctx context.Context, dir string) (string, error)
	// CommitsNotIn lists the commits on branch whose patch base does not
	// already carry, by patch identity rather than by SHA — a rebase landing
	// rewrites SHAs, so an ancestry test would refuse every landed stream
	// forever. Each commit carries its `git patch-id --stable`, which is what
	// clusters N branches carrying one body of work into one item.
	CommitsNotIn(ctx context.Context, dir, branch, base string) ([]Commit, error)
	// DirtyPaths lists modified or untracked paths in a worktree.
	DirtyPaths(ctx context.Context, dir string) ([]string, error)
	// Tags lists tags matching a glob pattern, newest version first.
	Tags(ctx context.Context, dir, pattern string) ([]string, error)
	// LogSubjects lists the subjects of commits in the exclusive range
	// from..to. An empty from means "every commit reachable from to".
	LogSubjects(ctx context.Context, dir, from, to string) ([]string, error)
	// DeleteRemoteBranch removes a branch from origin and verifies it is
	// gone, so "removes its own scaffolding" covers the remote as well as the
	// local checkout.
	DeleteRemoteBranch(ctx context.Context, dir, branch string) error
}

// PullRequest is the subset of a pull request stream verbs read.
type PullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	Head   string `json:"head"`
	Base   string `json:"base"`
	Draft  bool   `json:"draft"`
	State  string `json:"state"`
}

// GitHub is the remote surface stream verbs use. Every method takes the
// worktree directory so the underlying tool resolves the repository the same
// way an operator standing in that directory would.
type GitHub interface {
	// CreateDraftPullRequest opens a draft pull request from head to base.
	CreateDraftPullRequest(ctx context.Context, dir, base, head, title, body string) (PullRequest, error)
	// PullRequestForBranch finds the open pull request whose head is branch.
	PullRequestForBranch(ctx context.Context, dir, branch string) (PullRequest, bool, error)
	// OpenPullRequestsTargeting lists every open pull request whose base is
	// base. `stream end` needs this to find the agent pull requests that
	// GitHub would silently retarget at `main` when the stream branch is
	// deleted.
	OpenPullRequestsTargeting(ctx context.Context, dir, base string) ([]PullRequest, error)
	// ClosePullRequest closes one pull request with a comment saying why.
	ClosePullRequest(ctx context.Context, dir string, number int, comment string) error
	// RetargetPullRequest moves one pull request onto a new base.
	RetargetPullRequest(ctx context.Context, dir string, number int, base string) error
	// PullRequest re-reads one pull request by number, reporting found=false
	// when it does not resolve. It is what makes a close or a retarget an
	// asserted effect rather than a trusted exit code.
	PullRequest(ctx context.Context, dir string, number int) (PullRequest, bool, error)
	// DefaultBranchStatus reports the conclusion of the most recent
	// completed CI run on the repository's default branch: "success",
	// "failure", or "" when it cannot be established.
	DefaultBranchStatus(ctx context.Context, dir, branch string) (conclusion string, err error)
}
