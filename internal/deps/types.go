// Package deps coordinates exact dependency updates across isolated repository
// worktrees. Ecosystem adapters own discovery and mutation; the runner owns Git,
// verification, publication, and deterministic reports.
package deps

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/quality"
)

// Ecosystem identifies a dependency manifest or reference format.
type Ecosystem string

const (
	EcosystemGitHubActions Ecosystem = "github-actions"
	EcosystemGo            Ecosystem = "go"
)

// Target is the exact dependency identity and version requested by the user.
type Target struct {
	Ecosystem  Ecosystem `yaml:"ecosystem"`
	Dependency string    `yaml:"dependency"`
	Version    string    `yaml:"version"`
	Resolved   string    `yaml:"resolved,omitempty"`
}

// ParseTarget validates a command target such as strongo/cicd@v1.10.5.
func ParseTarget(ecosystem, value string) (Target, error) {
	target := Target{Ecosystem: Ecosystem(strings.TrimSpace(ecosystem))}
	switch target.Ecosystem {
	case EcosystemGitHubActions, EcosystemGo:
	default:
		return Target{}, fmt.Errorf("unsupported dependency ecosystem %q (want github-actions or go)", ecosystem)
	}
	at := strings.LastIndex(value, "@")
	if at <= 0 || at == len(value)-1 {
		return Target{}, fmt.Errorf("invalid dependency target %q (want fully-qualified-dependency@version)", value)
	}
	target.Dependency = strings.TrimSpace(value[:at])
	target.Version = strings.TrimSpace(value[at+1:])
	if target.Dependency == "" || target.Version == "" {
		return Target{}, fmt.Errorf("invalid dependency target %q (want fully-qualified-dependency@version)", value)
	}
	if target.Ecosystem == EcosystemGitHubActions {
		if matched, _ := regexp.MatchString(`^[^/\s]+/[^/\s]+$`, target.Dependency); !matched {
			return Target{}, fmt.Errorf("GitHub Actions dependency %q must be a full owner/repository identity", target.Dependency)
		}
	}
	return target, nil
}

// Repository identifies a canonical clone selected by command-level discovery.
type Repository struct {
	Slug     string
	Path     string
	CloneURL string
	Archived bool
}

// Options controls repository isolation, verification, and optional publishing.
type Options struct {
	GitHubDir      string
	Ref            string
	Parallel       int
	DryRun         bool
	Resume         bool
	AllowDowngrade bool
	Verify         bool
	Checks         []quality.Check
	Timeout        time.Duration
	Retry          int
	Commit         bool
	Push           bool
	PR             bool
	Merge          bool
	ReportDir      string

	// ResolveGitHubRef is injectable for hermetic adapter tests.
	ResolveGitHubRef func(context.Context, string, string) (string, error)
}

// Report is the stable Markdown/YAML index for one exact-set operation.
type Report struct {
	SchemaVersion int                `yaml:"schema_version"`
	Operation     string             `yaml:"operation"`
	Status        string             `yaml:"status"`
	Target        Target             `yaml:"target"`
	GitHubDir     string             `yaml:"github_dir"`
	BaseRef       string             `yaml:"base_ref"`
	Verification  []quality.Check    `yaml:"verification,omitempty"`
	Parallel      int                `yaml:"parallel"`
	Repositories  []RepositoryReport `yaml:"repositories"`
}

// RepositoryReport records one selected repository and every external stage.
type RepositoryReport struct {
	Repository    string                      `yaml:"repository"`
	CanonicalDir  string                      `yaml:"canonical_dir,omitempty"`
	WorktreeDir   string                      `yaml:"worktree_dir,omitempty"`
	Branch        string                      `yaml:"branch,omitempty"`
	Ref           string                      `yaml:"ref"`
	Status        string                      `yaml:"status"`
	Reason        string                      `yaml:"reason"`
	Decisions     []Decision                  `yaml:"decisions,omitempty"`
	ChangedFiles  []string                    `yaml:"changed_files,omitempty"`
	Verifications []quality.VerificationEntry `yaml:"verifications,omitempty"`
	Commit        string                      `yaml:"commit,omitempty"`
	Pushed        bool                        `yaml:"pushed,omitempty"`
	PR            string                      `yaml:"pr,omitempty"`
	Checks        []RemoteCheck               `yaml:"checks,omitempty"`
	Merged        bool                        `yaml:"merged,omitempty"`
}

// Decision explains one existing dependency reference before and after update.
type Decision struct {
	File          string `yaml:"file"`
	BeforeRef     string `yaml:"before_ref,omitempty"`
	BeforeVersion string `yaml:"before_version,omitempty"`
	TargetVersion string `yaml:"target_version"`
	ResolvedRef   string `yaml:"resolved_ref,omitempty"`
	AfterRef      string `yaml:"after_ref,omitempty"`
	AfterVersion  string `yaml:"after_version,omitempty"`
	Action        string `yaml:"action"`
	Reason        string `yaml:"reason"`
}

// RemoteCheck is the normalized GitHub check state observed before merge.
type RemoteCheck struct {
	Name   string `json:"name" yaml:"name"`
	Bucket string `json:"bucket" yaml:"bucket"`
	Link   string `json:"link,omitempty" yaml:"link,omitempty"`
}

func sortRepositoryReport(report *RepositoryReport) {
	sort.Strings(report.ChangedFiles)
	sort.Slice(report.Decisions, func(i, j int) bool {
		if report.Decisions[i].File == report.Decisions[j].File {
			return report.Decisions[i].BeforeRef < report.Decisions[j].BeforeRef
		}
		return report.Decisions[i].File < report.Decisions[j].File
	})
}
