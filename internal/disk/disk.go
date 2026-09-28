// Package disk reports where the bytes WB causes to exist actually are, and
// how much room is left for the next one.
//
// On 2026-09-17 this fleet's workstation reached 101 MB free of 150 GB. The
// first symptom was not a warning: it was a Go build failing in the linker with
// "no space left on device". Nothing had reported the growth, because nothing
// measured it. `wb fleet stats` counts repositories, worktrees and attention —
// all of them counts, none of them bytes.
//
// The measurement that mattered was not the total. It was the split. Worktrees
// held 340 MB across the whole fleet; a single shared Go build cache held 22 GB;
// per-task scratch under /tmp held 46 GB in roughly 2,500 directories whose
// owning tasks had long finished. Anyone reasoning from "worktrees are the big
// thing WB creates" would have cleaned the wrong 0.2%.
//
// So this reports per category, and separates two numbers that a single "size"
// would conflate:
//
//   - apparent bytes, which is what each tree looks like on its own
//   - unshared bytes, which is what removing it would actually give back
//
// Those differ sharply here. Git worktrees share objects with their canonical
// clone and pnpm hard-links every store entry into every consumer, so a report
// that only shows apparent size promises a reclaim that deleting cannot deliver.
// internal/diskusage already draws that distinction correctly across trees, and
// this package accounts every category through one shared walk so content
// linked into two categories is not counted twice.
package disk

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/diskusage"
)

// Filesystem is the capacity of the volume a path lives on.
type Filesystem struct {
	Path string `yaml:"path" json:"path"`
	// TotalBytes is the volume's size.
	TotalBytes int64 `yaml:"total_bytes" json:"total_bytes"`
	// UsedBytes is what is consumed by everything, not only by WB.
	UsedBytes int64 `yaml:"used_bytes" json:"used_bytes"`
	// AvailableBytes is what this user can still write, which on most
	// filesystems is less than free: a reserve is held back for root.
	AvailableBytes int64 `yaml:"available_bytes" json:"available_bytes"`
}

// AvailableRatio is the share of the volume still writable, 0 when unknown.
func (f Filesystem) AvailableRatio() float64 {
	if f.TotalBytes <= 0 {
		return 0
	}
	return float64(f.AvailableBytes) / float64(f.TotalBytes)
}

// Category is one accounted group of WB-attributable bytes.
type Category struct {
	Name string `yaml:"name" json:"name"`
	// Kind separates what may be deleted freely from what may not:
	// "cache" is regenerable, "scratch" belongs to a task, "worktree" may
	// hold unlanded work, "state" is WB's own durable records.
	Kind string `yaml:"kind" json:"kind"`
	// Roots are the measured paths, absent ones omitted.
	Roots []string `yaml:"roots" json:"roots"`
	// ApparentBytes is what the trees look like in isolation.
	ApparentBytes int64 `yaml:"apparent_bytes" json:"apparent_bytes"`
	// UnsharedBytes is what removing them would actually reclaim.
	UnsharedBytes int64 `yaml:"unshared_bytes" json:"unshared_bytes"`
	// Note explains anything a byte count alone would misrepresent.
	Note string `yaml:"note,omitempty" json:"note,omitempty"`
}

// Report is one machine's accounting.
type Report struct {
	Filesystem Filesystem `yaml:"filesystem" json:"filesystem"`
	Categories []Category `yaml:"categories" json:"categories"`
	// AttributedBytes is the unshared total across categories, measured in one
	// walk so content shared between categories is counted once.
	AttributedBytes int64 `yaml:"attributed_bytes" json:"attributed_bytes"`
	// Findings are conditions worth acting on, most urgent first.
	Findings []string `yaml:"findings,omitempty" json:"findings,omitempty"`
	// Skipped records roots that could not be measured, so a small total is
	// never silently mistaken for a tidy machine.
	Skipped []string `yaml:"skipped,omitempty" json:"skipped,omitempty"`
}

// Options configures a report.
type Options struct {
	// ProjectsRoot is the {org}/{repo} checkout root.
	ProjectsRoot string
	// WBHome is WB's own state directory; empty resolves to ~/.wb.
	WBHome string
	// SkipSizes reports the roots and the filesystem without walking trees.
	// Measuring a 22 GB build cache costs minutes of IO, and the free-space
	// figure alone is often the answer.
	SkipSizes bool
	// MinimumAvailableRatio raises a finding when headroom falls below it.
	// Zero applies the default.
	MinimumAvailableRatio float64
	// FilesystemProbe overrides how the volume's total/available bytes are
	// read. Nil uses the real platform statfs (filesystemFor). Tests that
	// must not depend on the host's own free space at test time inject a
	// fake here instead of asserting on whatever headroom this machine
	// happens to have.
	FilesystemProbe func(path string) (Filesystem, error)
}

