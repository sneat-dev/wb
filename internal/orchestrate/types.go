// Package orchestrate runs typed repository mutations through isolated
// worktrees, local verification, and optional GitHub publication stages.
package orchestrate

import (
	"context"
	"time"

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
}

// PullRequestWaitOptions identifies exactly one direct-push or pull-request
// head whose observed checks are read by a bounded foreground invocation. A
// caller resumes a pending result with the same repository, target, PR (when
// supplied), and head; any later head is a distinct integration candidate.
type PullRequestWaitOptions struct {
	Repository        string
	PullRequest       string
	Target            string
	Head              string
	Slice             time.Duration
	CheckPollInterval time.Duration
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
	Status       PullRequestWaitStatus `json:"status" yaml:"status"`
	Repository   string                `json:"repository" yaml:"repository"`
	PullRequest  string                `json:"pull_request,omitempty" yaml:"pull_request,omitempty"`
	Target       string                `json:"target" yaml:"target"`
	Head         string                `json:"head" yaml:"head"`
	ObservedHead string                `json:"observed_head,omitempty" yaml:"observed_head,omitempty"`
	Checks       []RemoteCheck         `json:"checks,omitempty" yaml:"checks,omitempty"`
	Reason       string                `json:"reason,omitempty" yaml:"reason,omitempty"`
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
