// Package diskreport answers two questions a fleet workstation asks under
// pressure: where did the space go, and what is safe to reclaim.
//
// Both were previously answered by hand, badly. This workstation reached 99%
// of its root filesystem while three Go lanes were running; the reclaim that
// followed was a `du` by eye, and the biggest single win — 14 GB of Go build
// cache nothing had touched for six hours — was found by accident rather than
// by measurement. Neither question is hard; both are just multi-call, which
// makes them exactly the shape a verb should own.
package diskreport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/diskusage"
	"github.com/sneat-dev/wb/internal/wbhome"
	"golang.org/x/sys/unix"
)

// Kind classifies a measured tree by what losing it would cost.
const (
	// KindRebuildable is a cache: deleting it costs time, never information.
	KindRebuildable = "rebuildable"
	// KindGoverned is WB-managed working state, retired by its own verb rather
	// than by a size threshold.
	KindGoverned = "governed"
	// KindEvidence is the analytics and audit base. It is never pruned: it is
	// small, and it is the only record of what actually happened.
	KindEvidence = "evidence"
)

// Entry is one measured tree.
type Entry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind"`
	// Prunable states whether `wb cache prune` may touch this tree at all.
	// An unprunable tree is named in code rather than merely omitted from a
	// default list, so that "it was not in the list" can never become "it was
	// deleted by a later list".
	Prunable bool            `json:"prunable"`
	Usage    diskusage.Usage `json:"usage"`
	// Budget is the size this tree is allowed to reach before a prune evicts
	// from it. Zero means no budget is declared.
	BudgetBytes int64 `json:"budget_bytes,omitempty"`
	// Note carries the one thing a reader needs that the numbers do not say.
	Note string `json:"note,omitempty"`
}

// Filesystem is the volume a path lives on.
type Filesystem struct {
	Path            string `json:"path"`
	TotalBytes      int64  `json:"total_bytes"`
	FreeBytes       int64  `json:"free_bytes"`
	AvailableBytes  int64  `json:"available_bytes"`
	UsedPercent     int    `json:"used_percent"`
	BelowFloorBytes bool   `json:"below_floor,omitempty"`
}

// Report is the whole occupancy picture.
type Report struct {
	SchemaVersion int        `json:"schema_version"`
	Filesystem    Filesystem `json:"filesystem"`
	// Worktrees, Repositories and Tasks are the three groupings of the same
	// checkouts. Each is measured as one accounting unit, so a store file two
	// worktrees both hard-link into is counted once in the group total.
	Worktrees    []Entry `json:"worktrees,omitempty"`
	Repositories []Entry `json:"repositories,omitempty"`
	Tasks        []Entry `json:"tasks,omitempty"`
	Caches       []Entry `json:"caches"`
	Logs         []Entry `json:"logs"`
	// Totals are the sums a reader acts on: what the caches hold, what the
	// worktrees hold, and what removing all of each would actually return.
	CacheTotal    diskusage.Usage `json:"cache_total"`
	WorktreeTotal diskusage.Usage `json:"worktree_total"`
	LogTotal      diskusage.Usage `json:"log_total"`
	Notes         []string        `json:"notes,omitempty"`
}

// Options selects what to measure.
type Options struct {
	ProjectsRoot string
	// SkipWorktrees omits the per-worktree walk, which is the expensive half on
	// a machine with a dozen node_modules trees.
	SkipWorktrees bool
	// SkipCaches omits the cache walk.
	SkipCaches bool
	// FloorBytes is the free-space floor a stream start would refuse below.
	FloorBytes int64
	// Home overrides the resolved WB home; tests set it.
	Home string
	// CacheRoots overrides the default cache locations; tests set it.
	CacheRoots []Entry
}

// GoBuildGrowthNote records the rate this fleet measured, because it is the
// argument for checking a floor before starting work rather than after failing.
const GoBuildGrowthNote = "go-build grew about 1 GB per hour under three concurrent Go lanes on this fleet; " +
	"check the free-space floor before starting a stream, not after a build fails at 99%"

