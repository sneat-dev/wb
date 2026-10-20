package worktrees

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/repopath"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// SourceListOptions identifies one reviewed remote source, independently of
// WB task names, placement, and the fleet inventory's substring filter.
type SourceListOptions struct {
	ProjectsRoot string
	Repository   string
	Branch       string
	HeadSHA      string
	Runner       runner.Runner
}

type sourceRegistration struct {
	path, head, branch string
	detached           bool
}

// ListSourceWithDiagnostics reads the source repository's Git registry first.
// Only its exact branch and detached/unknown potential producers are inspected.
// Unregistered residue remains visible to ListWithDiagnostics, but cannot
// become source evidence merely by occupying a task-shaped directory.
func ListSourceWithDiagnostics(ctx context.Context, options SourceListOptions) (ListOutcome, error) {
	outcome := ListOutcome{SchemaVersion: 1}
	root, err := absoluteProjectsRoot(options.ProjectsRoot)
	if err != nil {
		return outcome, err
	}
	if !sourceObjectID(options.HeadSHA) || !validBranch(ctx, options.Branch) {
		return outcome, fmt.Errorf("source branch and full head SHA must be established")
	}
	canonicalPath, err := sourceCanonicalPath(root, options.Repository)
	if err != nil || canonicalPath == "" {
		return outcome, err
	}
	home, err := wbhome.Root(root)
	if err != nil {
		return outcome, err
	}
	canonical, err := openCanonicalRepository(canonicalPath)
	if err != nil {
		return outcome, fmt.Errorf("open source canonical repository: %w", err)
	}
	defer canonical.close()
	commonIdentity, commonErr := canonical.common.Stat()
	rootIdentity, rootErr := canonical.root.Stat()
	identity, err := sourceGit(ctx, options, canonicalPath, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir")
	fields := strings.Split(strings.TrimSpace(identity), "\n")
	if commonErr != nil || rootErr != nil || err != nil || len(fields) != 3 || !sourceSameDirectory(fields[0], rootIdentity) || !sourceSameDirectory(fields[1], commonIdentity) || !sourceSameDirectory(fields[2], commonIdentity) {
		return outcome, fmt.Errorf("source canonical Git identity is unavailable or contradictory: %v", errors.Join(commonErr, rootErr, err))
	}
	registry, err := sourceGit(ctx, options, canonicalPath, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return outcome, fmt.Errorf("read source Git worktree registry: %w", err)
	}
	registered, err := parseSourceRegistry(registry)
	if err != nil {
		return outcome, err
	}
	if !sourceSameDirectory(registered[0].path, rootIdentity) {
		return outcome, fmt.Errorf("source Git registry does not identify its canonical checkout")
	}
	var selected []os.FileInfo
	for _, entry := range registered {
		if entry.branch != "" && !validBranch(ctx, entry.branch) {
			return outcome, fmt.Errorf("source Git registry has an invalid branch reference")
		}
		if entry.branch != "" && entry.branch != options.Branch {
			continue
		}
		// Reject physical aliases before reading any producer's claim. A
		// missing directory still reaches the native inspection refusal below.
		identity, _ := os.Stat(entry.path)
		duplicate := false
		for _, previous := range selected {
			duplicate = duplicate || identity != nil && os.SameFile(previous, identity)
		}
		if duplicate {
			outcome.Diagnostics = append(outcome.Diagnostics, listDiagnostic("", "", entry.path, "source Git registry repeats a physical producer"))
			continue
		}
		if identity != nil {
			selected = append(selected, identity)
		}
		result, _, inspectErr := inspectSourceRegistration(ctx, options, root, home, canonicalPath, commonIdentity, entry)
		if inspectErr != nil {
			outcome.Diagnostics = append(outcome.Diagnostics, listDiagnostic("", "", entry.path, inspectErr.Error()))
			continue
		}
		if result == nil {
			continue // consistently clean detached checkout at another commit
		}
		outcome.Results = append(outcome.Results, *result)
	}
	after, err := sourceGit(ctx, options, canonicalPath, "worktree", "list", "--porcelain", "-z")
	if err != nil || after != registry {
		return outcome, fmt.Errorf("source Git worktree registry changed or became unreadable during inspection: %v", err)
	}
	if err := canonical.validate(); err != nil {
		return outcome, err
	}
	return outcome, nil
}

func sourceGit(ctx context.Context, options SourceListOptions, path string, args ...string) (string, error) {
	if options.Runner == nil {
		return git(ctx, path, append([]string{"--no-optional-locks"}, args...)...)
	}
	result, err := options.Runner.RunOpts(ctx, path, runner.RunOptions{Env: console.Env()}, "git", append([]string{"--no-optional-locks"}, args...)...)
	return result.Stdout, err
}

func sourceObjectID(value string) bool {
	_, err := hex.DecodeString(value)
	return len(value) == 40 && err == nil
}

func sourceSameDirectory(path string, identity os.FileInfo) bool {
	info, err := os.Stat(path)
	return filepath.IsAbs(path) && err == nil && info.IsDir() && os.SameFile(info, identity)
}

// Resolve only matching owner/repository names, preserving their spelling.
// Other owners' repositories and task directories are never opened.
func sourceCanonicalPath(root, repository string) (string, error) {
	address, err := splitRepositoryAddress(repository)
	if err != nil {
		return "", err
	}
	levels := []string{root}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !repopath.IsForgeHost(entry.Name()) || address.Host != "" && !strings.EqualFold(address.Host, entry.Name()) {
			continue
		}
		level := filepath.Join(root, entry.Name())
		if entry.IsDir() {
			levels = append(levels, level)
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			info, err := os.Stat(level)
			if err != nil {
				return "", err
			}
			if info.IsDir() {
				levels = append(levels, level)
			}
		}
	}
	var matches []string
	for _, level := range levels {
		if address.Host != "" && level == root {
			continue
		}
		owners, err := os.ReadDir(level)
		if err != nil {
			return "", err
		}
		for _, owner := range owners {
			if !strings.EqualFold(owner.Name(), address.Org) {
				continue
			}
			repositories, err := os.ReadDir(filepath.Join(level, owner.Name()))
			if err != nil {
				return "", err
			}
			for _, candidate := range repositories {
				if strings.EqualFold(candidate.Name(), address.Repo) {
					matches = append(matches, filepath.Join(level, owner.Name(), candidate.Name()))
				}
			}
		}
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("source repository %s has ambiguous canonical placements", repository)
	}
	if len(matches) == 0 {
		return "", nil
	}
	return matches[0], nil
}

