package quality

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// ChangedPackagesResult is the outcome of computing which Go packages a
// local diff touched against a resolved merge base.
type ChangedPackagesResult struct {
	// Target is the branch or ref the caller asked to diff against.
	Target string
	// MergeBase is the resolved merge-base commit of HEAD and Target.
	MergeBase string
	// Packages is the sorted, deduplicated list of `go test`/`go vet`-ready
	// package patterns the diff touches ("." for workingDir's own module
	// root, "./internal/foo" or "../sibling" otherwise, always relative to
	// workingDir). A directory outside the Go module that contains
	// workingDir, a directory the diff empties out entirely (fully deleted
	// or moved away), and a directory the go tool itself would never build
	// (no buildable *.go file left in it, a "testdata" directory, or a
	// "_"/"."-prefixed path segment) are never included.
	Packages []string
}

// ChangedPackages computes the Go packages a local diff against target
// touches, scoped to the Go module that contains workingDir (the nearest
// go.mod at or above workingDir, up to the repository root) and expressed
// as patterns relative to workingDir itself — exactly what a human typing
// `go test ./...` from that directory would need, whether workingDir is a
// repository's top level or a nested module root such as
// "<product>/backend" (the fleet's common layout).
//
// It captures staged changes, unstaged changes, and already-committed
// changes since the merge base, all in one pass
// (spec/plans/coverage-to-100/README.md task-19, issue #570). This promotes
// the pre-commit hook template's shell mapping (internal/hooks/config.go,
// BuiltinGoPreCommit) into a first-class, tested Go function `wb run
// --changed` calls.
//
// The diff itself is computed the same way GitTouchedFiles' underlying `git
// diff --merge-base <mergeBase>` call already does for `wb coverage
// --changed`: with no --cached, `git diff <ref>` compares ref to the
// CURRENT WORKTREE, which is the index (staged changes) and the on-disk
// files (unstaged changes) together — already committed changes since the
// merge base are naturally included too, since they are already part of
// that worktree state relative to the older merge-base ref. One git
// invocation therefore captures all three kinds of local change; no
// separate --cached pass is needed. Git always reports these paths relative
// to the repository's top level, never relative to workingDir, which is why
// this function resolves that top level itself (GitTopLevel) instead of
// treating workingDir as if it already were that root.
//
// Untracked files (never added to Git at all) are not included: `git diff`
// itself never reports them, staged, unstaged, or committed.
//
// Renames count both their old and new path (GitTouchedFiles uses
// --no-renames for exactly this reason: with rename detection on, git
// prints only the destination path and silently drops the source). Files
// that do not end in ".go" are ignored; a "_test.go" change counts like any
// other Go file. The module root package maps to ".".
func ChangedPackages(ctx context.Context, workingDir, target string) (ChangedPackagesResult, error) {
	workingDir, err := filepath.Abs(workingDir)
	if err != nil {
		return ChangedPackagesResult{}, err
	}
	gitRoot, err := GitTopLevel(ctx, workingDir)
	if err != nil {
		return ChangedPackagesResult{}, err
	}
	moduleRoot, err := findModuleRoot(workingDir, gitRoot)
	if err != nil {
		return ChangedPackagesResult{}, err
	}
	mergeBase, err := GitMergeBase(ctx, gitRoot, target)
	if err != nil {
		return ChangedPackagesResult{}, err
	}
	touchedFiles, err := GitTouchedFiles(ctx, gitRoot, mergeBase)
	if err != nil {
		return ChangedPackagesResult{}, err
	}
	dirs := packageDirsFromFiles(touchedFiles, true)
	return ChangedPackagesResult{
		Target:    target,
		MergeBase: mergeBase,
		Packages:  changedCommandPatterns(gitRoot, moduleRoot, workingDir, dirs),
	}, nil
}