// DefaultMinimumAvailableRatio is the headroom below which a report complains.
// A tenth of the volume is enough for a large link step and a test run; the
// incident this package exists for had 0.0007 left.
const DefaultMinimumAvailableRatio = 0.10

// Collect measures the machine.
func Collect(ctx context.Context, options Options) (Report, error) {
	projectsRoot := options.ProjectsRoot
	if projectsRoot == "" {
		return Report{}, errors.New("projects root is required")
	}
	home := options.WBHome
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return Report{}, fmt.Errorf("resolve home directory: %w", err)
		}
		home = filepath.Join(userHome, ".wb")
	}
	minimum := options.MinimumAvailableRatio
	if minimum <= 0 {
		minimum = DefaultMinimumAvailableRatio
	}

	probe := options.FilesystemProbe
	if probe == nil {
		probe = filesystemFor
	}
	report := Report{}
	filesystem, err := probe(projectsRoot)
	if err != nil {
		return Report{}, err
	}
	report.Filesystem = filesystem

	groups := candidateGroups(projectsRoot, home)

	// One walk across every category: a pnpm store hard-linked into a worktree
	// belongs to whichever category is measured first and must not be added
	// again under the second.
	walk := diskusage.NewWalk()
	measured := map[string][]string{}
	for _, group := range groups {
		category := Category{Name: group.name, Kind: group.kind, Note: group.note}
		for _, root := range group.roots {
			if root == "" {
				continue
			}
			if _, err := os.Stat(root); err != nil {
				continue
			}
			category.Roots = append(category.Roots, root)
			if options.SkipSizes {
				continue
			}
			usage, err := walk.Measure(ctx, root)
			if err != nil {
				report.Skipped = append(report.Skipped, fmt.Sprintf("%s: %v", root, err))
				continue
			}
			category.ApparentBytes += usage.ApparentBytes
			measured[group.name] = append(measured[group.name], root)
		}
		if len(category.Roots) == 0 {
			continue
		}
		report.Categories = append(report.Categories, category)
	}

	// Reclaim figures come from the walk, not from summing per-tree results.
	// A file hard-linked into two measured trees is unshared with respect to
	// the pair and shared with respect to either one alone, so per-tree sums
	// are wrong in both directions — which is the whole reason Walk exists.
	if !options.SkipSizes {
		for i := range report.Categories {
			roots := measured[report.Categories[i].Name]
			if len(roots) == 0 {
				continue
			}
			report.Categories[i].UnsharedBytes = walk.Total(roots...).UnsharedBytes
		}
		report.AttributedBytes = walk.Total().UnsharedBytes
	}

	sort.SliceStable(report.Categories, func(i, j int) bool {
		return report.Categories[i].UnsharedBytes > report.Categories[j].UnsharedBytes
	})
	report.Findings = findings(report, minimum, options.SkipSizes)
	return report, nil
}

type group struct {
	name  string
	kind  string
	roots []string
	note  string
}

// candidateGroups names every place WB is known to put bytes. A path that does
// not exist is dropped later rather than here, so the set stays declarative.
func candidateGroups(projectsRoot, home string) []group {
	userHome, _ := os.UserHomeDir()
	groups := []group{
		{
			name: "worktrees", kind: "worktree",
			roots: append(worktreeRoots(projectsRoot), filepath.Join(home, "worktrees")),
			note:  "may hold unlanded work; retire with wb worktree gc, never rm",
		},
		{
			name: "go-build-cache", kind: "cache",
			roots: []string{goEnv("GOCACHE")},
			note:  "regenerable; shared across every worktree, so per-task copies multiply it",
		},
		{
			name: "go-module-cache", kind: "cache",
			roots: []string{goEnv("GOMODCACHE")},
			note:  "regenerable; re-downloading costs network, not correctness",
		},
		{
			name: "wb-state", kind: "state",
			roots: []string{home},
			note:  "work logs, receipts, hub store; not regenerable",
		},
	}
	if userHome != "" {
		groups = append(groups, group{
			name: "node-package-store", kind: "cache",
			roots: []string{
				filepath.Join(userHome, ".local", "share", "pnpm", "store"),
				filepath.Join(userHome, "Library", "pnpm", "store"),
				filepath.Join(userHome, ".npm"),
			},
			note: "hard-linked into consumers, so apparent size far exceeds what removal reclaims",
		})
	}
	groups = append(groups, group{
		name: "scratch", kind: "scratch",
		roots: scratchRoots(),
		note:  "per-task temporary space with no owner; 46 GB of it filled this machine on 2026-09-17",
	})
	return groups
}