// Measure builds the report.
func Measure(ctx context.Context, options Options) (Report, error) {
	home := strings.TrimSpace(options.Home)
	if home == "" {
		resolved, err := wbhome.Root(options.ProjectsRoot)
		if err != nil {
			return Report{}, err
		}
		home = resolved
	}
	report := Report{SchemaVersion: 1, Notes: []string{GoBuildGrowthNote}}
	filesystem, err := MeasureFilesystem(home, options.FloorBytes)
	if err != nil {
		return Report{}, err
	}
	report.Filesystem = filesystem

	if !options.SkipCaches {
		roots := options.CacheRoots
		if len(roots) == 0 {
			roots = DefaultCaches(home)
		}
		for _, entry := range roots {
			usage, measureErr := diskusage.Measure(ctx, entry.Path)
			if measureErr != nil {
				return Report{}, measureErr
			}
			entry.Usage = usage
			if entry.Kind == KindEvidence {
				report.Logs = append(report.Logs, entry)
				report.LogTotal = report.LogTotal.Add(usage)
				continue
			}
			report.Caches = append(report.Caches, entry)
			report.CacheTotal = report.CacheTotal.Add(usage)
		}
	}

	if !options.SkipWorktrees {
		if err := measureWorktrees(ctx, options.ProjectsRoot, home, &report); err != nil {
			return Report{}, err
		}
	}
	sortEntries(report.Caches)
	sortEntries(report.Logs)
	return report, nil
}

// measureWorktrees walks every WB-managed checkout once and reports it three
// ways. One walk, three groupings: measuring per worktree and again per task
// would be the same question asked twice.
func measureWorktrees(ctx context.Context, projectsRoot, home string, report *Report) error {
	resolution, err := wbhome.Resolve(projectsRoot)
	if err != nil {
		return err
	}
	walk := diskusage.NewWalk()
	byTask := map[string][]string{}
	byRepository := map[string][]string{}
	for _, layout := range resolution.Read {
		tasks, readErr := os.ReadDir(layout.WorktreesRoot)
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return fmt.Errorf("read worktrees root %s: %w", layout.WorktreesRoot, readErr)
		}
		for _, task := range tasks {
			if !task.IsDir() || strings.HasPrefix(task.Name(), ".") {
				continue
			}
			taskPath := filepath.Join(layout.WorktreesRoot, task.Name())
			owners, ownerErr := os.ReadDir(taskPath)
			if ownerErr != nil {
				continue
			}
			for _, owner := range owners {
				if !owner.IsDir() || strings.HasPrefix(owner.Name(), ".") {
					continue
				}
				repositories, repositoryErr := os.ReadDir(filepath.Join(taskPath, owner.Name()))
				if repositoryErr != nil {
					continue
				}
				for _, repository := range repositories {
					if !repository.IsDir() {
						continue
					}
					path := filepath.Join(taskPath, owner.Name(), repository.Name())
					usage, measureErr := walk.Measure(ctx, path)
					if measureErr != nil {
						return measureErr
					}
					slug := owner.Name() + "/" + repository.Name()
					report.Worktrees = append(report.Worktrees, Entry{
						Name: task.Name() + " " + slug, Path: path, Kind: KindGoverned,
						Usage: usage, Note: "retired by wb worktree gc, never by size",
					})
					byTask[task.Name()] = append(byTask[task.Name()], path)
					byRepository[slug] = append(byRepository[slug], path)
				}
			}
		}
	}
	// A canonical clone belongs to its repository's total: it is the tree every
	// worktree of that repository is cut from, and on a fleet it is often the
	// larger half.
	for slug := range byRepository {
		canonical := filepath.Join(projectsRoot, slug)
		if _, statErr := os.Stat(canonical); statErr == nil {
			if _, measureErr := walk.Measure(ctx, canonical); measureErr != nil {
				return measureErr
			}
			byRepository[slug] = append(byRepository[slug], canonical)
		}
	}
	for task, roots := range byTask {
		report.Tasks = append(report.Tasks, Entry{
			Name: task, Path: filepath.Dir(roots[0]), Kind: KindGoverned, Usage: walk.Total(roots...),
		})
	}
	for slug, roots := range byRepository {
		report.Repositories = append(report.Repositories, Entry{
			Name: slug, Path: filepath.Join(projectsRoot, slug), Kind: KindGoverned, Usage: walk.Total(roots...),
		})
	}
	report.WorktreeTotal = walk.Total()
	sortEntries(report.Worktrees)
	sortEntries(report.Tasks)
	sortEntries(report.Repositories)
	return nil
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Usage.UnsharedBytes != entries[j].Usage.UnsharedBytes {
			return entries[i].Usage.UnsharedBytes > entries[j].Usage.UnsharedBytes
		}
		return entries[i].Name < entries[j].Name
	})
}