// packageDirsFromFiles maps every touched file (repository-top-level-relative
// paths, as GitTouchedFiles returns them) to its package directory, using
// the same rule regardless of caller: path.Dir, with the repository root
// itself mapping to ".". When goOnly is true, a file that does not end in
// ".go" is ignored — the scope `wb run --changed` needs, matching the
// pre-commit hook template's own `-- '*.go'` filter, since only Go source
// decides what `go test`/`go vet` should run over. When goOnly is false,
// every touched file decides package ownership, matching
// EvaluateRatchet's pre-task-19 behaviour and founder decision 11
// (spec/plans/coverage-to-100/README.md task-3): a table-test fixture, an
// embedded (go:embed) asset, a .s/.c/.syso file, or go.mod/go.sum itself
// can change a package's coverage without any *.go file in the diff, so the ratchet must
// still count that package as changed. This one function is the single
// place either caller computes the mapping, so the two can never drift
// silently (task-19 cutover) while still disagreeing, deliberately, on
// which files count.
func packageDirsFromFiles(files map[string]bool, goOnly bool) map[string]bool {
	dirs := make(map[string]bool, len(files))
	for file := range files {
		if goOnly && !strings.HasSuffix(file, ".go") {
			continue
		}
		dir := path.Dir(file)
		if dir == "." || dir == "" {
			dirs["."] = true
			continue
		}
		dirs[dir] = true
	}
	return dirs
}

// changedCommandPatterns turns repository-top-level-relative package
// directories into sorted `go test`/`go vet`-ready patterns relative to
// workingDir, applying every exclusion `wb run --changed` needs:
//   - outside the Go module that contains workingDir (moduleRoot) — a
//     sibling module elsewhere in the same repository is not this
//     invocation's concern;
//   - the directory no longer exists at all (fully deleted, or every file
//     moved elsewhere) — mirrors the pre-commit hook template's own
//     `[ -d "$package" ]` check; note a `git mv` leaves its old, now-empty
//     directory behind on disk, so that directory still counts as
//     "existing" here, exactly as the hook's own shell test would see it;
//   - the directory exists but has no buildable *.go file left in it (for
//     example a `git mv` that moved out the last *.go file but left a
//     README behind) — `go test`/`go vet` fail outright ("no Go files") on
//     a directory like that, which is not a diagnosis of anything the diff
//     did;
//   - the directory is one the go tool itself always ignores: a
//     "testdata" path segment, or a "_"/"."-prefixed path segment.
func changedCommandPatterns(gitRoot, moduleRoot, workingDir string, dirs map[string]bool) []string {
	patterns := make([]string, 0, len(dirs))
	for dir := range dirs {
		absoluteDir := gitRoot
		if dir != "." {
			absoluteDir = filepath.Join(gitRoot, dir)
		}
		moduleRelativeDir, ok := moduleRelativePath(moduleRoot, absoluteDir)
		if !ok {
			continue
		}
		info, err := os.Stat(absoluteDir)
		if err != nil || !info.IsDir() {
			continue
		}
		if hasGoToolIgnoredSegment(moduleRelativeDir) {
			continue
		}
		if !hasBuildableGoFile(absoluteDir) {
			continue
		}
		// relativePattern's error is unreachable here: workingDir is always
		// absolute (ChangedPackages resolves it with filepath.Abs before
		// anything else) and absoluteDir is always absolute too (joined
		// from gitRoot, itself GitTopLevel's absolute output), and
		// filepath.Rel only ever fails between paths of different
		// absoluteness. relativePattern's own error branch is exercised
		// directly in changed_packages_test.go against a deliberately
		// mismatched pair.
		pattern, _ := relativePattern(workingDir, absoluteDir)
		patterns = append(patterns, pattern)
	}
	sort.Strings(patterns)
	return patterns
}

