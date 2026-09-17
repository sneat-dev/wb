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
	if strings.TrimSpace(worktreeDir) == "" {
		return nil
	}
	summary := &ChangeSummary{}
	if status, err := gitOutput(ctx, worktreeDir, "status", "--porcelain"); err == nil {
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
	if strings.TrimSpace(baseSHA) != "" {
		if shortstat, err := gitOutput(ctx, worktreeDir, "diff", "--shortstat", baseSHA); err == nil {
			insertions, deletions := parseShortstat(shortstat)
			summary.Insertions, summary.Deletions = insertions, deletions
		}
		if count, err := gitOutput(ctx, worktreeDir, "rev-list", "--count", baseSHA+"..HEAD"); err == nil {
			if commits, convErr := strconv.Atoi(strings.TrimSpace(count)); convErr == nil {
				summary.Commits = commits
			}
		}
	}
	if summary.FilesChanged == 0 && summary.Insertions == 0 && summary.Deletions == 0 && summary.Commits == 0 {
		return summary
	}
	return summary
}

// parseShortstat reads Git's "N files changed, X insertions(+), Y deletions(-)"
// line. Absent clauses mean zero rather than an error.
func parseShortstat(output string) (insertions, deletions int) {
	for _, clause := range strings.Split(output, ",") {
		fields := strings.Fields(clause)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		switch {
		case strings.HasPrefix(fields[1], "insertion"):
			insertions = value
		case strings.HasPrefix(fields[1], "deletion"):
			deletions = value
		}
	}
	return insertions, deletions
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
