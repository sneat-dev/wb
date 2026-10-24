package agents

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// SummarizeChanges derives a cheap, deterministic description of what a worker
// left behind in its worktree. It reads Git state only; it never reads file
// contents, so it cannot grow into a transcript and cannot be mistaken for a
// review of the change.
//
// A failure to read Git state yields no summary rather than a failed run: the
// worktree is the artefact and a missing diff stat must not hide it.
func SummarizeChanges(ctx context.Context, worktreeDir, baseSHA string) *ChangeSummary {
	return SummarizeChangesWith(ctx, worktreeDir, baseSHA, nil)
}

// SummarizeChangesWith is SummarizeChanges with the paths that were already
// dirty before the worker started. When a base commit is known the changed files
// come from `git diff --name-only`, which includes the worker's commits and
// excludes pre-existing uncommitted work; otherwise Git status is the only
// evidence available.
func SummarizeChangesWith(ctx context.Context, worktreeDir, baseSHA string, preexisting []string) *ChangeSummary {
	if strings.TrimSpace(worktreeDir) == "" {
		return nil
	}
	summary := &ChangeSummary{}
	status, statusErr := gitOutput(ctx, worktreeDir, "status", "--porcelain")
	untracked := []string{}
	if statusErr == nil {
		untracked = subtractPaths(untrackedFromStatus(status), preexisting)
		summary.Untracked = untracked
		if len(summary.Untracked) > maxUntrackedList {
			summary.Untracked = summary.Untracked[:maxUntrackedList]
		}
	}
	if strings.TrimSpace(baseSHA) != "" {
		// One numstat call carries the paths and the line counts together, so a
		// summary costs the same number of Git invocations it always did.
		if numstat, err := gitOutput(ctx, worktreeDir, "diff", "--numstat", baseSHA, "--"); err == nil {
			files, insertions, deletions := parseNumstat(numstat)
			summary.Insertions, summary.Deletions = insertions, deletions
			for _, name := range files {
				summary.FilesChanged++
				if len(summary.Files) < maxChangedFiles {
					summary.Files = append(summary.Files, name)
				} else {
					summary.Truncated = true
				}
			}
		}
		if count, err := gitOutput(ctx, worktreeDir, "rev-list", "--count", baseSHA+"..HEAD"); err == nil {
			if commits, convErr := strconv.Atoi(strings.TrimSpace(count)); convErr == nil {
				summary.Commits = commits
			}
		}
	} else if statusErr == nil {
		for _, line := range strings.Split(status, "\n") {
			line = strings.TrimRight(line, "\r")
			if len(line) < 4 {
				continue
			}
			summary.FilesChanged++
			if len(summary.Files) < maxChangedFiles {
				summary.Files = append(summary.Files, strings.TrimSpace(line[3:]))
			} else {
				summary.Truncated = true
			}
		}
	}
	// Untracked files are not in a diff, so they are appended explicitly: a
	// change summary that silently omitted new files would understate the work.
	// A file that is both listed by the diff and still untracked (an intent-to-add
	// entry) is counted once.
	for _, path := range untracked {
		if containsString(summary.Files, path) {
			continue
		}
		summary.FilesChanged++
		if len(summary.Files) < maxChangedFiles {
			summary.Files = append(summary.Files, path)
		} else {
			summary.Truncated = true
		}
	}
	if summary.FilesChanged == 0 && summary.Insertions == 0 && summary.Deletions == 0 && summary.Commits == 0 && len(summary.Untracked) == 0 {
		return summary
	}
	return summary
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// subtractPaths removes paths that were already dirty when the run started.
func subtractPaths(paths, preexisting []string) []string {
	if len(preexisting) == 0 {
		return paths
	}
	known := make(map[string]bool, len(preexisting))
	for _, path := range preexisting {
		known[path] = true
	}
	remaining := make([]string, 0, len(paths))
	for _, path := range paths {
		if !known[path] {
			remaining = append(remaining, path)
		}
	}
	return remaining
}

// parseNumstat reads Git's "added removed path" lines. A binary change reports
// "-" for both counts: the path still belongs in the list, the counts are zero.
func parseNumstat(output string) (files []string, insertions, deletions int) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(fields) != 3 {
			continue
		}
		files = append(files, fields[2])
		if added, err := strconv.Atoi(fields[0]); err == nil {
			insertions += added
		}
		if removed, err := strconv.Atoi(fields[1]); err == nil {
			deletions += removed
		}
	}
	return files, insertions, deletions
}

func gitOutput(ctx context.Context, dir string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, arguments...)...)
	command.Env = append(gitEnvironment(), "GIT_OPTIONAL_LOCKS=0")
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("git %s in %s: %w", strings.Join(arguments, " "), dir, err)
	}
	return string(output), nil
}

// gitEnvironment keeps Git from reading a user's global or system
// configuration, so a worker's worktree state cannot depend on how the machine
// happens to be configured.
func gitEnvironment() []string {
	environment := []string{
		"PATH=" + pathValue(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	}
	// HOME is passed only when it actually resolves: an empty HOME= is a
	// different, and worse, statement to Git than no HOME at all.
	if home := homeDir(); home != "" {
		environment = append(environment, "HOME="+home)
	}
	return environment
}