// hasGoToolIgnoredSegment reports whether any path segment of a module-root
// relative directory is one the go tool itself always ignores: a directory
// literally named "testdata", or one whose name starts with "_" or ".".
func hasGoToolIgnoredSegment(moduleRelativeDir string) bool {
	if moduleRelativeDir == "." {
		return false
	}
	for _, segment := range strings.Split(filepath.ToSlash(moduleRelativeDir), "/") {
		if segment == "testdata" || strings.HasPrefix(segment, "_") || strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}

// hasBuildableGoFile reports whether absoluteDir currently contains at
// least one "*.go" file (test or not) — the minimum the go tool needs to
// treat it as a package at all, rather than fail with "no Go files in
// <dir>" against a directory a diff left behind empty of Go source.
func hasBuildableGoFile(absoluteDir string) bool {
	entries, err := os.ReadDir(absoluteDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			return true
		}
	}
	return false
}

// moduleRelativePath returns absoluteDir's path relative to moduleRoot, and
// reports ok=false when absoluteDir is outside moduleRoot (including when
// filepath.Rel itself cannot relate the two — only possible between paths
// of different absoluteness, which never occurs through ChangedPackages'
// own call site; exercised directly in changed_packages_test.go). Computing
// this once, here, means "is absoluteDir inside the module" and "what is
// its module-relative path" can never disagree with each other the way two
// separate filepath.Rel calls over the same inputs safely could not anyway,
// but redundantly.
func moduleRelativePath(moduleRoot, absoluteDir string) (rel string, ok bool) {
	rel, err := filepath.Rel(moduleRoot, absoluteDir)
	if err != nil {
		return "", false
	}
	if rel == "." {
		return rel, true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// relativePattern renders absoluteDir as a `go test`/`go vet`-ready pattern
// relative to workingDir: "." for workingDir itself, "./..." for a
// descendant, and a plain ".."-prefixed path (which the go tool accepts
// directly, with no "./" needed) for a directory reached by walking up from
// workingDir first.
func relativePattern(workingDir, absoluteDir string) (string, error) {
	rel, err := filepath.Rel(workingDir, absoluteDir)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	switch {
	case rel == ".":
		return ".", nil
	case rel == "..", strings.HasPrefix(rel, "../"):
		return rel, nil
	default:
		return "./" + rel, nil
	}
}

// findModuleRoot walks upward from startDir looking for the nearest go.mod,
// never searching above ceilingDir (the repository's top level — searching
// further would leave the repository entirely). It returns an error when no
// go.mod exists anywhere in that range, so a repository or subtree with no
// Go module at all fails closed instead of silently reporting no changes.
func findModuleRoot(startDir, ceilingDir string) (string, error) {
	dir := startDir
	for {
		if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !info.IsDir() {
			return dir, nil
		}
		if dir == ceilingDir {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("no go.mod found from %s up to the repository root %s", startDir, ceilingDir)
}

// GitTopLevel resolves the repository's top-level working-tree directory
// containing dir. Every path GitTouchedFiles/GitChangedLines/GitLineOffsets
// report is relative to this directory, never to whatever directory the
// caller happened to invoke Git from — callers that need those paths
// relative to a different directory (a nested Go module root, or the
// caller's own working directory) must rebase them explicitly; see
// ChangedPackages for that rebasing.
func GitTopLevel(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git rev-parse --show-toplevel: %w: %s", err, string(exitErr.Stderr))
		}
		return "", fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// GitMergeBase resolves the merge-base commit of HEAD and target inside
// repoRoot. It is the base resolution `wb coverage --changed --target` and
// `wb run --changed --target` both use, so the two never disagree about
// which commit a diff is measured against.
func GitMergeBase(ctx context.Context, repoRoot, target string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "merge-base", "HEAD", target)
	cmd.Dir = repoRoot
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git merge-base HEAD %s: %w: %s", target, err, string(exitErr.Stderr))
		}
		return "", fmt.Errorf("git merge-base HEAD %s: %w", target, err)
	}
	return strings.TrimSpace(string(output)), nil
}
