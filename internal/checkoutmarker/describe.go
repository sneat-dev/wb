package checkoutmarker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/agentguard"
)

// Inspection is a described checkout together with the exclude file that keeps
// its marker out of git status.
type Inspection struct {
	Descriptor  Descriptor
	ExcludePath string
}

// DescribeOptions configures Describe.
type DescribeOptions struct {
	// ProjectsRoot is the directory holding {owner}/{repository} clones.
	ProjectsRoot string
	// BaseBranch is the protected branch a canonical clone must stay on.
	BaseBranch string
	// Version identifies the WB build that generated the marker.
	Version string
	// Now supplies the timestamp; nil means time.Now.
	Now func() time.Time
}

// Describe resolves what a checkout is, using filesystem reads only.
//
// No Git process is started. Everything the marker states is already on disk:
// the shape of `.git` says whether the checkout is primary or linked, the
// `gitdir:` pointer says which canonical clone a worktree belongs to, and HEAD
// says which branch it is on. Keeping it process-free is what lets `wb sync`
// and `wb worktree create` refresh markers across the whole fleet without
// paying for a fork per checkout.
func Describe(path string, options DescribeOptions) (Inspection, error) {
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	baseBranch := options.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Inspection{}, fmt.Errorf("resolve %s: %w", path, err)
	}
	absolute = filepath.Clean(absolute)

	location := agentguard.Classify(options.ProjectsRoot, absolute)
	switch location.Kind {
	case agentguard.KindCanonical:
		return describeCanonical(location, options.ProjectsRoot, baseBranch, options.Version, now())
	case agentguard.KindLinked:
		return describeWorktree(location, options, baseBranch, now())
	default:
		return Inspection{}, fmt.Errorf(
			"%s is not a WB-managed checkout: no canonical clone under %s and no linked worktree encloses it",
			absolute, options.ProjectsRoot,
		)
	}
}

func describeCanonical(location agentguard.Location, projectsRoot, baseBranch, version string, now time.Time) (Inspection, error) {
	gitDir := filepath.Join(location.Root, ".git")
	// State the clone by the path an operator types, not by whichever of its
	// equivalent forms happened to reach this call. Without this, the same
	// clone reached once as <projects-root>/owner/name and once as the
	// physical path Git reports produces two different markers, and a fleet
	// sweep rewrites the file on every run forever.
	checkoutPath := filepath.Join(projectsRoot, location.Owner, location.Repository)
	return Inspection{
		Descriptor: Descriptor{
			Kind:          KindCanonical,
			Writable:      false,
			Repository:    location.Slug(),
			CheckoutPath:  checkoutPath,
			CanonicalPath: checkoutPath,
			Branch:        readHeadBranch(gitDir),
			BaseBranch:    baseBranch,
			GeneratedAt:   now,
			GeneratedBy:   version,
		},
		ExcludePath: filepath.Join(gitDir, "info", "exclude"),
	}, nil
}

func describeWorktree(location agentguard.Location, options DescribeOptions, baseBranch string, now time.Time) (Inspection, error) {
	gitDir, err := linkedGitDir(location.Root)
	if err != nil {
		return Inspection{}, err
	}
	commonDir := commonDirectoryFor(gitDir)
	canonicalPath := ""
	repository := ""
	if commonDir != "" && filepath.Base(commonDir) == ".git" {
		canonicalPath = filepath.Dir(commonDir)
		canonicalLocation := agentguard.Classify(options.ProjectsRoot, canonicalPath)
		repository = canonicalLocation.Slug()
		if repository != "" {
			// Git records the PHYSICAL path in its gitdir pointer, so on macOS
			// a clone reached through /Users arrives here as /System/Volumes/…
			// or /private/…. The marker is read by people and agents who know
			// the clone by the path they type, so state it in the projects-root
			// form rather than the one the filesystem happens to resolve to.
			canonicalPath = filepath.Join(options.ProjectsRoot, canonicalLocation.Owner, canonicalLocation.Repository)
		}
	}
	task, worktreesRoot := taskCoordinates(location.Root, repository)
	excludePath := ""
	if commonDir != "" {
		excludePath = filepath.Join(commonDir, "info", "exclude")
	}
	return Inspection{
		Descriptor: Descriptor{
			Kind:          KindWorktree,
			Writable:      true,
			Repository:    repository,
			CheckoutPath:  location.Root,
			CanonicalPath: canonicalPath,
			Branch:        readHeadBranch(gitDir),
			BaseBranch:    baseBranch,
			Task:          task,
			WorktreesRoot: worktreesRoot,
			GeneratedAt:   now,
			GeneratedBy:   options.Version,
		},
		ExcludePath: excludePath,
	}, nil
}

// linkedGitDir follows the `gitdir:` pointer a linked worktree keeps in its
// `.git` file.
func linkedGitDir(root string) (string, error) {
	contents, err := os.ReadFile(filepath.Join(root, ".git"))
	if err != nil {
		return "", fmt.Errorf("read the worktree pointer in %s: %w", root, err)
	}
	for _, line := range strings.Split(string(contents), "\n") {
		value, found := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
		if !found {
			continue
		}
		target := strings.TrimSpace(value)
		if target == "" {
			break
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(root, target)
		}
		return filepath.Clean(target), nil
	}
	return "", fmt.Errorf("%s holds no gitdir pointer", filepath.Join(root, ".git"))
}

// commonDirectoryFor turns <common>/worktrees/<name> into <common>. Anything
// that does not have that shape yields "", which the caller reports as an
// incomplete description rather than guessing.
func commonDirectoryFor(gitDir string) string {
	parent := filepath.Dir(gitDir)
	if filepath.Base(parent) != "worktrees" {
		return ""
	}
	return filepath.Dir(parent)
}

// taskCoordinates recovers the task name and worktrees root from a managed
// worktree's own path, which WB lays out as
// <worktrees-root>/<task>/<owner>/<repository>. A checkout somewhere else —
// an adopted worktree that was deliberately never relocated — reports no task
// rather than a wrong one.
func taskCoordinates(root, repository string) (task, worktreesRoot string) {
	if repository == "" {
		return "", ""
	}
	owner, name, found := strings.Cut(repository, "/")
	if !found {
		return "", ""
	}
	repositoryDirectory := filepath.Dir(root)
	ownerDirectory := filepath.Dir(repositoryDirectory)
	if filepath.Base(root) != name || filepath.Base(repositoryDirectory) != owner {
		return "", ""
	}
	return filepath.Base(ownerDirectory), filepath.Dir(ownerDirectory)
}

// readHeadBranch reads the checked-out branch straight out of HEAD. A detached
// HEAD yields "", which the marker states as an empty branch rather than
// inventing one.
func readHeadBranch(gitDir string) string {
	contents, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(contents))
	reference, found := strings.CutPrefix(head, "ref:")
	if !found {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(reference), "refs/heads/")
}