// MeasureFilesystem reports the volume a path lives on.
func MeasureFilesystem(path string, floorBytes int64) (Filesystem, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return Filesystem{}, fmt.Errorf("read filesystem statistics for %s: %w", path, err)
	}
	blockSize := int64(stat.Bsize)
	total := int64(stat.Blocks) * blockSize
	free := int64(stat.Bfree) * blockSize
	available := int64(stat.Bavail) * blockSize
	filesystem := Filesystem{Path: path, TotalBytes: total, FreeBytes: free, AvailableBytes: available}
	if total > 0 {
		filesystem.UsedPercent = int(((total - available) * 100) / total)
	}
	filesystem.BelowFloorBytes = floorBytes > 0 && available < floorBytes
	return filesystem, nil
}

// DefaultCaches are the trees this fleet actually fills, with the budgets and
// the never-prune rules that belong to each.
//
// The evidence trees are listed here rather than omitted precisely so they are
// named: a tree that is merely absent from a default list gets added to a later
// list by someone who does not know why it was absent.
func DefaultCaches(home string) []Entry {
	userHome, err := os.UserHomeDir()
	if err != nil {
		userHome = home
	}
	const gigabyte = 1 << 30
	return []Entry{
		{
			Name: "go-build", Path: filepath.Join(userHome, ".cache", "go-build"),
			Kind: KindRebuildable, Prunable: true, BudgetBytes: 6 * gigabyte,
			Note: "Go touches an entry's modification time on every access, so age is a true last-used signal here",
		},
		{
			Name: "go-modules", Path: filepath.Join(userHome, "go", "pkg", "mod"),
			Kind: KindRebuildable, Prunable: true, BudgetBytes: 8 * gigabyte,
			Note: "re-downloadable; evicting it costs network, not information",
		},
		{
			Name: "pnpm-store", Path: filepath.Join(userHome, ".local", "share", "pnpm", "store"),
			Kind: KindRebuildable, Prunable: true, BudgetBytes: 8 * gigabyte,
			Note: "hard-linked into every frontend worktree: pruning it while one links in corrupts that worktree",
		},
		{
			Name: "npm", Path: filepath.Join(userHome, ".npm"),
			Kind: KindRebuildable, Prunable: true, BudgetBytes: 2 * gigabyte,
		},
		{
			Name: "playwright", Path: filepath.Join(userHome, ".cache", "ms-playwright"),
			Kind: KindRebuildable, Prunable: true, BudgetBytes: 2 * gigabyte,
			Note: "version-pinned browsers; only versions nothing pins are safe to drop",
		},
		{
			Name: "puppeteer", Path: filepath.Join(userHome, ".cache", "puppeteer"),
			Kind: KindRebuildable, Prunable: true, BudgetBytes: gigabyte,
		},
		{
			Name: "wb-reports", Path: filepath.Join(home, "reports"),
			Kind: KindRebuildable, Prunable: true, BudgetBytes: gigabyte,
			Note: "cleanup receipts; prunable by age only",
		},
		{
			Name: "wb-worklogs", Path: filepath.Join(home, "worklogs"),
			Kind: KindEvidence, Prunable: false,
			Note: "never pruned: the source for every WB report, and 17 MB across this whole fleet",
		},
		{
			Name: "wb-sessions", Path: filepath.Join(home, "sessions"),
			Kind: KindEvidence, Prunable: false, Note: "never pruned",
		},
		{
			Name: "wb-parked-sessions", Path: filepath.Join(home, "parked-sessions"),
			Kind: KindEvidence, Prunable: false, Note: "never pruned",
		},
		{
			Name: "wb-handoffs", Path: filepath.Join(home, "handoffs"),
			Kind: KindEvidence, Prunable: false, Note: "never pruned",
		},
	}
}

// NeverPrunable names the trees no flag may reach. It exists as a function
// rather than as an omission so a caller can assert against it.
func NeverPrunable(home string) []string {
	names := make([]string, 0, 4)
	for _, entry := range DefaultCaches(home) {
		if !entry.Prunable {
			names = append(names, entry.Path)
		}
	}
	sort.Strings(names)
	return names
}

// Age renders how long ago something was touched.
func Age(at time.Time, now time.Time) string {
	if at.IsZero() {
		return "-"
	}
	elapsed := now.Sub(at)
	switch {
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh", int(elapsed.Hours()))
	default:
		return fmt.Sprintf("%dd", int(elapsed.Hours()/24))
	}
}
