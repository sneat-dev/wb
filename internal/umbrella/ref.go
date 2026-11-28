// Package umbrella implements registry-agnostic umbrella references (<registry>:<id>),
// the provider interface, the GitHub provider, and formatting helpers for commit
// trailers, pull-request body links, status comments, and closing summaries.
package umbrella

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/sneat-dev/wb/internal/repopath"
)

const (
	// DefaultRegistry is used when an umbrella reference omits "<registry>:"
	// and neither fleet nor repository configuration specifies another registry.
	DefaultRegistry = "github"

	// RegistryGitHub is the built-in GitHub Issues umbrella registry.
	RegistryGitHub = "github"

	// RegistrySynchestra is the planned Synchestra umbrella registry.
	// TODO(sneat-dev/wb#1027): implement the Synchestra umbrella provider in a follow-up issue.
	RegistrySynchestra = "synchestra"

	// TrailerKey is the Git commit trailer key recorded on commits made by WB verbs.
	TrailerKey = "Wb-Umbrella"
)

var (
	registryNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	digitsPattern       = regexp.MustCompile(`^[1-9][0-9]*$`)
)

// Ref is a typed umbrella reference "<registry>:<id>".
type Ref struct {
	Registry string `json:"registry"`
	ID       string `json:"id"`
}

// IsZero reports whether the reference is unset.
func (r Ref) IsZero() bool {
	return r.Registry == "" && r.ID == ""
}

// String returns the canonical typed reference "<registry>:<id>".
func (r Ref) String() string {
	if r.IsZero() {
		return ""
	}
	return r.Registry + ":" + r.ID
}

// Display returns the compact human reference: "owner/repo#N" for GitHub and
// "<registry>:<id>" for other registries.
func (r Ref) Display() string {
	if r.IsZero() {
		return ""
	}
	if r.Registry == RegistryGitHub {
		return r.ID
	}
	return r.String()
}

// ValidateRegistryName checks that a registry identifier is lowercase ASCII
// starting with a letter and containing only letters, digits, hyphens, or underscores.
func ValidateRegistryName(name string) error {
	if strings.TrimSpace(name) != name || !registryNamePattern.MatchString(name) {
		return fmt.Errorf("invalid umbrella registry %q; use lowercase letters, digits, hyphens, or underscores starting with a letter", name)
	}
	return nil
}

// HasExplicitRegistry reports whether raw includes an explicit "<registry>:"
// prefix and therefore does not depend on a configured default registry.
func HasExplicitRegistry(raw string) bool {
	return strings.Contains(strings.TrimSpace(raw), ":")
}

// Parse parses a typed "<registry>:<id>" reference or a bare "<id>". Bare
// GitHub-shaped references ("owner/repo#N") always resolve to RegistryGitHub;
// other bare identifiers use defaultRegistry (or DefaultRegistry when empty).
func Parse(raw string, defaultRegistry string) (Ref, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Ref{}, fmt.Errorf("umbrella reference must not be empty")
	}
	if strings.ContainsAny(trimmed, " \t\r\n\x00") {
		return Ref{}, fmt.Errorf("umbrella reference %q must not contain whitespace or control characters", raw)
	}
	def := strings.ToLower(strings.TrimSpace(defaultRegistry))
	if def == "" {
		def = DefaultRegistry
	} else if err := ValidateRegistryName(def); err != nil {
		return Ref{}, fmt.Errorf("default umbrella registry: %w", err)
	}

	var registry, id string
	if before, after, found := strings.Cut(trimmed, ":"); found {
		registry = strings.ToLower(before)
		id = after
		if registry == "http" || registry == "https" {
			return Ref{}, fmt.Errorf("umbrella reference %q must use <registry>:<id> (for GitHub, owner/repo#N or github:owner/repo#N), not a URL", raw)
		}
		if err := ValidateRegistryName(registry); err != nil {
			return Ref{}, err
		}
		if id == "" {
			return Ref{}, fmt.Errorf("umbrella reference %q is missing its identifier after %q", raw, registry+":")
		}
	} else if _, _, _, ghErr := ParseGitHubID(trimmed); ghErr == nil {
		registry = RegistryGitHub
		id = trimmed
	} else {
		registry = def
		id = trimmed
	}

	if registry == RegistryGitHub {
		owner, repo, number, err := ParseGitHubID(id)
		if err != nil {
			return Ref{}, err
		}
		id = fmt.Sprintf("%s/%s#%d", owner, repo, number)
	}

	return Ref{Registry: registry, ID: id}, nil
}

// ParseGitHubID validates a GitHub issue identifier "owner/repo#N".
func ParseGitHubID(id string) (owner, repo string, number int, err error) {
	slug, numStr, hasHash := strings.Cut(id, "#")
	if !hasHash || strings.Contains(numStr, "#") {
		return "", "", 0, fmt.Errorf("GitHub umbrella %q must have the form owner/repo#N", id)
	}
	owner, repo, hasSlash := strings.Cut(slug, "/")
	if !hasSlash || strings.Contains(repo, "/") {
		return "", "", 0, fmt.Errorf("GitHub umbrella %q must have the form owner/repo#N", id)
	}
	if !repopath.SafeSegment(owner, false) || !repopath.SafeSegment(repo, true) {
		return "", "", 0, fmt.Errorf("GitHub umbrella %q has invalid repository %q", id, slug)
	}
	if !digitsPattern.MatchString(numStr) {
		return "", "", 0, fmt.Errorf("GitHub umbrella %q must have a positive issue number", id)
	}
	parsed, convErr := strconv.Atoi(numStr)
	if convErr != nil || parsed <= 0 {
		return "", "", 0, fmt.Errorf("GitHub umbrella %q must have a positive issue number", id)
	}
	return owner, repo, parsed, nil
}
