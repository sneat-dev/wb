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
	// package patterns the diff touches ("." for the module root,
	// "./internal/foo" otherwise). A directory a diff only ever deletes
	// files out of, leaving nothing behind, is never included: there is no
	// package left to test.
	Packages []string
}

// ChangedPackages computes the Go packages a local diff against target
// touches: staged changes, unstaged changes, and already-committed changes
// since the merge base, all in one pass (spec/plans/coverage-to-100/README.md
// task-19, issue #570). This promotes the pre-commit hook template's shell
// mapping (internal/hooks/config.go, BuiltinGoPreCommit) into a first-class,
// tested Go function `wb run --changed` and `wb coverage --changed` both
// call, instead of each recomputing its own version.
//
// The diff itself is computed the same way GitTouchedFiles' underlying `git
// diff --merge-base <mergeBase>` call already does for `wb coverage
// --changed`: with no --cached, `git diff <ref>` compares ref to the
// CURRENT WORKTREE, which is the index (staged changes) and the on-disk
// files (unstaged changes) together — already committed changes since the
// merge base are naturally included too, since they are already part of
// that worktree state relative to the older merge-base ref. One git
// invocation therefore captures all three kinds of local change; no
// separate --cached pass is needed.
//
// Renames count both their old and new path (GitTouchedFiles uses
// --no-renames for exactly this reason: with rename detection on, git
// prints only the destination path and silently drops the source). A
// package directory that no longer exists in the current worktree (fully
// deleted, or every file moved elsewhere) is dropped, mirroring the hook's
// own existing-directory check — `go test`/`go vet` cannot run against a
// package that is not there, and a hook that tried would fail on the wrong
// thing. Files that do not end in ".go" are ignored; a "_test.go" change
// counts like any other Go file. The module root package maps to ".".
func ChangedPackages(ctx context.Context, repoRoot, target string) (ChangedPackagesResult, error) {
	mergeBase, err := GitMergeBase(ctx, repoRoot, target)
	if err != nil {
		return ChangedPackagesResult{}, err
	}
	touchedFiles, err := GitTouchedFiles(ctx, repoRoot, mergeBase)
	if err != nil {
		return ChangedPackagesResult{}, err
	}
	dirs := goPackageDirsFromFiles(touchedFiles)
	return ChangedPackagesResult{
		Target:    target,
		MergeBase: mergeBase,
		Packages:  existingPackagePatterns(repoRoot, dirs),
	}, nil
}

// goPackageDirsFromFiles maps every touched *.go file (repository-relative
// paths, as GitTouchedFiles returns them) to its package directory,
// dropping any file that does not end in ".go". The module root maps to
// ".". This is the exact mapping EvaluateRatchet uses to decide per-package
// ratchet ownership from the same touched-file set (task-19 cutover: one
// function, not two computations that can drift), narrowed there to Go
// files only for the same reason a hook has no business vetting a package
// because its README changed.
func goPackageDirsFromFiles(touchedFiles map[string]bool) map[string]bool {
	dirs := make(map[string]bool, len(touchedFiles))
	for file := range touchedFiles {
		if !strings.HasSuffix(file, ".go") {
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

// existingPackagePatterns turns repository-relative package directories into
// sorted `go test`/`go vet`-ready patterns ("." or "./internal/foo"),
// dropping any directory the current worktree no longer has — the same rule
// the pre-commit hook template applies before running `go vet` on the
// packages a commit touches (internal/hooks/config.go, BuiltinGoPreCommit).
func existingPackagePatterns(repoRoot string, dirs map[string]bool) []string {
	patterns := make([]string, 0, len(dirs))
	for dir := range dirs {
		if dir == "." {
			patterns = append(patterns, ".")
			continue
		}
		info, err := os.Stat(filepath.Join(repoRoot, dir))
		if err != nil || !info.IsDir() {
			continue
		}
		patterns = append(patterns, "./"+dir)
	}
	sort.Strings(patterns)
	return patterns
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
