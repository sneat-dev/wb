// Package checkoutmarker writes one `.worktree.md` into every checkout,
// stating what that checkout is and whether it may be written to.
//
// # Why a marker in every checkout, not only in the ones that must stay clean
//
// The obvious design is a warning file dropped into canonical clones. It does
// not work: a negative signal present only where writing is wrong means a
// MISSING file reads as "nothing here objects, go ahead", which is exactly the
// wrong default for the checkout WB has not reached yet. A marker in every
// checkout inverts that. An agent reads one file and learns where it is, and
// absence means "unknown, verify" rather than "safe".
//
// It also degrades to readers the PreToolUse guard cannot reach: Codex, a
// human, and any tool that has not been written yet.
//
// # Why it stays untracked
//
// Committing `.worktree.md` and adding it to `.gitignore` does not work either:
// `.gitignore` has no effect on an already-tracked file, so a committed marker
// that WB rewrites shows up as ` M .worktree.md` — a dirty canonical clone,
// the very condition this exists to prevent — and conflicts on any pull that
// touches it. `git update-index --skip-worktree` is per-clone index state that
// silently reverts, which is not something to hang a safety guarantee on.
//
// So WB generates the file locally and never commits it, and pairs every write
// with an ignore rule so `git status` stays clean. Verified against real Git:
// one entry in the common directory's `info/exclude` covers the canonical
// clone and every linked worktree cut from it, because linked worktrees have
// no `info/exclude` of their own. WB's own hooks read `git status
// --porcelain`, which never lists an ignored path, so the marker cannot trip
// the policy it advertises.
package checkoutmarker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileName is the marker's name in every checkout.
const FileName = ".worktree.md"

// ExcludePattern is the ignore rule that keeps the marker out of git status.
// It is anchored so it can only ever match the checkout root's own marker.
const ExcludePattern = "/" + FileName

// WorktreesExcludePattern keeps WB's repository-local linked checkout root
// out of status, recursive searches, and build discovery in its canonical
// clone. It applies only at the canonical checkout root.
const WorktreesExcludePattern = "/.worktrees/"

// excludeHeader labels WB's block inside a file the user may also be editing.
const excludeHeader = "# WB: generated per-checkout marker, see AGENTS.md"

// Kind is what a checkout is.
type Kind string

const (
	// KindCanonical is the shared clone at <projects-root>/<owner>/<repository>
	// that every worktree in the fleet is cut from. It must stay clean.
	KindCanonical Kind = "canonical"
	// KindWorktree is a linked worktree: an isolated checkout that exists to be
	// written to.
	KindWorktree Kind = "worktree"
)

// Descriptor is everything the marker states. The field names are the schema
// agents key their decisions off, so they are part of the contract.
type Descriptor struct {
	Kind          Kind
	Writable      bool
	Repository    string
	CheckoutPath  string
	CanonicalPath string
	Branch        string
	BaseBranch    string
	Task          string
	WorktreesRoot string
	GeneratedAt   time.Time
	GeneratedBy   string
}

// Render produces the marker's exact contents: a machine-readable YAML
// document first, so a reader never has to parse prose, then the instructions
// a human or an agent acts on.
func Render(descriptor Descriptor) string {
	var builder strings.Builder
	builder.WriteString("---\n")
	builder.WriteString("wb_checkout: 1\n")
	fmt.Fprintf(&builder, "kind: %s\n", descriptor.Kind)
	fmt.Fprintf(&builder, "writable: %t\n", descriptor.Writable)
	writeYAMLString(&builder, "repository", descriptor.Repository)
	writeYAMLString(&builder, "checkout_path", descriptor.CheckoutPath)
	writeYAMLString(&builder, "canonical_path", descriptor.CanonicalPath)
	writeYAMLString(&builder, "branch", descriptor.Branch)
	writeYAMLString(&builder, "base_branch", descriptor.BaseBranch)
	writeYAMLString(&builder, "task", descriptor.Task)
	writeYAMLString(&builder, "worktrees_root", descriptor.WorktreesRoot)
	writeYAMLString(&builder, "generated_by", descriptor.GeneratedBy)
	writeYAMLString(&builder, "generated_at", descriptor.GeneratedAt.UTC().Format(time.RFC3339))
	builder.WriteString("---\n\n")
	if descriptor.Kind == KindCanonical {
		builder.WriteString(canonicalBody(descriptor))
	} else {
		builder.WriteString(worktreeBody(descriptor))
	}
	return builder.String()
}

func canonicalBody(descriptor Descriptor) string {
	repository := descriptor.Repository
	if repository == "" {
		repository = "<owner/repository>"
	}
	return fmt.Sprintf(`# This is a canonical clone — do not write here

`+"`%s`"+` is the shared canonical clone of **%s**. Every linked worktree in the
fleet is cut from it, so it must stay clean and stay on `+"`%s`"+`.

Uncommitted work left here is invisible to WB and one routine checkout away
from being destroyed. It has happened: a `+"`git checkout origin/main -- .`"+` run
to read a single file staged 186 files against a stale HEAD, and a generator
run in the wrong directory left a finished, unlanded document sitting untracked
where the next checkout would have taken it.

## Work here instead

    wb worktree create <task> %s

Then work in the printed worktree path. Its own `+"`.worktree.md`"+` will say
`+"`writable: true`"+`.

## What this clone is still for

Reading, and only the Git operations that keep it current:
`+"`git fetch`"+`, `+"`git merge --ff-only`"+`, `+"`git status`"+`, `+"`git log`"+`, `+"`git show`"+`.

## If it is already dirty

Do not reset, clean, or check out over it — that discards work nothing else
holds. Move the content onto a branch first:

    wb worktree rescue %s

## About this file

WB generates it. It is untracked and git-ignored on purpose, so it can say
this without making the clone dirty. Do not commit it.
`, descriptor.CheckoutPath, repository, descriptor.BaseBranch, repository, descriptor.CheckoutPath)
}