func parseSourceRegistry(value string) ([]sourceRegistration, error) {
	if !strings.HasSuffix(value, "\x00\x00") {
		return nil, fmt.Errorf("source Git worktree registry is incomplete")
	}
	var result []sourceRegistration
	seen := map[string]bool{}
	for _, record := range strings.Split(strings.TrimSuffix(value, "\x00\x00"), "\x00\x00") {
		fields := strings.Split(record, "\x00")
		path, known := strings.CutPrefix(fields[0], "worktree ")
		if !known || !filepath.IsAbs(path) || seen[path] {
			return nil, fmt.Errorf("source Git registry has an invalid or duplicate worktree path")
		}
		seen[path] = true
		entry := sourceRegistration{path: path}
		for _, field := range fields[1:] {
			switch {
			case strings.HasPrefix(field, "HEAD ") && entry.head == "":
				entry.head = strings.TrimPrefix(field, "HEAD ")
			case strings.HasPrefix(field, "branch refs/heads/") && entry.branch == "" && !entry.detached:
				entry.branch = strings.TrimPrefix(field, "branch refs/heads/")
				if entry.branch == "" {
					return nil, fmt.Errorf("source Git registry has an empty branch")
				}
			case field == "detached" && !entry.detached && entry.branch == "":
				entry.detached = true
			case field == "locked" || strings.HasPrefix(field, "locked ") || field == "prunable" || strings.HasPrefix(field, "prunable "):
			default:
				return nil, fmt.Errorf("source Git registry has contradictory or unknown fields")
			}
		}
		if !sourceObjectID(entry.head) {
			return nil, fmt.Errorf("source Git registry has an invalid head SHA")
		}
		result = append(result, entry)
	}
	return result, nil
}

