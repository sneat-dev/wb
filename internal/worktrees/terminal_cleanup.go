package worktrees

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// TerminalCleanupProof is the exact successful cleanup result for one retired
// managed worktree. Callers must still revalidate its Git ancestry against the
// current remote target; this proves only the local identity and terminal WB
// lifecycle transition recorded before the checkout disappeared.
type TerminalCleanupProof struct {
	ReportPath  string
	GeneratedAt time.Time
	Result      CleanupResult
}

// FindTerminalCleanupProof searches WB's recognized private report layouts for
// a successful terminal cleanup of one exact managed-worktree identity.
// Absence is never evidence: only a structurally valid applied cleanup receipt
// with the expected repository, target, task, path, and branch is accepted.
func FindTerminalCleanupProof(projectsRoot, repository, target, task, worktree, branch string) (*TerminalCleanupProof, error) {
	if _, _, err := splitRepository(repository); err != nil {
		return nil, fmt.Errorf("invalid terminal cleanup repository: %w", err)
	}
	if !validSafeSegment(task) || !filepath.IsAbs(worktree) || branch == "" || target == "" {
		return nil, errors.New("terminal cleanup lookup identity is incomplete")
	}
	canonical, err := CanonicalRepositoryPath(projectsRoot, repository)
	if err != nil {
		return nil, err
	}
	resolution, err := wbhome.Resolve(projectsRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve WB home for terminal cleanup lookup: %w", err)
	}

	var matches []TerminalCleanupProof
	for _, layout := range resolution.Read {
		reportsRoot := filepath.Join(layout.Home, "reports", "worktree-cleanup")
		rootInfo, statErr := os.Lstat(reportsRoot)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return nil, fmt.Errorf("inspect terminal cleanup report root %s: %w", reportsRoot, statErr)
		}
		if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("terminal cleanup report root %s is not a real directory", reportsRoot)
		}
		entries, readErr := os.ReadDir(reportsRoot)
		if readErr != nil {
			return nil, fmt.Errorf("read terminal cleanup report root %s: %w", reportsRoot, readErr)
		}
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if _, parseErr := time.Parse("20060102T150405.000000000Z", entry.Name()); parseErr != nil {
				continue
			}
			path := filepath.Join(reportsRoot, entry.Name(), "cleanup.json")
			info, statErr := os.Lstat(path)
			if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				continue
			}
			decoder := json.NewDecoder(bytes.NewReader(contents))
			decoder.DisallowUnknownFields()
			var report cleanupReport
			if decoder.Decode(&report) != nil || requireJSONEOF(decoder) != nil || report.GeneratedAt.IsZero() || report.Phase != "applied" || !report.Apply || !cleanupReportSelectsTask(report, task) {
				continue
			}
			var matched []CleanupResult
			for _, result := range report.Results {
				if result.Repository == repository && result.Task == task && filepath.Clean(result.WorktreeDir) == filepath.Clean(worktree) && result.Branch == branch {
					matched = append(matched, result)
				}
			}
			if len(matched) != 1 {
				continue
			}
			result := matched[0]
			if result.Base != target || result.HeadSHA == "" || filepath.Clean(result.CanonicalDir) != filepath.Clean(canonical) {
				continue
			}
			matches = append(matches, TerminalCleanupProof{ReportPath: path, GeneratedAt: report.GeneratedAt, Result: result})
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no terminal cleanup receipt proves retired replacement %s", worktree)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].GeneratedAt.Before(matches[j].GeneratedAt) })
	proof := matches[len(matches)-1]
	if !proof.Result.Eligible || !proof.Result.Applied || !proof.Result.Clean || !proof.Result.IntegratedAtOrigin ||
		!proof.Result.WorktreeGone || !proof.Result.BranchDeleted || proof.Result.RemoteTargetSHA == "" {
		return nil, fmt.Errorf("latest cleanup receipt %s does not prove retired replacement %s completed", proof.ReportPath, worktree)
	}
	return &proof, nil
}

func cleanupReportSelectsTask(report cleanupReport, task string) bool {
	selected := 0
	if report.Task == task {
		selected++
	}
	for _, candidate := range report.Tasks {
		if candidate == task {
			selected++
		}
	}
	return selected == 1
}
