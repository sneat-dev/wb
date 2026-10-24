package agents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// diffFileName is the private patch artefact kept beside a run record.
const diffFileName = "diff.patch"

// maxDiffBytes bounds the patch WB keeps and the patch it prints. A worker can
// generate a diff larger than any caller wants in its context.
const maxDiffBytes = 2 << 20

// maxUntrackedList bounds the untracked list in a change summary.
const maxUntrackedList = 20

// WriteDiff stores the run's patch under its private directory and returns the
// path. A failure to write is not a failed run: the worktree is the artefact.
func (store Store) WriteDiff(ctx context.Context, agentID, worktreeDir, baseSHA string, deps TargetDeps) (string, error) {
	if deps.Git == nil {
		deps.Git = gitOutput
	}
	if strings.TrimSpace(worktreeDir) == "" || strings.TrimSpace(baseSHA) == "" {
		return "", nil
	}
	patch, err := deps.Git(ctx, worktreeDir, "diff", "--no-color", baseSHA, "--")
	if err != nil {
		return "", err
	}
	if len(patch) > maxDiffBytes {
		patch = patch[:maxDiffBytes] + "\n… (truncated; the worktree holds the full change)\n"
	}
	path := filepath.Join(store.Dir(agentID), diffFileName)
	if err := os.WriteFile(path, []byte(patch), 0o600); err != nil {
		return "", fmt.Errorf("write run diff %s: %w", path, err)
	}
	return path, nil
}

// RenderDiff builds the `wb agent diff` document for one finished run. The short
// form reports the stat and the untracked paths; the patch form prints the
// bounded patch itself.
func RenderDiff(ctx context.Context, record Record, patch bool, deps TargetDeps) (string, error) {
	if deps.Git == nil {
		deps.Git = gitOutput
	}
	dir := record.WorktreeDir
	if strings.TrimSpace(dir) == "" {
		return "", requestErrorf("run %s has no worktree to diff", record.AgentID)
	}
	base := strings.TrimSpace(record.BaseSHA)
	baseArgs := []string{}
	if base != "" {
		baseArgs = append(baseArgs, base)
	}
	var out strings.Builder
	if !patch {
		statArgs := append([]string{"diff", "--stat", "--no-color"}, baseArgs...)
		statArgs = append(statArgs, "--")
		stat, err := deps.Git(ctx, dir, statArgs...)
		if err != nil {
			return "", fmt.Errorf("read run diff stat: %w", err)
		}
		if strings.TrimSpace(stat) == "" {
			out.WriteString("no tracked change\n")
		} else {
			out.WriteString(strings.TrimRight(stat, "\n") + "\n")
		}
		if record.Changes != nil && len(record.Changes.Untracked) > 0 {
			out.WriteString("\nuntracked (not in the patch):\n")
			for _, path := range record.Changes.Untracked {
				out.WriteString("  " + path + "\n")
			}
		}
		if record.DiffPath != "" {
			out.WriteString("\npatch: " + record.DiffPath + "\n")
		}
		return out.String(), nil
	}
	patchArgs := append([]string{"diff", "--no-color"}, baseArgs...)
	patchArgs = append(patchArgs, "--")
	raw, err := deps.Git(ctx, dir, patchArgs...)
	if err != nil {
		return "", fmt.Errorf("read run diff: %w", err)
	}
	if len(raw) > maxDiffBytes {
		raw = raw[:maxDiffBytes] + "\n… (truncated; the worktree holds the full change)\n"
	}
	if strings.TrimSpace(raw) == "" {
		return "no tracked change\n", nil
	}
	return raw, nil
}

// untrackedFromStatus collects the "??" entries Git leaves out of a diff.
func untrackedFromStatus(status string) []string {
	var untracked []string
	for _, line := range strings.Split(status, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "?? ") {
			continue
		}
		untracked = append(untracked, strings.TrimSpace(line[3:]))
	}
	return untracked
}
