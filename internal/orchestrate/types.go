// Package orchestrate runs typed repository mutations through isolated
// worktrees, local verification, and optional GitHub publication stages.
package orchestrate

import (
	"context"
	"path"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/runner"
)

// Repository identifies a canonical clone selected by command-level discovery.
type Repository struct {
	Slug     string
	Path     string
	CloneURL string
	Archived bool
}

// Options controls a repository operation independently of mutation policy.
type Options struct {
	GitHubDir string
	Operation string
	Branch    string
	Ref       string
	Parallel  int
	DryRun    bool
	Resume    bool
	Verify    bool
	Checks    []quality.Check
	Timeout   time.Duration
	Retry     int
	// CheckPollInterval overrides the GitHub-check polling delay. A zero value
	// uses the production default. It is primarily useful for deterministic
	// lifecycle tests.
	CheckPollInterval time.Duration
	Commit            bool
	Push              bool
	PR                bool
	Merge             bool
	// WaitForPRChecks observes exact PR-head checks after opening a pull
	// request, but deliberately does not merge it. It is only valid with PR
	// publication and is intended for validation-only campaigns.
	WaitForPRChecks bool
	// Hold is a set of "owner/name" glob patterns naming repositories whose
	// merge is a human decision. A held repository is changed, verified,
	// pushed, has its pull request opened, and has its exact PR-head checks
	// waited on exactly like any other — and is then left OPEN, even under
	// Merge. It is not a way to skip a repository (that is the caller's own
	// exclusion, applied before this engine sees the repository); it is a way
	// to do all the mechanical work and stop at the one step that needs an
	// owner's judgement, such as a deploy repository the founder gates.
	Hold     []string
	Progress progress.Reporter

	// Prompt is recorded as the originating instruction in the WB manifest
	// journal of every worktree this operation creates, satisfying wb's own
	// commit-admission hook (internal/worktrees.CheckAdmission) — without it,
	// a worktree this engine creates and then commits into is rejected by
	// wb's own pre-commit hook as carrying no record of what it is or who
	// asked for it. Normalize fills in an operation-derived default when
	// empty, so every caller gets a truthful record even if it has nothing
	// more specific to say.
	Prompt string
	// Model, AgentRuntime, Initiator, CLI, and Provider identify who or what
	// asked for this operation, recorded in the same manifest for
	// provenance. Normalize defaults Model to "unknown" when empty, matching
	// the same explicit-over-guessed convention used everywhere else a
	// child model identity is recorded (see internal/worktrees.WorkLogOptions).
	Model        string
	AgentRuntime string
	Initiator    string
	CLI          string
	Provider     string
	// DependencyCampaign marks worktrees created by dependency set/bump
	// campaigns. Their supersession receipts require exact dependency proof.
	DependencyCampaign bool
	// FetchMemo, when non-nil, memoizes this run's completed origin fetches
	// and receives touch-invalidation from the push, PR-open, and merge
	// stages (see FetchMemo). Only a campaign loop that alternates fleet-wide
	// discovery and mutation over the same repositories within one process —
	// wb deps bump with --fetch-cache — threads one memo through every
	// discovery and wave lifecycle it runs. Every other caller leaves it nil
	// and keeps the unconditional engine fetch: for deps set --fleet there is
	// no prior discovery, so that fetch is the operation's only origin read
	// and must never be skipped.
	FetchMemo *FetchMemo
	// FetchMemoDiscovery marks this lifecycle as read-only graph discovery,
	// which is the ONLY context allowed to consume the memo: EnsureCanonical
	// may then skip a fresh, untouched memoized fetch. The mutation engine
	// leaves it false even when FetchMemo is threaded, so a wave's branch
	// base is always cut from a fetch completed moments before — never from a
	// snapshot up to one full discovery pass old, which would interact badly
	// with strict up-to-date branch protection and exact-head merges. The
	// engine's own fetches still refresh the memo, and its publication
	// stages still invalidate through it.
	FetchMemoDiscovery bool

	// run overrides this package's generic command runner (ports.go); nil
	// uses defaultRunner. A unit test sets this to a runnertest.Fake so an
	// engine call that reaches the migrated call sites
	// (spec/plans/coverage-to-100 task-17) never starts a real process.
	run runner.Runner
}

// resolveRunner returns options.run, falling back to defaultRunner
// (ports.go) when the caller left it nil.
func (options Options) resolveRunner() runner.Runner {
	if options.run != nil {
		return options.run
	}
	return defaultRunner
}

// Assessment is adapter-owned planning metadata plus an execution decision.
type Assessment[T any] struct {
	Metadata    T
	Applicable  bool
	NeedsChange bool
	Reason      string
}

// Handler supplies mutation policy while Engine owns repository lifecycle.
type Handler[T any] interface {
	Inspect(context.Context, string, string, Repository) (Assessment[T], error)
	Apply(context.Context, string, Repository) (T, error)
	ValidatePublishable(context.Context, string, Repository) error
	CommitMessage(Repository) string
	PullRequest(Repository) (title, body string)
}

// InPlaceInspector is implemented by handlers whose plans can read a supplied
// managed worktree directly. The normal Handler contract intentionally plans
// from a fetched canonical base; this narrower opt-in keeps that behavior for
// every other caller while allowing dependency updates to respect staged and
// unstaged manifest state in an explicit in-place request.
type InPlaceInspector[T any] interface {
	InspectWorkingTree(context.Context, string, Repository) (Assessment[T], error)
}

// AppliedFileReporter identifies files the handler changed. The engine unions
// these with the Git-status delta for an in-place request, so pre-existing
// dirty or untracked implementation files do not become operation report
// evidence merely because they were already present in the checkout.
type AppliedFileReporter[T any] interface {
	AppliedFiles(T) []string
}

// Result records lifecycle state and typed adapter metadata for one repository.
type Result[T any] struct {
	Repository    string
	CanonicalDir  string
	WorktreeDir   string
	Branch        string
	Ref           string
	Status        string
	Reason        string
	Metadata      T
	ChangedFiles  []string
	Verifications []quality.VerificationEntry
	Commit        string
	Pushed        bool
	PR            string
	Checks        []githubchecks.RemoteCheck
	Merged        bool
	// Held records that Options.Hold matched this repository, so its pull
	// request was deliberately left open for a human decision rather than
	// merged. It is never inferred from a failure.
	Held bool
}

// MatchesHold reports whether an "owner/name" repository slug matches any
// hold pattern. Patterns use path.Match semantics, where "*" never crosses a
// "/", so "sneat-co/*" holds every repository in one owner and
// "sneat-co/sneat-go" holds exactly one. An exact string equal to the slug
// always matches, so a caller never has to think about glob metacharacters in
// a literal repository name.
func MatchesHold(slug string, patterns []string) bool {
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if pattern == slug {
			return true
		}
		if matched, err := path.Match(pattern, slug); err == nil && matched {
			return true
		}
	}
	return false
}
