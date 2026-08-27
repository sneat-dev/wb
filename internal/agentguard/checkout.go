// Package agentguard refuses agent tool calls that would write into a
// canonical clone.
//
// WB's Git hooks are the wrong layer for this. Every violation observed on
// 2026-08-27 — a `git checkout origin/main -- .` that staged 186 files, a
// `specscore` run that left a whole unlanded lesson untracked — happened
// without ever reaching a commit, so a pre-commit hook could not have seen
// them. The write is the damage. And when a pre-commit hook does fire, it can
// be walked around: the commit that survived that day was made with
// `git -c core.hooksPath=/dev/null commit`.
//
// This package therefore runs one step earlier, from a Claude Code PreToolUse
// hook, and judges the tool call before the tool runs.
//
// # Fail open, without exception
//
// The guard runs on every tool call of every agent on the machine. A guard
// that fails closed would stop the whole fleet on any WB bug, partial
// upgrade, or payload it has not seen before. Every unknown is therefore an
// allow: an unparseable payload, an unrecognised tool, a path that cannot be
// resolved, a shell construct the conservative scanner does not model, a
// panic. Callers must preserve that property — see Inspect.
package agentguard

import (
	"os"
	"path/filepath"
	"strings"
)

// Kind classifies the Git checkout that encloses a path.
type Kind string

const (
	// KindUnknown means no enclosing Git checkout was found, or the answer
	// could not be established. It is always allowed.
	KindUnknown Kind = "unknown"
	// KindCanonical is a primary checkout sitting exactly at
	// <projects-root>/<owner>/<repository>: a WB canonical clone, which must
	// stay clean because every linked worktree in the fleet is cut from it.
	KindCanonical Kind = "canonical"
	// KindLinked is a linked Git worktree — its `.git` is a file pointing at
	// the canonical clone's common directory. This is where work belongs, so
	// it is always allowed.
	KindLinked Kind = "linked"
	// KindForeign is a primary checkout somewhere other than the managed
	// <projects-root>/<owner>/<repository> shape: a scratch clone, a test
	// fixture, a vendored repository. WB has no policy over it, so it is
	// allowed.
	KindForeign Kind = "foreign"
)

// Location is what Classify resolved about a path.
type Location struct {
	Kind Kind
	// Root is the enclosing checkout's root directory, empty for KindUnknown.
	Root string
	// Owner and Repository are set only for KindCanonical.
	Owner      string
	Repository string
}

// Slug returns owner/repository for a canonical clone, or "".
func (l Location) Slug() string {
	if l.Owner == "" || l.Repository == "" {
		return ""
	}
	return l.Owner + "/" + l.Repository
}

// maxAncestorWalk bounds the upward search. A repository root within 64
// directories of a file is every real case; anything deeper is a symlink loop
// or a filesystem the guard should not be walking, and answering "unknown"
// (allow) is the correct outcome there.
const maxAncestorWalk = 64

// Classify names the Git checkout that encloses path, using nothing but
// filesystem metadata.
//
// It deliberately spawns no process. `git rev-parse --git-dir` would answer
// the same question authoritatively, but this runs ahead of every tool call
// of every agent, and a fork+exec per call is a cost the whole fleet pays all
// day. The distinction Git itself draws is visible in a single lstat: a
// primary checkout's `.git` is a directory, a linked worktree's `.git` is a
// regular file holding a `gitdir:` pointer.
//
// The innermost enclosing checkout wins. That is what makes a worktree nested
// inside a canonical clone — Claude Code's own `.claude/worktrees/<name>`, for
// instance — classify as linked and stay writable, even though it is
// physically below a canonical clone root.
//
// path need not exist: a Write creating a new file names a path that does not
// exist yet. Only its ancestors are consulted.
func Classify(projectsRoot, path string) Location {
	absolute, ok := absolutePath(path)
	if !ok {
		return Location{Kind: KindUnknown}
	}
	root, primary, found := enclosingCheckout(absolute)
	if !found {
		return Location{Kind: KindUnknown}
	}
	if !primary {
		return Location{Kind: KindLinked, Root: root}
	}
	owner, repository, managed := canonicalCoordinates(projectsRoot, root)
	if !managed {
		return Location{Kind: KindForeign, Root: root}
	}
	return Location{Kind: KindCanonical, Root: root, Owner: owner, Repository: repository}
}

