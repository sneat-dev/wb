// Package worktreelayout owns repository-address and checkout-path grammar.
// It sits below the worktrees facade and reuses repopath's literal host and
// segment rules without importing the facade's lifecycle policy.
package worktreelayout

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/repopath"
)

// Path returns the checkout path selected by a resolved placement.
func Path(root string, repositoryLocal bool, cloneRelative, task, repository string) (string, error) {
	address, err := SplitRepositoryAddress(repository)
	if err != nil {
		return "", err
	}
	if !ValidSafeSegment(task) {
		return "", fmt.Errorf("invalid worktree task %q", task)
	}
	if repositoryLocal {
		return filepath.Join(root, task), nil
	}
	relative := strings.Trim(strings.TrimSpace(cloneRelative), "/")
	if relative == "" {
		relative = address.Relative()
	}
	if !ValidCloneRelative(relative) {
		return "", fmt.Errorf("canonical clone address %q is not a safe relative path", relative)
	}
	return filepath.Join(root, task, filepath.FromSlash(relative)), nil
}

// ValidSafeSegment accepts an owner or task segment without a leading dot.
func ValidSafeSegment(value string) bool { return repopath.SafeSegment(value, false) }

// ValidRepositorySegment also accepts a leading dot for a repository name.
func ValidRepositorySegment(value string) bool { return repopath.SafeSegment(value, true) }

// ValidCloneRelative accepts exactly owner/repository or host/owner/repository.
func ValidCloneRelative(relative string) bool {
	parts := strings.Split(relative, "/")
	if len(parts) == 3 {
		if !repopath.IsForgeHost(parts[0]) {
			return false
		}
		parts = parts[1:]
	}
	if len(parts) != 2 {
		return false
	}
	return ValidSafeSegment(parts[0]) && ValidRepositorySegment(parts[1])
}

// SplitCloneRelative splits a clone address without validating it. It retains
// the legacy trim behavior used by callers with already resolved placements.
func SplitCloneRelative(relative string) (parent, repository string) {
	relative = strings.Trim(strings.TrimSpace(relative), "/")
	index := strings.LastIndex(relative, "/")
	if index < 0 {
		return "", relative
	}
	return relative[:index], relative[index+1:]
}

// SplitRepository validates a coordinate and returns its owner and name.
func SplitRepository(repository string) (owner, name string, err error) {
	address, err := SplitRepositoryAddress(repository)
	if err != nil {
		return "", "", err
	}
	return address.Org, address.Repo, nil
}

// SplitRepositoryAddress accepts the legacy two-level coordinate or a literal
// forge-host coordinate. Surrounding whitespace is never silently trimmed.
func SplitRepositoryAddress(repository string) (repopath.Address, error) {
	if repository != strings.TrimSpace(repository) {
		return repopath.Address{}, fmt.Errorf("repository %q must not have surrounding whitespace", repository)
	}
	parts := strings.Split(repository, "/")
	switch len(parts) {
	case 2:
		if !ValidSafeSegment(parts[0]) || !ValidRepositorySegment(parts[1]) {
			return repopath.Address{}, fmt.Errorf("repository %q must be owner/name using safe path segments", repository)
		}
		return repopath.Address{Org: parts[0], Repo: parts[1]}, nil
	case 3:
		address, err := repopath.ParseRelative(repository)
		if err != nil {
			return repopath.Address{}, fmt.Errorf("repository %q must be host/owner/name with a literal forge hostname: %w", repository, err)
		}
		return address, nil
	default:
		return repopath.Address{}, fmt.Errorf("repository %q must be owner/name or host/owner/name using safe path segments", repository)
	}
}

// CanonicalRepositoryPath resolves an address to its existing or predicted
// canonical clone path below projectsRoot.
func CanonicalRepositoryPath(projectsRoot, repository string) (owner, name, canonical string, err error) {
	address, err := SplitRepositoryAddress(repository)
	if err != nil {
		return "", "", "", err
	}
	resolved, err := ResolveCanonicalClone(projectsRoot, address)
	if err != nil {
		return "", "", "", err
	}
	return resolved.Org, resolved.Repo, resolved.Path(projectsRoot), nil
}

// ResolveCanonicalClone honors a specified host and otherwise locates an
// existing clone, leaving unqualified missing coordinates at the legacy path.
func ResolveCanonicalClone(projectsRoot string, address repopath.Address) (repopath.Address, error) {
	if address.Host != "" {
		return address, nil
	}
	return repopath.Locate(projectsRoot, address.Org, address.Repo)
}

// CanonicalCoordinates identifies the exact two- or three-level placement of
// a canonical clone below projectsRoot.
func CanonicalCoordinates(projectsRoot, root string) (host, owner, name string, err error) {
	relative, err := filepath.Rel(projectsRoot, root)
	if err != nil {
		return "", "", "", err
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) == 3 {
		if !repopath.IsForgeHost(parts[0]) {
			return "", "", "", fmt.Errorf("canonical clone %s must be at <projects-root>/{host}/{owner}/{repository} with the literal forge hostname as its first level", root)
		}
		host = parts[0]
		parts = parts[1:]
	}
	if len(parts) != 2 {
		return "", "", "", fmt.Errorf("canonical clone %s must be at <projects-root>/{host}/{owner}/{repository}", root)
	}
	owner, name, err = SplitRepository(strings.Join(parts, "/"))
	if err != nil {
		return "", "", "", err
	}
	return host, owner, name, nil
}
