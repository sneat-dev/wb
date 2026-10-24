package agents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// Target is one resolved writable directory and the facts that make a later
// diff and a cleanup decision honest.
type Target struct {
	Repository string
	Dir        string
	Branch     string
	Base       string
	BaseSHA    string

	HeadSHA     string
	SnapshotSHA string
	Dirty       bool
	DirtyFiles  []string

	Managed   bool
	Canonical bool

	ClaimPath  string
	ClaimRunID string

	// WorktreeName is the WB task name when the checkout carries a marker.
	WorktreeName string
}

// TargetDeps are the seams target resolution needs. They exist so the whole
// resolution can be exercised without a live Git checkout.
type TargetDeps struct {
	Git        func(ctx context.Context, dir string, args ...string) (string, error)
	ReadFile   func(string) ([]byte, error)
	OriginSlug func(context.Context, string) (string, error)
}

// DefaultTargetDeps returns the production seams.
func DefaultTargetDeps() TargetDeps {
	return TargetDeps{Git: gitOutput, ReadFile: os.ReadFile, OriginSlug: worktrees.OriginSlug}
}

// ResolveTarget inspects a directory a caller wants a worker to write in.
func ResolveTarget(ctx context.Context, path string, allowCanonical, allowDirty bool, deps TargetDeps) (Target, error) {
	if deps.Git == nil {
		deps.Git = gitOutput
	}
	if deps.ReadFile == nil {
		deps.ReadFile = os.ReadFile
	}
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return Target{}, requestErrorf("a worktree path is required")
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return Target{}, requestErrorf("resolve worktree path %q: %v", path, err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return Target{}, requestErrorf("worktree path %q does not exist: %v", absolute, err)
	}
	if !info.IsDir() {
		return Target{}, requestErrorf("worktree path %q is not a directory", absolute)
	}
	toplevel, err := deps.Git(ctx, absolute, "rev-parse", "--show-toplevel")
	if err != nil {
		return Target{}, requestErrorf("%q is not a Git worktree: %v", absolute, err)
	}
	root := strings.TrimSpace(toplevel)
	if root == "" {
		return Target{}, requestErrorf("%q is not a Git worktree", absolute)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err == nil {
		root = resolved
	}
	target := Target{Dir: root}
	if deps.OriginSlug != nil {
		if slug, slugErr := deps.OriginSlug(ctx, root); slugErr == nil {
			target.Repository = slug
		}
	}
	if branch, branchErr := deps.Git(ctx, root, "rev-parse", "--abbrev-ref", "HEAD"); branchErr == nil {
		target.Branch = strings.TrimSpace(branch)
	}
	if head, headErr := deps.Git(ctx, root, "rev-parse", "HEAD"); headErr == nil {
		target.HeadSHA = strings.TrimSpace(head)
	}
	marker, markerErr := readCheckoutMarker(filepath.Join(root, ".worktree.md"), deps.ReadFile)
	if markerErr == nil {
		target.Managed = true
		target.Canonical = marker.kind == "canonical" || !marker.writable
		target.WorktreeName = marker.task
		target.Base = marker.baseBranch
	}
	status, statusErr := deps.Git(ctx, root, "status", "--porcelain")
	if statusErr == nil {
		for _, line := range strings.Split(status, "\n") {
			line = strings.TrimRight(line, "\r")
			if len(line) < 4 {
				continue
			}
			target.Dirty = true
			target.DirtyFiles = append(target.DirtyFiles, strings.TrimSpace(line[3:]))
		}
	}
	if target.Canonical && !allowCanonical {
		return Target{}, requestErrorf("%s is a canonical clone, not a worktree; a writable execution must not run there. Create an isolated checkout with `wb worktree create <task> %s`, or pass --allow-canonical to override deliberately", root, target.Repository)
	}
	if target.Dirty && !allowDirty {
		return Target{}, requestErrorf("%s has %d uncommitted path(s); pass --allow-dirty to run beside existing work (WB never cleans or resets them), or choose a clean worktree: %s", root, len(target.DirtyFiles), strings.Join(boundList(target.DirtyFiles, 5), ", "))
	}
	if target.Dirty {
		// `git stash create` writes a commit object and touches neither the
		// working tree nor the index, so the worker's change can be separated
		// from what was already there.
		if snapshot, snapshotErr := deps.Git(ctx, root, "stash", "create"); snapshotErr == nil {
			target.SnapshotSHA = strings.TrimSpace(snapshot)
		}
	}
	target.BaseSHA = target.SnapshotSHA
	if target.BaseSHA == "" {
		target.BaseSHA = target.HeadSHA
	}
	return target, nil
}

// checkoutMarker is the subset of the generated `.worktree.md` that decides
// whether a directory may be written to.
type checkoutMarker struct {
	kind       string
	writable   bool
	task       string
	baseBranch string
}

// readCheckoutMarker parses the generated marker. An absent marker is an error
// the caller treats as "unmanaged", never as "safe".
func readCheckoutMarker(path string, read func(string) ([]byte, error)) (checkoutMarker, error) {
	raw, err := read(path)
	if err != nil {
		return checkoutMarker{}, err
	}
	marker := checkoutMarker{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || line == "---" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "\"")
		switch strings.TrimSpace(key) {
		case "kind":
			marker.kind = value
		case "writable":
			marker.writable = value == "true"
		case "task":
			marker.task = value
		case "base_branch":
			marker.baseBranch = value
		}
	}
	return marker, nil
}

func boundList(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	return append(append([]string(nil), values[:limit]...), fmt.Sprintf("… (%d more)", len(values)-limit))
}