// enclosingCheckout walks up from path and reports the first directory holding
// a `.git` entry, and whether that entry is a directory (a primary checkout)
// rather than a file (a linked worktree).
func enclosingCheckout(path string) (root string, primary bool, found bool) {
	directory := path
	for range maxAncestorWalk {
		gitPath := filepath.Join(directory, ".git")
		// Stat, not Lstat: a canonical clone whose .git is a symlink to a
		// directory elsewhere is still a primary checkout, and reading it as
		// a "file" would silently downgrade it to a writable worktree.
		info, err := os.Stat(gitPath)
		if err == nil {
			return directory, info.IsDir(), true
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", false, false
		}
		directory = parent
	}
	return "", false, false
}

// canonicalCoordinates reports whether root is exactly
// <projects-root>/<owner>/<repository>.
//
// Requiring that exact shape is deliberate. A primary checkout anywhere else
// is not something WB manages, and refusing writes there would block work the
// guard has no standing to judge — a temporary fixture repository under
// /tmp, most of all, which every WB test suite creates.
func canonicalCoordinates(projectsRoot, root string) (owner, repository string, ok bool) {
	roots := pathVariants(root)
	for _, candidate := range pathVariants(projectsRoot) {
		for _, resolved := range roots {
			relative, err := filepath.Rel(candidate, resolved)
			if err != nil {
				continue
			}
			parts := strings.Split(filepath.ToSlash(relative), "/")
			if len(parts) != 2 || !validSegment(parts[0]) || !validSegment(parts[1]) {
				continue
			}
			return parts[0], parts[1], true
		}
	}
	return "", "", false
}

// pathVariants returns the forms of a directory that a comparison must accept.
//
// macOS resolves /tmp to /private/tmp and Git reports physical paths, so a
// projects root given one way and a tool-call path arriving the other way must
// still meet. Both forms are compared rather than only the resolved one,
// because EvalSymlinks fails outright on a path that does not exist.
func pathVariants(path string) []string {
	cleaned := filepath.Clean(path)
	variants := []string{cleaned}
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil && resolved != cleaned {
		variants = append(variants, resolved)
	}
	return variants
}

// validSegment rejects a path component that cannot be an owner or repository
// name, so `<projects-root>/.wb/worktrees` and similar internal directories
// never read as a repository coordinate.
func validSegment(segment string) bool {
	if segment == "" || segment == "." || segment == ".." {
		return false
	}
	if strings.HasPrefix(segment, ".") {
		return false
	}
	return true
}

// absolutePath makes a tool-supplied path absolute, expanding a leading ~.
// It reports false for anything it cannot turn into an absolute path, which
// the caller must treat as unknown and allow.
func absolutePath(path string) (string, bool) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", false
	}
	if trimmed == "~" || strings.HasPrefix(trimmed, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", false
		}
		trimmed = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(trimmed, "~"), "/"))
	}
	if !filepath.IsAbs(trimmed) {
		return "", false
	}
	return filepath.Clean(trimmed), true
}

// resolveAgainst makes a possibly-relative tool path absolute using the
// working directory the command would run in. An empty or relative base makes
// the answer unknown rather than guessing at the process's own cwd, which is
// never the agent's.
func resolveAgainst(base, path string) (string, bool) {
	if absolute, ok := absolutePath(path); ok {
		return absolute, true
	}
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || base == "" || !filepath.IsAbs(base) {
		return "", false
	}
	return filepath.Clean(filepath.Join(base, trimmed)), true
}
