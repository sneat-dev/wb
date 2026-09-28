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

// readCleanupReportFile is the shared trust boundary for cleanup receipts.
// Both chronological validation and retired-worktree lookup must interpret
// the same complete, regular JSON file before judging its identity or outcome.
func readCleanupReportFile(path string) (cleanupReport, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return cleanupReport{}, fmt.Errorf("stat terminal cleanup report %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return cleanupReport{}, fmt.Errorf("terminal cleanup report %s is not a regular file", path)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return cleanupReport{}, fmt.Errorf("read terminal cleanup report %s: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var report cleanupReport
	if err := decoder.Decode(&report); err != nil {
		return cleanupReport{}, fmt.Errorf("decode terminal cleanup report %s: %w", path, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return cleanupReport{}, fmt.Errorf("decode terminal cleanup report %s: %w", path, err)
	}
	return report, nil
}

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
	var latestUnverifiable time.Time
	var latestUnverifiablePath string
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
			recordedAt, parseErr := time.Parse("20060102T150405.000000000Z", entry.Name())
			if parseErr != nil {
				continue
			}
			path := filepath.Join(reportsRoot, entry.Name(), "cleanup.json")
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				if recordedAt.After(latestUnverifiable) {
					latestUnverifiable, latestUnverifiablePath = recordedAt, path
				}
				continue
			}
			report, readErr := readCleanupReportFile(path)
			if readErr != nil || !report.GeneratedAt.Equal(recordedAt) {
				// A later unreadable or malformed report could be a failed
				// retry for this task. Its identity cannot be trusted, even if
				// it belongs to another task, so an older success cannot prove
				// the latest cleanup completed.
				if recordedAt.After(latestUnverifiable) {
					latestUnverifiable, latestUnverifiablePath = recordedAt, path
				}
				continue
			}
			if report.Phase != "applied" || !report.Apply || !cleanupReportSelectsTask(report, task) {
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
	if !latestUnverifiable.Before(proof.GeneratedAt) {
		return nil, fmt.Errorf("later cleanup receipt %s is unverifiable; cannot establish latest terminal cleanup for %s", latestUnverifiablePath, worktree)
	}
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
