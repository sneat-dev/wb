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
	"path/filepath"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/diskusage"
	"github.com/sneat-dev/wb/internal/goenv"
	"github.com/sneat-dev/wb/internal/wbhome"
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
	// Partial is true when part of a root could not be read: the bytes are a
	// lower bound.
	Partial bool `yaml:"partial,omitempty" json:"partial,omitempty"`
	// SameAs names an earlier category that resolves to the same directory,
	// which owns its bytes; this one is not measured again.
	SameAs []string `yaml:"same_as,omitempty" json:"same_as,omitempty"`
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
	// WBHome is WB's own state directory; empty resolves it from the projects
	// root through wbhome, which is <root>/.wb. It is never $HOME/.wb: that
	// directory is retired and is reported as legacy-wb-home instead.
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
	return collectWithDiscovery(ctx, options, os.UserHomeDir, candidateGroups)
}

func collectWithDiscovery(ctx context.Context, options Options, userHomeDir func() (string, error), discoverGroups func(string, string) []group) (Report, error) {
	projectsRoot := options.ProjectsRoot
	if projectsRoot == "" {
		return Report{}, errors.New("projects root is required")
	}
	home := options.WBHome
	if home == "" {
		if _, err := userHomeDir(); err != nil {
			return Report{}, fmt.Errorf("resolve home directory: %w", err)
		}
		resolution, err := resolveLayout(projectsRoot)
		if err != nil {
			return Report{}, fmt.Errorf("resolve WB state directory: %w", err)
		}
		home = resolution.Write.Home
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

	groups := resolveGroups(discoverGroups(projectsRoot, home))
	nested := nestedRoots(groups)
	owner := map[string]string{}

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
				if !errors.Is(err, os.ErrNotExist) {
					report.Skipped = append(report.Skipped, fmt.Sprintf("%s: %v", root, err))
				}
				continue
			}
			if first, taken := owner[root]; taken {
				// Measured already: by another category it is reported as
				// same-as; within this one it is simply not walked twice
				// (TMPDIR and GOTMPDIR can be one directory).
				if first != group.name {
					category.SameAs = append(category.SameAs, first+" ("+root+")")
				}
				continue
			}
			owner[root] = group.name
			category.Roots = append(category.Roots, root)
			if options.SkipSizes {
				continue
			}
			usage, err := walk.MeasureExcluding(ctx, root, nested[filepath.Clean(root)])
			if err != nil {
				report.Skipped = append(report.Skipped, fmt.Sprintf("%s: %v", root, err))
				continue
			}
			category.ApparentBytes += usage.ApparentBytes
			category.Partial = category.Partial || usage.Unreadable > 0
			measured[group.name] = append(measured[group.name], root)
		}
		if len(category.Roots) == 0 && len(category.SameAs) == 0 {
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
//
// Every WB root comes from the projects root, never from $HOME: state lives in
// <root>/.wb and checkouts in <root>/.worktrees. The retired $HOME/.wb is its
// own category, because what it holds — legacy checkouts, a hub store,
// hand-made artifacts — is neither the current state nor anything prune may
// touch, and on 2026-11-27 reporting it as wb-state made 10 GB of it look like
// WB's own records.
func candidateGroups(projectsRoot, home string) []group {
	userHome, _ := os.UserHomeDir()
	legacyHome := legacyWBHome(userHome, home)
	// The table order of disk-prune#req:accounting-roots: when two
	// categories resolve to one directory, the first owns its bytes.
	groups := []group{
		{
			name: "wb-state", kind: "state",
			roots: []string{home},
			note:  "work logs, locks, claims, receipts; not regenerable; reported, never pruned",
		},
		{
			name: "worktrees", kind: "worktree",
			roots: checkoutStores(projectsRoot, legacyHome),
			note:  "may hold unlanded work; retire with wb clean, never rm",
		},
	}
	if legacyHome != "" {
		groups = append(groups, group{
			name: "legacy-wb-home", kind: "state",
			roots: []string{legacyHome},
			note:  "the retired $HOME/.wb: hub store, legacy reports and hand-made artifacts; legacy checkouts are counted under worktrees; a person decides",
		})
	}
	groups = append(groups,
		group{
			name: "go-build-cache", kind: "cache",
			roots: []string{goRoot(func(env goenv.Env) string { return env.GOCACHE })},
			note:  "regenerable; shared across every worktree, so per-task copies multiply it; wb disk prune trims it",
		},
		group{
			name: "go-module-cache", kind: "cache",
			roots: []string{goRoot(func(env goenv.Env) string { return env.GOMODCACHE })},
			note:  "regenerable; re-downloading costs network, not correctness; wb disk prune removes versions no go.sum references",
		},
	)
	if userHome != "" {
		groups = append(groups, group{
			name: "node-package-store", kind: "cache",
			roots: []string{
				filepath.Join(userHome, ".local", "share", "pnpm", "store"),
				filepath.Join(userHome, "Library", "pnpm", "store"),
				filepath.Join(userHome, ".npm"),
			},
			note: "hard-linked into consumers, so apparent size far exceeds what removal reclaims; wb disk prune runs pnpm store prune",
		})
	}
	groups = append(groups, group{
		name: "scratch", kind: "scratch",
		roots: scratchRoots(),
		note:  "per-task temporary space with no owner; 46 GB of it filled this machine on 2026-09-17; wb disk prune removes what nothing holds",
	})
	return groups
}

// legacyWBHome returns the retired $HOME/.wb when it exists and is not the
// state directory this run already measures as wb-state.
func legacyWBHome(userHome, stateHome string) string {
	if userHome == "" {
		return ""
	}
	legacy := filepath.Join(userHome, ".wb")
	if info, err := os.Stat(legacy); err != nil || !info.IsDir() {
		return ""
	}
	if sameDir(legacy, stateHome) {
		return ""
	}
	return legacy
}

func sameDir(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// checkoutStores lists every place a WB checkout can physically be: the central
// store <root>/.worktrees, the retired home's worktrees, the repository-local
// .worktrees of every canonical clone at whatever depth its placement uses,
// and the {org}/{repo}/.worktrees layout older fleets still have. Before this,
// only the last was scanned, so a {host}/{org}/{repo} fleet's checkouts — 2.7
// GB on the machine this was fixed on — were not reported at all.
func checkoutStores(projectsRoot, legacyHome string) []string {
	candidates := []string{filepath.Join(projectsRoot, ".worktrees")}
	if legacyHome != "" {
		candidates = append(candidates, filepath.Join(legacyHome, "worktrees"))
	}
	if repositories, err := scanLocal(projectsRoot); err == nil {
		for _, repository := range repositories {
			if repository.Path != "" {
				candidates = append(candidates, filepath.Join(repository.Path, ".worktrees"))
			}
		}
	}
	candidates = append(candidates, worktreeRoots(projectsRoot)...)
	seen := map[string]bool{}
	var roots []string
	for _, candidate := range candidates {
		clean := filepath.Clean(candidate)
		if seen[clean] {
			continue
		}
		seen[clean] = true
		roots = append(roots, clean)
	}
	return roots
}

// scanLocal is discover.ScanLocal behind a seam, so tests can describe a fleet
// without real Git clones.
var scanLocal = discover.ScanLocal

// resolveLayout is wbhome.Resolve behind the same kind of seam.
var resolveLayout = wbhome.Resolve

// nestedRoots maps each root to the other roots strictly inside it, across
// every category. The walk skips those subtrees under the outer root, so a
// checkout store inside the retired home is counted once, as worktrees.
func nestedRoots(groups []group) map[string][]string {
	var all []string
	for _, group := range groups {
		for _, root := range group.roots {
			if root != "" {
				all = append(all, filepath.Clean(root))
			}
		}
	}
	nested := map[string][]string{}
	for _, outer := range all {
		for _, inner := range all {
			if inner != outer && strings.HasPrefix(inner, outer+string(filepath.Separator)) {
				nested[outer] = append(nested[outer], inner)
			}
		}
	}
	return nested
}

// worktreeRoots finds every .worktrees directory two levels under the projects
// root, which is the {org}/{repo} layout WB maintains.
func worktreeRoots(projectsRoot string) []string {
	return worktreeRootsWithReadDir(projectsRoot, os.ReadDir)
}

// worktreeRootsWithReadDir keeps directory failures testable without permission-dependent fixtures.
func worktreeRootsWithReadDir(projectsRoot string, readDir func(string) ([]os.DirEntry, error)) []string {
	var roots []string
	orgs, err := readDir(projectsRoot)
	if err != nil {
		return nil
	}
	for _, org := range orgs {
		if !org.IsDir() || strings.HasPrefix(org.Name(), ".") {
			continue
		}
		repos, err := readDir(filepath.Join(projectsRoot, org.Name()))
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
	var roots []string
	for _, temp := range []string{os.TempDir(), os.Getenv("GOTMPDIR")} {
		if temp != "" {
			roots = append(roots, scratchRootsIn(temp)...)
		}
	}
	return roots
}

// scratchRootsIn lists one temporary directory's scratch-shaped entries the
// invoking user owns; another user's are neither measurable nor prunable.
func scratchRootsIn(temp string) []string {
	roots := []string{filepath.Join(temp, "wb")}
	entries, err := os.ReadDir(temp)
	if err != nil {
		return roots
	}
	for _, entry := range entries {
		if !entry.IsDir() || !ownedByUser(filepath.Join(temp, entry.Name())) {
			continue
		}
		// wb-pr308-lint-go-cache, wb-sync-report-focused-cache and friends:
		// per-task caches nothing retires, which is the shape that filled /tmp;
		// go-build* work directories and test directories that killed
		// processes left behind are the other shapes wb disk prune acts on.
		if scratchShaped(entry.Name()) {
			roots = append(roots, filepath.Join(temp, entry.Name()))
		}
	}
	return roots
}

// goRoot resolves a Go cache root without running go, which could write
// telemetry counters; a configuration that cannot be resolved, or a value
// such as GOCACHE=off, yields no root rather than a wrong one.
func goRoot(pick func(goenv.Env) string) string {
	return goRootWith(goenv.Resolve, pick)
}

func goRootWith(resolve func() (goenv.Env, error), pick func(goenv.Env) string) string {
	env, err := resolve()
	if err != nil {
		return ""
	}
	if root := pick(env); filepath.IsAbs(root) {
		return root
	}
	return ""
}

// findings converts the measurement into the few statements worth acting on.
func findings(report Report, minimumRatio float64, skippedSizes bool) []string {
	var found []string
	ratio := report.Filesystem.AvailableRatio()
	if report.Filesystem.TotalBytes > 0 && ratio < minimumRatio {
		found = append(found, fmt.Sprintf(
			"only %s of %s available (%.1f%%, floor %.0f%%) on the filesystem holding %s; wb disk prune plans what can go",
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
				"%s of scratch has no owning task; wb disk prune removes what nothing holds open",
				diskusage.Human(category.UnsharedBytes)))
		}
		if category.Name == "legacy-wb-home" && category.UnsharedBytes > 0 {
			found = append(found, fmt.Sprintf(
				"%s in the retired %s is not WB state and no verb manages it; a person decides what to keep (its legacy checkouts are under worktrees, retired with wb clean)",
				diskusage.Human(category.UnsharedBytes), category.Roots[0]))
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

// Probe reports the capacity of the filesystem holding path, as wb disk
// measures it: available bytes are what an ordinary process can still write.
func Probe(path string) (Filesystem, error) { return filesystemFor(path) }

// scratchShaped is every temporary-directory name wb disk measures as
// scratch; wb disk prune decides which of them may go.
func scratchShaped(name string) bool {
	for _, prefix := range []string{"wb-", "go-build", "Test", "Benchmark", "Fuzz"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// resolveGroups resolves every root through symlinks, so a symlinked
// GOMODCACHE is measured at its target and nesting is decided on real paths.
func resolveGroups(groups []group) []group {
	for g := range groups {
		for r, root := range groups[g].roots {
			if root == "" {
				continue
			}
			if real, err := filepath.EvalSymlinks(root); err == nil {
				groups[g].roots[r] = real
			}
		}
	}
	return groups
}
