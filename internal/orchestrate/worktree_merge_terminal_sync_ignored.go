package orchestrate

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/runner"
)

// statusIsUntrackedOnly reports whether every entry of a non-empty
// `git status --porcelain=v1` output is an untracked path. A tracked
// modification, a staged change, or a conflict makes it false.
func statusIsUntrackedOnly(status string) bool {
	for _, line := range strings.Split(strings.TrimRight(status, "\n"), "\n") {
		if !strings.HasPrefix(line, "?? ") {
			return false
		}
	}
	return true
}

// requireUntrackedDirtIgnoredAtTarget proves that the canonical clone's
// untracked paths cannot block or be hidden by a fast-forward to ref: no
// path is added, modified or removed by HEAD..ref (nor shares a file/
// directory prefix with such a path), and every path is ignored by the
// ignore rules ref itself carries — its committed .gitignore files on each
// path's ancestor directories, plus the repository's info/exclude and the
// configured global excludes. The .gitignore files are materialised from
// ref into a private scratch directory that git evaluates as a work tree
// with --no-index, so the canonical clone's index and working tree are never
// touched. It returns nil when every path qualifies.
//
// scratchRoot is the parent of the scratch directory; empty means the
// operating system's temporary directory.
func requireUntrackedDirtIgnoredAtTarget(ctx context.Context, run runner.Runner, canonical, ref, scratchRoot string) error {
	listed, err := syncIgnoreGit(ctx, run, canonical, nil, false, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	untracked := splitNULList(listed)
	if len(untracked) == 0 {
		return nil
	}
	diff, err := syncIgnoreGit(ctx, run, canonical, nil, false, "diff", "--name-only", "--no-renames", "-z", "HEAD", ref)
	if err != nil {
		return err
	}
	changed := splitNULList(diff)
	var touched []string
	for _, candidate := range untracked {
		if untrackedPathTouched(candidate, changed) {
			touched = append(touched, candidate)
		}
	}
	if len(touched) > 0 {
		return fmt.Errorf("untracked paths are changed by the fast-forward to %s: %s", ref, strings.Join(touched, ", "))
	}
	gitDir, err := syncIgnoreGit(ctx, run, canonical, nil, false, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	scratch, err := os.MkdirTemp(scratchRoot, "wb-canonical-sync-ignore-")
	if err != nil {
		return fmt.Errorf("create ignore scratch directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	if err := materializeTargetGitignores(ctx, run, canonical, ref, scratch, untracked); err != nil {
		return err
	}
	stdin := []byte(strings.Join(untracked, "\x00") + "\x00")
	report, err := syncIgnoreGit(ctx, run, scratch, stdin, true,
		"--git-dir="+strings.TrimSpace(gitDir), "--work-tree="+scratch,
		"check-ignore", "--no-index", "-z", "--stdin", "--verbose", "--non-matching")
	if err != nil {
		return err
	}
	ignored, err := parseCheckIgnoreVerbose(report)
	if err != nil {
		return err
	}
	var kept []string
	for _, candidate := range untracked {
		if !ignored[candidate] {
			kept = append(kept, candidate)
		}
	}
	if len(kept) > 0 {
		return fmt.Errorf("untracked paths are not ignored by %s: %s", ref, strings.Join(kept, ", "))
	}
	return nil
}

// untrackedPathTouched reports whether an untracked path equals a path the
// fast-forward changes, or one is a directory prefix of the other.
func untrackedPathTouched(candidate string, changed []string) bool {
	candidate = strings.TrimSuffix(candidate, "/")
	for _, other := range changed {
		if candidate == other || strings.HasPrefix(candidate, other+"/") || strings.HasPrefix(other, candidate+"/") {
			return true
		}
	}
	return false
}

// materializeTargetGitignores writes ref's committed .gitignore file for each
// ancestor directory of the given paths into scratch at the same relative
// path. Symlinked .gitignore entries are skipped because git never follows
// them.
func materializeTargetGitignores(ctx context.Context, run runner.Runner, canonical, ref, scratch string, paths []string) error {
	seen := map[string]bool{}
	var candidates []string
	for _, item := range paths {
		dir := strings.TrimSuffix(item, "/")
		for {
			dir = path.Dir(dir)
			name := path.Join(dir, ".gitignore")
			if !seen[name] {
				seen[name] = true
				candidates = append(candidates, name)
			}
			if dir == "." {
				break
			}
		}
	}
	sort.Strings(candidates)
	args := append([]string{"--literal-pathspecs", "ls-tree", "-z", ref, "--"}, candidates...)
	listing, err := syncIgnoreGit(ctx, run, canonical, nil, false, args...)
	if err != nil {
		return err
	}
	for _, entry := range splitNULList(listing) {
		meta, name, found := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !found || len(fields) != 3 {
			return fmt.Errorf("unexpected ls-tree entry %q", entry)
		}
		if fields[1] != "blob" || fields[0] == "120000" {
			continue
		}
		content, err := syncIgnoreGit(ctx, run, canonical, nil, false, "cat-file", "blob", fields[2])
		if err != nil {
			return err
		}
		destination := filepath.Join(scratch, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return fmt.Errorf("materialize %s: %w", name, err)
		}
		if err := os.WriteFile(destination, []byte(content), 0o600); err != nil {
			return fmt.Errorf("materialize %s: %w", name, err)
		}
	}
	return nil
}

// parseCheckIgnoreVerbose reads `git check-ignore -z --verbose
// --non-matching` output (source, line, pattern, path per record) and returns
// the paths whose last matching pattern ignores them; a negated pattern
// re-includes its path.
func parseCheckIgnoreVerbose(report string) (map[string]bool, error) {
	fields := strings.Split(report, "\x00")
	if fields[len(fields)-1] == "" {
		fields = fields[:len(fields)-1]
	}
	if len(fields)%4 != 0 {
		return nil, fmt.Errorf("unexpected check-ignore output with %d fields", len(fields))
	}
	ignored := map[string]bool{}
	for i := 0; i < len(fields); i += 4 {
		pattern := fields[i+2]
		if pattern != "" && !strings.HasPrefix(pattern, "!") {
			ignored[fields[i+3]] = true
		}
	}
	return ignored, nil
}

// syncIgnoreGit runs one read-only git observation and returns its standard
// output alone, so NUL-separated path lists are never interleaved with
// diagnostics. allowNoMatch accepts exit status 1, git check-ignore's
// documented "no path is ignored" result.
func syncIgnoreGit(ctx context.Context, run runner.Runner, dir string, stdin []byte, allowNoMatch bool, args ...string) (string, error) {
	result, err := run.RunOpts(ctx, dir, runner.RunOptions{Env: console.Env(), Stdin: stdin}, "git", args...)
	if err == nil && result.ExitCode != 0 {
		err = fmt.Errorf("exit status %d", result.ExitCode)
	}
	if err != nil && (!allowNoMatch || result.ExitCode != 1) {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(result.Stderr))
	}
	return result.Stdout, nil
}

func splitNULList(output string) []string {
	var items []string
	for _, item := range strings.Split(output, "\x00") {
		if item != "" {
			items = append(items, item)
		}
	}
	return items
}
