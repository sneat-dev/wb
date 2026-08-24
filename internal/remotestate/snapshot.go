// Package remotestate publishes one machine's WB fleet state to a shared
// store and reads every machine's state back. The store is pluggable; the
// snapshot format is not.
package remotestate

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// SchemaVersion is the snapshot format this binary writes and the newest it
// can read.
const SchemaVersion = 1

// Status constants for RepositoryState.
const (
	StatusAttention = "attention"
	StatusError     = "error"
)

// Redaction selects how unpushed commits are published.
type Redaction string

const (
	// RedactNone publishes `git log --oneline` subjects of unpushed commits.
	RedactNone Redaction = "subjects"
	// RedactUnpushed publishes only the number of unpushed commits.
	RedactUnpushed Redaction = "counts"
)

// Snapshot is one machine's published fleet state.
type Snapshot struct {
	SchemaVersion       int               `yaml:"schema_version" json:"schema_version"`
	Login               string            `yaml:"login" json:"login"`
	Machine             string            `yaml:"machine" json:"machine"`
	PublishedAt         time.Time         `yaml:"published_at" json:"published_at"`
	WBVersion           string            `yaml:"wb_version" json:"wb_version"`
	ProjectsRoot        string            `yaml:"projects_root" json:"projects_root"`
	RepositoriesScanned int               `yaml:"repositories_scanned" json:"repositories_scanned"`
	Repositories        []RepositoryState `yaml:"repositories" json:"repositories"`
	Worktrees           []WorktreeState   `yaml:"worktrees" json:"worktrees"`
}

// Key identifies the machine inside a store: "<login>/<machine>".
func (s Snapshot) Key() string { return s.Login + "/" + s.Machine }

// RepositoryState is one non-clean repository on the publishing machine.
type RepositoryState struct {
	Repository    string   `yaml:"repository" json:"repository"`
	Path          string   `yaml:"path" json:"path"`
	Status        string   `yaml:"status" json:"status"` // attention | error
	Summary       string   `yaml:"summary,omitempty" json:"summary,omitempty"`
	Branch        string   `yaml:"branch,omitempty" json:"branch,omitempty"`
	Upstream      string   `yaml:"upstream,omitempty" json:"upstream,omitempty"`
	Ahead         int      `yaml:"ahead,omitempty" json:"ahead,omitempty"`
	Behind        int      `yaml:"behind,omitempty" json:"behind,omitempty"`
	Modified      []string `yaml:"modified,omitempty" json:"modified,omitempty"`
	Untracked     []string `yaml:"untracked,omitempty" json:"untracked,omitempty"`
	Conflicted    []string `yaml:"conflicted,omitempty" json:"conflicted,omitempty"`
	Unpushed      []string `yaml:"unpushed,omitempty" json:"unpushed,omitempty"`
	UnpushedCount int      `yaml:"unpushed_count,omitempty" json:"unpushed_count,omitempty"`
	Stashed       []string `yaml:"stashed,omitempty" json:"stashed,omitempty"`
	Error         string   `yaml:"error,omitempty" json:"error,omitempty"`
}

// WorktreeState is one WB task worktree on the publishing machine, whether
// or not its owning session is still alive.
type WorktreeState struct {
	Task       string `yaml:"task" json:"task"`
	Repository string `yaml:"repository" json:"repository"`
	Branch     string `yaml:"branch" json:"branch"`
	HeadSHA    string `yaml:"head_sha" json:"head_sha"`
	Dir        string `yaml:"dir" json:"dir"`
	// OwnerState is worktrees.ListResult.OwnerState: "active", "orphaned", or
	// "unknown". Empty only if the underlying scan left it unset.
	OwnerState string `yaml:"owner_state,omitempty" json:"owner_state,omitempty"`
}

// RepositoryInput is the per-repository scan result Build consumes. Err set
// means the scan itself failed; Status and Tracking are then ignored.
type RepositoryInput struct {
	Repository string
	Path       string
	Status     gitops.RepoStatus
	Tracking   gitops.TrackingState
	Err        error
}

