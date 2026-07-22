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