func worktreeBody(descriptor Descriptor) string {
	task := descriptor.Task
	if task == "" {
		task = "(not recorded)"
	}
	repository := descriptor.Repository
	if repository == "" {
		repository = "<owner/repository>"
	}
	return fmt.Sprintf(`# This is a worktree — write here

`+"`%s`"+` is an isolated linked worktree of **%s**.

| | |
| --- | --- |
| task | %s |
| branch | %s |
| base branch | %s |
| canonical clone | %s |

This is where work belongs. Edit, commit, and push from here.

Do not write into the canonical clone above; it must stay clean because every
other worktree in the fleet is cut from it.

## About this file

WB generates it. It is untracked and git-ignored on purpose. Do not commit it.
`, descriptor.CheckoutPath, repository, task, descriptor.Branch, descriptor.BaseBranch, descriptor.CanonicalPath)
}

// writeYAMLString emits a quoted scalar so a path holding a colon, a leading
// digit, or a word YAML would read as a boolean survives the round trip.
func writeYAMLString(builder *strings.Builder, key, value string) {
	fmt.Fprintf(builder, "%s: %q\n", key, value)
}

// Result reports what one Apply call did.
type Result struct {
	// MarkerPath is where the marker lives.
	MarkerPath string
	// MarkerWritten is false when the marker was already exactly right.
	MarkerWritten bool
	// ExcludePath is the ignore file WB kept the rule in.
	ExcludePath string
	// ExcludeWritten is false when the rule was already present.
	ExcludeWritten bool
}

// Changed reports whether anything on disk moved.
func (r Result) Changed() bool { return r.MarkerWritten || r.ExcludeWritten }

// Apply writes the marker and its ignore rule, and is safe to run repeatedly.
//
// The ignore rule goes first. A marker written before its rule exists is a
// dirty checkout for as long as the gap lasts, and WB's own pre-commit and
// pre-push hooks refuse a checkout with an untracked file — so the wrong order
// would, for that instant, break exactly the policy the marker describes.
func Apply(descriptor Descriptor, excludePath string) (Result, error) {
	result := Result{
		MarkerPath:  filepath.Join(descriptor.CheckoutPath, FileName),
		ExcludePath: excludePath,
	}
	written, err := EnsureExclude(excludePath)
	if err != nil {
		return result, err
	}
	result.ExcludeWritten = written

	contents := Render(descriptor)
	existing, readErr := os.ReadFile(result.MarkerPath)
	if readErr == nil && Equivalent(string(existing), contents) {
		return result, nil
	}
	if err := writeFileAtomically(result.MarkerPath, contents); err != nil {
		return result, err
	}
	result.MarkerWritten = true
	return result, nil
}

// Equivalent compares two markers ignoring only their timestamps, so a refresh
// that would change nothing but the clock leaves the file alone. That is what
// makes repeated runs — on every sync, every create — free.
func Equivalent(left, right string) bool {
	return stripGeneratedAt(left) == stripGeneratedAt(right)
}

func stripGeneratedAt(contents string) string {
	lines := strings.Split(contents, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "generated_at:") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// EnsureExclude appends the marker's ignore rule to a Git exclude file, and
// reports whether it had to. Every other line in that file is preserved: it is
// the user's, and WB only ever adds to it.
func EnsureExclude(excludePath string) (bool, error) {
	if excludePath == "" {
		return false, fmt.Errorf("no exclude file was resolved for this checkout")
	}
	existing, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read %s: %w", excludePath, err)
	}
	patterns := []string{ExcludePattern, WorktreesExcludePattern}
	present := make(map[string]bool, len(patterns))
	for _, line := range strings.Split(string(existing), "\n") {
		for _, pattern := range patterns {
			if strings.TrimSpace(line) == pattern {
				present[pattern] = true
			}
		}
	}
	missing := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		if !present[pattern] {
			missing = append(missing, pattern)
		}
	}
	if len(missing) == 0 {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return false, fmt.Errorf("create %s: %w", filepath.Dir(excludePath), err)
	}
	var builder strings.Builder
	builder.Write(existing)
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		builder.WriteString("\n")
	}
	builder.WriteString(excludeHeader + "\n")
	for _, pattern := range missing {
		builder.WriteString(pattern + "\n")
	}
	if err := writeFileAtomically(excludePath, builder.String()); err != nil {
		return false, err
	}
	return true, nil
}

// writeFileAtomically replaces a file through a temporary file in the same
// directory, so an interrupted write can never leave a half-written marker or,
// far worse, a truncated exclude file that stops ignoring the marker.
func writeFileAtomically(path, contents string) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".wb-marker-*")
	if err != nil {
		return fmt.Errorf("stage a replacement for %s: %w", path, err)
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := temporary.WriteString(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return fmt.Errorf("set permissions on %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