// needsAttention mirrors the spec's definition of non-clean: dirty tree,
// stash, unpushed commits, ahead/behind, or a configured-but-unresolved or
// missing upstream on a named branch.
func (in RepositoryInput) needsAttention() bool {
	if in.Status.Dirty() || in.Tracking.Ahead > 0 || in.Tracking.Behind > 0 {
		return true
	}
	return in.Tracking.Branch != "" && in.Tracking.Upstream == ""
}

// buildSummary combines Status.Summary() and Tracking.Summary() into one line.
// It joins non-empty parts with "; " and includes the tracking line only when
// tracking itself is an attention cause (ahead/behind or configured-but-unresolved upstream).
func buildSummary(in RepositoryInput) string {
	parts := make([]string, 0, 2)
	if s := in.Status.Summary(); s != "" {
		parts = append(parts, s)
	}
	trackingMatters := in.Tracking.Ahead > 0 || in.Tracking.Behind > 0 || (in.Tracking.Branch != "" && in.Tracking.Upstream == "")
	if trackingMatters {
		if s := in.Tracking.Summary(); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "; ")
}

// Build assembles a snapshot. identity supplies Login, Machine, PublishedAt,
// WBVersion, and ProjectsRoot; everything else is derived here. Clean
// repositories are counted but not listed. Output is sorted by repository.
func Build(identity Snapshot, repos []RepositoryInput, wts []worktrees.ListResult, redaction Redaction) Snapshot {
	snap := identity
	snap.SchemaVersion = SchemaVersion
	snap.RepositoriesScanned = len(repos)
	snap.Repositories = make([]RepositoryState, 0)
	snap.Worktrees = make([]WorktreeState, 0, len(wts))
	for _, in := range repos {
		if in.Err != nil {
			snap.Repositories = append(snap.Repositories, RepositoryState{Repository: in.Repository, Path: in.Path, Status: StatusError, Error: in.Err.Error()})
			continue
		}
		if !in.needsAttention() {
			continue
		}

		// Combine status and tracking summaries
		summary := buildSummary(in)

		state := RepositoryState{
			Repository:    in.Repository,
			Path:          in.Path,
			Status:        StatusAttention,
			Summary:       summary,
			Branch:        in.Tracking.Branch,
			Upstream:      in.Tracking.Upstream,
			Ahead:         in.Tracking.Ahead,
			Behind:        in.Tracking.Behind,
			Modified:      in.Status.Modified,
			Untracked:     in.Status.Untracked,
			Conflicted:    in.Status.Conflicted,
			UnpushedCount: len(in.Status.Unpushed),
			Stashed:       in.Status.Stashed,
		}
		if redaction != RedactUnpushed {
			state.Unpushed = in.Status.Unpushed
		}
		snap.Repositories = append(snap.Repositories, state)
	}
	sort.Slice(snap.Repositories, func(i, j int) bool { return snap.Repositories[i].Repository < snap.Repositories[j].Repository })
	for _, wt := range wts {
		snap.Worktrees = append(snap.Worktrees, WorktreeState{Task: wt.Task, Repository: wt.Repository, Branch: wt.Branch, HeadSHA: wt.HeadSHA, Dir: wt.WorktreeDir, OwnerState: wt.OwnerState})
	}
	sort.Slice(snap.Worktrees, func(i, j int) bool {
		if snap.Worktrees[i].Task != snap.Worktrees[j].Task {
			return snap.Worktrees[i].Task < snap.Worktrees[j].Task
		}
		return snap.Worktrees[i].Repository < snap.Worktrees[j].Repository
	})
	return snap
}

// Encode renders a snapshot as YAML.
func Encode(s Snapshot) ([]byte, error) { return yaml.Marshal(s) }

// Decode parses a snapshot, refusing formats newer than this binary knows.
func Decode(data []byte) (Snapshot, error) {
	var s Snapshot
	if err := yaml.Unmarshal(data, &s); err != nil {
		return Snapshot{}, fmt.Errorf("parse snapshot: %w", err)
	}
	if s.SchemaVersion > SchemaVersion {
		return Snapshot{}, fmt.Errorf("snapshot schema_version %d is newer than supported %d; update wb", s.SchemaVersion, SchemaVersion)
	}
	return s, nil
}