// worktreeRoots finds every .worktrees directory two levels under the projects
// root, which is the {org}/{repo} layout WB maintains.
func worktreeRoots(projectsRoot string) []string {
	var roots []string
	orgs, err := os.ReadDir(projectsRoot)
	if err != nil {
		return nil
	}
	for _, org := range orgs {
		if !org.IsDir() || strings.HasPrefix(org.Name(), ".") {
			continue
		}
		repos, err := os.ReadDir(filepath.Join(projectsRoot, org.Name()))
		if err != nil {
			continue
		}
		for _, repo := range repos {
			if !repo.IsDir() {
				continue
			}
			candidate := filepath.Join(projectsRoot, org.Name(), repo.Name(), ".worktrees")
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				roots = append(roots, candidate)
			}
		}
	}
	return roots
}

// scratchRoots covers the location WB will own and the ad-hoc ones that exist
// because it does not own one yet.
func scratchRoots() []string {
	temp := os.TempDir()
	roots := []string{filepath.Join(temp, "wb")}
	entries, err := os.ReadDir(temp)
	if err != nil {
		return roots
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// wb-pr308-lint-go-cache, wb-sync-report-focused-cache and friends:
		// per-task caches nothing retires, which is the shape that filled /tmp.
		if strings.HasPrefix(entry.Name(), "wb-") {
			roots = append(roots, filepath.Join(temp, entry.Name()))
		}
	}
	return roots
}

// goEnv asks the toolchain rather than guessing a path that varies by platform
// and by GOPATH. A missing toolchain yields no root rather than a wrong one.
func goEnv(name string) string {
	command := exec.Command("go", "env", name)
	output, err := command.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// findings converts the measurement into the few statements worth acting on.
func findings(report Report, minimumRatio float64, skippedSizes bool) []string {
	var found []string
	ratio := report.Filesystem.AvailableRatio()
	if report.Filesystem.TotalBytes > 0 && ratio < minimumRatio {
		found = append(found, fmt.Sprintf(
			"only %s of %s available (%.1f%%, floor %.0f%%) on the filesystem holding %s",
			diskusage.Human(report.Filesystem.AvailableBytes),
			diskusage.Human(report.Filesystem.TotalBytes),
			ratio*100, minimumRatio*100, report.Filesystem.Path))
	}
	if skippedSizes {
		return found
	}
	for _, category := range report.Categories {
		if category.Kind == "scratch" && category.UnsharedBytes > 0 {
			found = append(found, fmt.Sprintf(
				"%s of scratch has no owning task; nothing retires it today",
				diskusage.Human(category.UnsharedBytes)))
		}
	}
	if len(report.Skipped) > 0 {
		found = append(found, fmt.Sprintf(
			"%d root(s) could not be measured, so the total is a floor, not a total",
			len(report.Skipped)))
	}
	return found
}

// Render writes the human form: the filesystem first, because that is the
// number that decides whether anything else matters.
func Render(report Report) string {
	var out strings.Builder
	fs := report.Filesystem
	fmt.Fprintf(&out, "Filesystem %s: %s available of %s (%.1f%% free)\n",
		fs.Path, diskusage.Human(fs.AvailableBytes), diskusage.Human(fs.TotalBytes),
		fs.AvailableRatio()*100)
	fmt.Fprintf(&out, "WB-attributed: %s reclaimable\n\n", diskusage.Human(report.AttributedBytes))

	fmt.Fprintf(&out, "%-20s %-9s %12s %12s\n", "CATEGORY", "KIND", "RECLAIM", "APPARENT")
	for _, category := range report.Categories {
		fmt.Fprintf(&out, "%-20s %-9s %12s %12s\n",
			category.Name, category.Kind,
			diskusage.Human(category.UnsharedBytes), diskusage.Human(category.ApparentBytes))
	}

	if len(report.Findings) > 0 {
		out.WriteString("\nFindings:\n")
		for _, finding := range report.Findings {
			fmt.Fprintf(&out, "  - %s\n", finding)
		}
	}
	for _, skipped := range report.Skipped {
		fmt.Fprintf(&out, "  ! unmeasured %s\n", skipped)
	}
	return out.String()
}
