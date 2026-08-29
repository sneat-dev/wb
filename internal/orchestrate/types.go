// Package orchestrate runs typed repository mutations through isolated
// worktrees, local verification, and optional GitHub publication stages.
package orchestrate

import (
	"context"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
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
	Progress          progress.Reporter

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

// RemoteCheck is the normalized GitHub check state observed before merge.
type RemoteCheck struct {
	Name   string `json:"name" yaml:"name"`
	Bucket string `json:"bucket" yaml:"bucket"`
	Link   string `json:"link,omitempty" yaml:"link,omitempty"`
	AppID  int64  `json:"app_id,omitempty" yaml:"app_id,omitempty"`
}

// RequiredRemoteCheck is GitHub's target-policy expectation. IntegrationID
// is non-zero when a ruleset pins the context to one GitHub App; every receipt
// must then observe the matching exact-head check-run producer, not merely a
// same-named PR summary or legacy status from another actor.
type RequiredRemoteCheck struct {
	Name          string `json:"name" yaml:"name"`
	IntegrationID int64  `json:"integration_id,omitempty" yaml:"integration_id,omitempty"`
}

// PullRequestWaitOptions identifies exactly one direct-push or pull-request
// head whose observed checks are read by a bounded foreground invocation. A
// caller resumes a pending result with the same repository, target, PR (when
// supplied), and head; any later head is a distinct integration candidate.
type PullRequestWaitOptions struct {
	Repository  string
	PullRequest string
	Target      string
	Head        string
	// AllowTargetDescendant is only for post-landing target CI: the exact
	// landed Head must remain an ancestor of the observed target. Pre-landing
	// candidate and pull-request waits retain exact target-head freshness.
	AllowTargetDescendant bool
	Slice                 time.Duration
	CheckPollInterval     time.Duration
	// Progress receives completed GitHub observations. It is diagnostic only;
	// callers must use the returned result as the authoritative receipt.
	Progress func(PullRequestWaitProgress)
}

// PullRequestWaitProgress is one completed observation inside a bounded wait.
type PullRequestWaitProgress struct {
	Observation int
	Result      PullRequestWaitResult
	NextPoll    time.Duration
}

// PullRequestWaitStatus is intentionally small so callers can branch on a
// machine result instead of parsing human GitHub CLI output.
type PullRequestWaitStatus string

const (
	PullRequestWaitPassed  PullRequestWaitStatus = "passed"
	PullRequestWaitPending PullRequestWaitStatus = "pending"
	PullRequestWaitFailed  PullRequestWaitStatus = "failed"
)

// PullRequestWaitResult is one terminating foreground observation slice.
// Pending means resume is required, not that the merger is finished.
type PullRequestWaitResult struct {
	Status                   PullRequestWaitStatus `json:"status" yaml:"status"`
	Repository               string                `json:"repository" yaml:"repository"`
	PullRequest              string                `json:"pull_request,omitempty" yaml:"pull_request,omitempty"`
	Target                   string                `json:"target" yaml:"target"`
	Head                     string                `json:"head" yaml:"head"`
	ObservedHead             string                `json:"observed_head,omitempty" yaml:"observed_head,omitempty"`
	ObservedTargetHead       string                `json:"observed_target_head,omitempty" yaml:"observed_target_head,omitempty"`
	CandidateContainsTarget  bool                  `json:"candidate_contains_target,omitempty" yaml:"candidate_contains_target,omitempty"`
	TargetContainsHead       bool                  `json:"target_contains_head,omitempty" yaml:"target_contains_head,omitempty"`
	TargetFreshnessAuthority string                `json:"target_freshness_authority,omitempty" yaml:"target_freshness_authority,omitempty"`
	Checks                   []RemoteCheck         `json:"checks,omitempty" yaml:"checks,omitempty"`
	RequiredChecks           []RequiredRemoteCheck `json:"required_checks,omitempty" yaml:"required_checks,omitempty"`
	RequiredChecksAuthority  string                `json:"required_checks_authority,omitempty" yaml:"required_checks_authority,omitempty"`
	StableObservations       int                   `json:"stable_observations" yaml:"stable_observations"`
	Reason                   string                `json:"reason,omitempty" yaml:"reason,omitempty"`
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
	Checks        []RemoteCheck
	Merged        bool
}
