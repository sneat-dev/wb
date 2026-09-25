package defaultbranch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/gitcli"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/runner"
)

// The ports below are this package's dependency contract, per
// spec/plans/coverage-to-100/README.md task-22 (decision 18): "Each family
// declares consumer-side ports in its destination package, backed at first
// by today's helpers, and switches to task-8's runner when that package's
// slot lands." Run and every helper it calls now reach git and gh only
// through Engine's four fields below — never through a package-level var or
// exec.CommandContext directly. Production wiring is newDefaultBranchEngine,
// below; a unit test builds an *Engine directly from fakes_test.go's fakes.

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

// Engine holds this command family's four ports. Run and every helper that
// reaches git, gh, fleet discovery or the wait clock is a method on *Engine,
// so a unit test builds one directly from fakes_test.go's fakes instead of
// reassigning a package-level var — the package-var-reassignment style this
// package's tests used before this file wired the ports in (task-22,
// decision 18's "unit tests that use fakes").
type Engine struct {
	Git       Git
	GitHub    GitHub
	Discovery Discovery
	Clock     Clock
}

// newDefaultBranchEngine returns an *Engine wired to production
// implementations: real git over internal/gitcli and internal/runner, real
// gh through internal/githubobserver, real fleet discovery
// (internal/discover), and a real clock.
func newDefaultBranchEngine() *Engine {
	return &Engine{
		Git:       defaultBranchGitAdapter{client: gitcli.New(runner.New())},
		GitHub:    defaultBranchGitHubAdapter{},
		Discovery: defaultBranchDiscoveryAdapter{},
		Clock:     defaultBranchClockAdapter{},
	}
}

// defaultBranchGitAdapter implements Git over a gitcli.Client for every
// operation gitcli exposes with an identical contract. Its own IsAncestor
// mirrors gitcli.Client.IsAncestor's exit-code interpretation without
// gitcli's ambiguous-exit sentinel wrapping, so a non-0/non-1 exit's error
// text stays exactly what this package produced before this move onto
// internal/runner (rule 1: byte-identical error text across a
// behaviour-preserving refactor).
type defaultBranchGitAdapter struct {
	client gitcli.Client
}

var _ Git = defaultBranchGitAdapter{}

// Run implements Git.
func (a defaultBranchGitAdapter) Run(ctx context.Context, dir string, args ...string) (string, error) {
	return a.client.Run(ctx, dir, args...)
}

// IsAncestor implements Git. See the type doc comment for why this is not a
// direct delegation to a.client.IsAncestor.
func (a defaultBranchGitAdapter) IsAncestor(ctx context.Context, dir, ancestor, descendant string) (bool, error) {
	result, err := a.client.Runner.Run(ctx, dir, "git", "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	if result.ExitCode == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %w: %s", ancestor, descendant, err, strings.TrimSpace(result.Stderr))
}

// AtomicRenameRefs implements Git.
func (a defaultBranchGitAdapter) AtomicRenameRefs(ctx context.Context, dir, source, destination, expected string) error {
	return a.client.AtomicRenameRefs(ctx, dir, source, destination, expected)
}

// AttachHead implements Git.
func (a defaultBranchGitAdapter) AttachHead(ctx context.Context, dir, destination string) error {
	return a.client.AttachHead(ctx, dir, destination)
}

// RefExists implements Git.
func (a defaultBranchGitAdapter) RefExists(ctx context.Context, dir, ref string) (bool, error) {
	return a.client.RefExists(ctx, dir, ref)
}

// defaultBranchGitHubAdapter implements GitHub over internal/githubobserver,
// unchanged from the package-level vars it replaces.
type defaultBranchGitHubAdapter struct{}

var _ GitHub = defaultBranchGitHubAdapter{}

// Read implements GitHub.
func (defaultBranchGitHubAdapter) Read(ctx context.Context, endpoint string) ([]byte, error) {
	return githubobserver.Read(ctx, "", "api", endpoint)
}

// Execute implements GitHub.
func (defaultBranchGitHubAdapter) Execute(ctx context.Context, args ...string) githubobserver.CommandResponse {
	return githubobserver.Execute(ctx, "", args...)
}

// defaultBranchDiscoveryAdapter implements Discovery over internal/discover,
// unchanged from the package-level vars it replaces.
type defaultBranchDiscoveryAdapter struct{}

var _ Discovery = defaultBranchDiscoveryAdapter{}

// AuthUser implements Discovery.
func (defaultBranchDiscoveryAdapter) AuthUser() (string, error) { return discover.AuthUser() }

// MemberOrgs implements Discovery.
func (defaultBranchDiscoveryAdapter) MemberOrgs() ([]string, error) { return discover.MemberOrgs() }

// ListRemote implements Discovery.
func (defaultBranchDiscoveryAdapter) ListRemote(owner string) ([]discover.Repo, error) {
	return discover.ListRemote(owner)
}

// defaultBranchClockAdapter implements Clock over the real time package,
// unchanged from the package-level vars it replaces.
type defaultBranchClockAdapter struct{}

var _ Clock = defaultBranchClockAdapter{}

// Now implements Clock.
func (defaultBranchClockAdapter) Now() time.Time { return time.Now() }

// Wait implements Clock.
func (defaultBranchClockAdapter) Wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