func inspectSourceRegistration(ctx context.Context, options SourceListOptions, root, home, canonical string, common os.FileInfo, entry sourceRegistration) (*ListResult, os.FileInfo, error) {
	identity, err := os.Stat(entry.path)
	if err != nil || !identity.IsDir() {
		return nil, nil, fmt.Errorf("source producer directory is unavailable: %v", err)
	}
	args := []string{"rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir", "HEAD", "--abbrev-ref", "HEAD"}
	before, err := sourceGit(ctx, options, entry.path, args...)
	fields := strings.Split(strings.TrimSpace(before), "\n")
	branch := entry.branch
	if entry.detached {
		branch = "HEAD"
	}
	if err != nil || len(fields) != 4 || !sourceSameDirectory(fields[0], identity) || !sourceSameDirectory(fields[1], common) || fields[2] != entry.head || fields[3] != branch || branch == "" {
		return nil, nil, fmt.Errorf("source producer Git identity is unavailable or contradicts its canonical registration: %v", err)
	}
	status, err := sourceGit(ctx, options, entry.path, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return nil, nil, fmt.Errorf("read source producer status: %w", err)
	}
	var result *ListResult
	if entry.detached {
		// A detached checkout at another commit cannot be retired as this
		// branch's source. Its dirty files remain in place and do not affect
		// admission for an independently produced pull request.
		if strings.EqualFold(entry.head, options.HeadSHA) {
			return nil, nil, fmt.Errorf("detached potential source producer is at the PR head")
		}
	} else {
		claim, _, _, err := activeWorkLogClaimReadOnly(home, entry.path)
		if err != nil {
			return nil, nil, fmt.Errorf("corroborate source producer ownership: %w", err)
		}
		// The native claim reader corroborates current placement, including
		// immutable relocation receipts; claim.Worktree may name its old path.
		if !strings.EqualFold(claim.Repository, options.Repository) || claim.Branch != options.Branch {
			return nil, nil, fmt.Errorf("source producer claim does not match its repository, branch, and physical directory")
		}
		layout, external, err := sourceClaimLayout(root, home, entry.path, identity, claim)
		if err != nil {
			return nil, nil, err
		}
		if _, err := resolveRecordedWorktreeBase(ctx, home, entry.path, claim.Base); err != nil {
			return nil, nil, err
		}
		result = &ListResult{Task: claim.Task, Repository: claim.Repository, CanonicalDir: canonical, WorktreeDir: entry.path,
			WorktreesRoot: layout.WorktreesRoot, Branch: entry.branch, HeadSHA: entry.head, Base: claim.Base, Clean: status == "", External: external}
	}
	after, err := sourceGit(ctx, options, entry.path, args...)
	if err != nil || after != before {
		return nil, nil, fmt.Errorf("source producer Git identity changed during inspection: %v", err)
	}
	afterStatus, err := sourceGit(ctx, options, entry.path, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil || afterStatus != status || !sourceSameDirectory(entry.path, identity) || !sourceSameDirectory(fields[1], common) {
		return nil, nil, fmt.Errorf("source producer status or physical identity changed during inspection: %v", err)
	}
	return result, identity, nil
}

func sourceClaimLayout(root, home, path string, identity os.FileInfo, claim workLogClaim) (wbhome.Layout, bool, error) {
	if layout, err := claimedLocalWorktreeLayout(root, path, claim); err == nil {
		return layout, false, nil
	}
	adopted, err := claimLineageWasAdopted(home, claim)
	if err != nil {
		return wbhome.Layout{}, false, err
	}
	if !adopted {
		layout, err := claimedSharedWorktreeLayout(path, claim)
		return layout, false, err
	}
	// claimLineageWasAdopted validated the static repository coordinate.
	owner, repository, _ := splitRepository(claim.Repository)
	layout := wbhome.Layout{Home: home, WorktreesRoot: filepath.Join(home, "worktrees")}
	pointer, ok := readAdoptedWorktreePointer(filepath.Join(layout.WorktreesRoot, claim.Task, owner, repository))
	if !ok || !sourceSameDirectory(pointer, identity) {
		return wbhome.Layout{}, false, fmt.Errorf("source producer adoption registration is unavailable or contradictory")
	}
	return layout, true, nil
}
