package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// Orphan enumeration answers one question about a checkout nobody remembers
// creating: what is it, where did it come from, and what should happen to it.
//
// It discovers through each canonical clone's own worktree registry rather than
// by walking a directory hierarchy. That is the only source that sees all three
// layout generations at once — WB's current home, the legacy projects-root
// directory, and pre-WB checkouts sitting anywhere else — because Git records
// every linked worktree regardless of where its working tree lives.

// Layout names where a linked worktree's working tree sits, which is what
// separates a worktree WB created from one that predates it.
const (
	LayoutCurrent  = "current"
	LayoutLegacy   = "legacy"
	LayoutLocal    = "local"
	LayoutShared   = "shared"
	LayoutExternal = "external"
)

// Disposition is the recommendation, always paired with the evidence for it.
const (
	DispositionActive     = "active"
	DispositionRemove     = "remove"
	DispositionReview     = "review"
	DispositionDecide     = "decide"
	DispositionUnreadable = "unreadable"
)

// OrphanOptions selects the sweep. It is read-only in every configuration.
type OrphanOptions struct {
	ProjectsRoot string
	Base         string
	// StaleAfter is how long without a commit before an unmerged worktree needs
	// a decision. It never makes anything eligible for automatic removal.
	StaleAfter time.Duration
	// Now is injectable so ages are deterministic under test.
	Now time.Time
}

// OrphanWorktree is one linked worktree and everything known about it.
type OrphanWorktree struct {
	Path         string `json:"path"`
	CanonicalDir string `json:"canonical_dir"`
	Repository   string `json:"repository"`
	Layout       string `json:"layout"`
	Branch       string `json:"branch"`
	EffortID     string `json:"effort_id"`
	ParentEffort string `json:"parent_effort,omitempty"`
	RootEffort   string `json:"root_effort"`

	HasManifest bool   `json:"has_manifest"`
	Provenance  string `json:"provenance,omitempty"`
	HasPrompts  bool   `json:"has_prompts"`
	PromptCount int    `json:"prompt_count"`

	LastCommit time.Time `json:"last_commit,omitempty"`
	AgeDays    int       `json:"age_days"`
	Dirty      bool      `json:"dirty"`
	Missing    bool      `json:"missing"`
	Merged     bool      `json:"merged_into_target"`

	// Owner evidence, when a session declared itself. OwnerState is live,
	// gone, or unstated; only a declared PID counts, so a worktree nobody
	// claimed is unstated rather than falsely reported as abandoned.
	OwnerState string `json:"owner_state"`
	OwnerAgent string `json:"owner_agent,omitempty"`
	OwnerPID   int    `json:"owner_pid,omitempty"`

	Disposition string   `json:"disposition"`
	Evidence    []string `json:"evidence"`
}

// OrphanFamily groups worktrees by their root effort so an abandoned family is
// reported as one subject rather than as unrelated rows.
type OrphanFamily struct {
	RootEffort  string           `json:"root_effort"`
	Worktrees   []OrphanWorktree `json:"worktrees"`
	Disposition string           `json:"disposition"`
	Reason      string           `json:"reason,omitempty"`
}

// OrphanReport is the whole read-only sweep.
type OrphanReport struct {
	Families []OrphanFamily `json:"families"`
	// Residue is the checkouts no registry mentions, which the family sweep is
	// structurally unable to see. See orphans_residue.go.
	Residue   []OrphanResidue `json:"residue,omitempty"`
	Totals    OrphanTotals    `json:"totals"`
	Unscanned []string        `json:"unscanned,omitempty"`
}

type OrphanTotals struct {
	Worktrees   int            `json:"worktrees"`
	Families    int            `json:"families"`
	ByLayout    map[string]int `json:"by_layout"`
	ByDispositn map[string]int `json:"by_disposition"`
	NoManifest  int            `json:"without_manifest"`
	Dirty       int            `json:"dirty"`
	Residue     int            `json:"residue"`
}

// Orphans enumerates every linked worktree reachable from the canonical clones
// below the projects root. It never mutates a repository, a worktree, or a
// journal: triage has to be safe to run at any time, including against a fleet
// with live agents.
func Orphans(ctx context.Context, options OrphanOptions) (OrphanReport, error) {
	projectsRoot, err := absoluteProjectsRoot(options.ProjectsRoot)
	if err != nil {
		return OrphanReport{}, err
	}
	base := strings.TrimSpace(options.Base)
	if base == "" {
		base = "main"
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	staleAfter := options.StaleAfter
	if staleAfter <= 0 {
		staleAfter = 14 * 24 * time.Hour
	}

	// Classification is not a write-policy question. wbhome.Resolve narrows to
	// a single layout when WB_HOME is explicit, which is right for creating
	// state and wrong here: an isolated session must not blind the sweep to the
	// legacy hierarchy that holds most of the fleet.
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return OrphanReport{}, err
	}
	currentRoot := filepath.Join(filepath.Clean(home), "worktrees")
	legacyRoot := filepath.Join(projectsRoot, ".wb", "worktrees")

	clones, unscanned := discoverCanonicalClones(projectsRoot)
	report := OrphanReport{Unscanned: unscanned}
	report.Totals.ByLayout = map[string]int{}
	report.Totals.ByDispositn = map[string]int{}

	families := map[string][]OrphanWorktree{}
	registered := map[string]bool{}
	residueRoots := map[string]string{
		currentRoot: LayoutCurrent,
		legacyRoot:  LayoutLegacy,
	}
	for _, clone := range clones {
		residueRoots[filepath.Join(clone.path, ".worktrees")] = LayoutLocal
		linked, err := linkedWorktreesOf(ctx, clone.path)
		if err != nil {
			report.Unscanned = append(report.Unscanned, fmt.Sprintf("%s: %v", clone.path, err))
			continue
		}
		for _, worktree := range linked {
			registered[filepath.Clean(worktree.path)] = true
			entry := inspectOrphan(ctx, home, clone, worktree, currentRoot, legacyRoot, base, staleAfter, now)
			families[entry.RootEffort] = append(families[entry.RootEffort], entry)
		}
	}
	// Everything above discovered through Git and therefore cannot see a
	// checkout Git no longer registers. WB owns the roots below, so a directory
	// there that nothing lists is WB's residue to explain.
	report.Residue = residueSweep(projectsRoot, residueRoots, registered)
	sort.Slice(report.Residue, func(i, j int) bool { return report.Residue[i].Path < report.Residue[j].Path })
	report.Totals.Residue = len(report.Residue)

	for root, worktrees := range families {
		sort.Slice(worktrees, func(i, j int) bool {
			if worktrees[i].EffortID != worktrees[j].EffortID {
				return worktrees[i].EffortID < worktrees[j].EffortID
			}
			return worktrees[i].Path < worktrees[j].Path
		})
		family := OrphanFamily{RootEffort: root, Worktrees: worktrees}
		family.Disposition, family.Reason = familyDisposition(worktrees)
		report.Families = append(report.Families, family)

		for _, worktree := range worktrees {
			report.Totals.Worktrees++
			report.Totals.ByLayout[worktree.Layout]++
			report.Totals.ByDispositn[worktree.Disposition]++
			if !worktree.HasManifest {
				report.Totals.NoManifest++
			}
			if worktree.Dirty {
				report.Totals.Dirty++
			}
		}
	}
	sort.Slice(report.Families, func(i, j int) bool {
		return report.Families[i].RootEffort < report.Families[j].RootEffort
	})
	report.Totals.Families = len(report.Families)
	return report, nil
}

type canonicalClone struct {
	path       string
	repository string
}

// discoverCanonicalClones finds <projects-root>/<owner>/<repository>/.git. It
// reports what it could not read instead of failing the sweep: one unreadable
// directory must never hide the other 600 worktrees.
func discoverCanonicalClones(projectsRoot string) ([]canonicalClone, []string) {
	var clones []canonicalClone
	var unscanned []string

	owners, err := os.ReadDir(projectsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []string{fmt.Sprintf("%s: %v", projectsRoot, err)}
	}
	for _, owner := range owners {
		if !owner.IsDir() || strings.HasPrefix(owner.Name(), ".") {
			continue
		}
		ownerPath := filepath.Join(projectsRoot, owner.Name())
		repositories, err := os.ReadDir(ownerPath)
		if err != nil {
			unscanned = append(unscanned, fmt.Sprintf("%s: %v", ownerPath, err))
			continue
		}
		for _, repository := range repositories {
			if !repository.IsDir() {
				continue
			}
			path := filepath.Join(ownerPath, repository.Name())
			if info, err := os.Stat(filepath.Join(path, ".git")); err != nil || !info.IsDir() {
				continue
			}
			clones = append(clones, canonicalClone{
				path:       path,
				repository: owner.Name() + "/" + repository.Name(),
			})
		}
	}
	sort.Slice(clones, func(i, j int) bool { return clones[i].path < clones[j].path })
	return clones, unscanned
}

type linkedWorktree struct {
	path    string
	branch  string
	missing bool
}

// linkedWorktreesOf reads a clone's own worktree registry, which is what makes
// this sweep independent of where a working tree happens to live.
func linkedWorktreesOf(ctx context.Context, clone string) ([]linkedWorktree, error) {
	out, err := git(ctx, clone, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var worktrees []linkedWorktree
	var current linkedWorktree
	primary := true
	flush := func() {
		if current.path == "" {
			return
		}
		if primary {
			primary = false // the first record is the canonical checkout itself
		} else {
			worktrees = append(worktrees, current)
		}
		current = linkedWorktree{}
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			current.path = filepath.Clean(strings.TrimPrefix(line, "worktree "))
		case strings.HasPrefix(line, "branch "):
			current.branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "prunable" || strings.HasPrefix(line, "prunable "):
			current.missing = true
		}
	}
	flush()
	return worktrees, nil
}

func inspectOrphan(
	ctx context.Context,
	home string,
	clone canonicalClone,
	worktree linkedWorktree,
	currentRoot, legacyRoot string,
	base string,
	staleAfter time.Duration,
	now time.Time,
) OrphanWorktree {
	entry := OrphanWorktree{
		Path:         worktree.path,
		CanonicalDir: clone.path,
		Repository:   clone.repository,
		Branch:       worktree.branch,
		Layout:       orphanLayoutOf(ctx, home, clone, worktree.path, currentRoot, legacyRoot),
		Missing:      worktree.missing,
	}
	if _, err := os.Stat(worktree.path); err != nil {
		entry.Missing = true
	}

	// Identity comes from the worktree's own manifest when it has one. That is
	// the whole point of holding the journal in the checkout; everything below
	// is the fallback for the worktrees that predate it.
	if manifest, err := ReadManifest(worktree.path); err == nil {
		entry.HasManifest = true
		entry.Provenance = manifest.Provenance
		entry.EffortID = manifest.EffortID
		entry.Evidence = append(entry.Evidence, "identity from its own manifest")
	} else {
		entry.EffortID = effortFromWorktreePath(worktree.path)
		if entry.EffortID == "" {
			entry.EffortID = strings.ReplaceAll(strings.TrimPrefix(worktree.branch, "feature/"), "/", ".")
		}
		if entry.EffortID == "" {
			entry.EffortID = filepath.Base(worktree.path)
		}
		entry.Evidence = append(entry.Evidence, "identity reconstructed from path and branch; no manifest")
	}
	if prompts, err := ListPrompts(worktree.path); err == nil && len(prompts) > 0 {
		entry.HasPrompts = true
		entry.PromptCount = len(prompts)
	}
	entry.ParentEffort = ParentEffort(entry.EffortID)
	entry.RootEffort = rootEffort(entry.EffortID)

	if entry.Missing {
		entry.Disposition = DispositionReview
		entry.Evidence = append(entry.Evidence, "registered with Git but its working tree is gone; git worktree prune would clear it")
		return entry
	}

	if commit, err := git(ctx, worktree.path, "log", "-1", "--format=%cI"); err == nil {
		if parsed, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(commit)); parseErr == nil {
			entry.LastCommit = parsed.UTC()
			entry.AgeDays = int(now.Sub(parsed).Hours() / 24)
		}
	}
	if clean, err := cleanWorktree(ctx, worktree.path); err == nil {
		entry.Dirty = !clean
	}
	if worktree.branch != "" {
		if merged, err := isAncestor(ctx, clone.path, worktree.branch, "origin/"+base); err == nil {
			entry.Merged = merged
		}
	}
	entry.OwnerState, entry.OwnerAgent, entry.OwnerPID = DeclaredOwner(worktree.path)
	entry.Disposition, entry.Evidence = orphanDisposition(entry, base, staleAfter, now)
	return entry
}

func orphanDisposition(entry OrphanWorktree, base string, staleAfter time.Duration, now time.Time) (string, []string) {
	evidence := entry.Evidence
	switch {
	case entry.Dirty:
		return DispositionReview, append(evidence,
			"holds uncommitted changes; removing it would destroy work that exists nowhere else")
	case entry.Merged:
		return DispositionRemove, append(evidence,
			fmt.Sprintf("its branch is already contained in origin/%s, so nothing would be lost", base))

	// A running declared session is proof, not a guess, so it outranks every
	// age heuristic below — including the no-commit case, since a session that
	// has not committed yet is working rather than abandoned.
	case entry.OwnerState == OwnerLive:
		return DispositionActive, append(evidence, fmt.Sprintf(
			"declared owner %s (PID %d) is running", ownerLabel(entry), entry.OwnerPID))

	case entry.LastCommit.IsZero():
		return DispositionDecide, append(evidence,
			"carries no commit of its own; it may never have started")

	// A declared session that has exited settles the question the age
	// heuristic can only guess at. Recent work whose owner is gone is exactly
	// what used to be reported as "likely still in use".
	case entry.OwnerState == OwnerGone:
		return DispositionDecide, append(evidence, fmt.Sprintf(
			"declared owner %s (PID %d) has exited, leaving unmerged work from %d days ago; it needs a decision",
			ownerLabel(entry), entry.OwnerPID, entry.AgeDays))

	case now.Sub(entry.LastCommit) < staleAfter:
		return DispositionActive, append(evidence, fmt.Sprintf(
			"committed %d days ago and no session declared itself, so this is inferred from age alone; run wb worktree own to make it knowable",
			entry.AgeDays))
	default:
		return DispositionDecide, append(evidence, fmt.Sprintf(
			"unmerged and idle for %d days; it needs a decision, and WB will not make it",
			entry.AgeDays))
	}
}

// ownerLabel names the declared owner, falling back to its PID when the
// harness declared no agent name.
func ownerLabel(entry OrphanWorktree) string {
	if entry.OwnerAgent != "" {
		return entry.OwnerAgent
	}
	return "an unnamed session"
}

// familyDisposition is deliberately conservative: a family is only removable
// when every one of its worktrees is, because a parent whose child still holds
// work is exactly the case that must not be swept.
func familyDisposition(worktrees []OrphanWorktree) (string, string) {
	counts := map[string]int{}
	for _, worktree := range worktrees {
		counts[worktree.Disposition]++
	}
	switch {
	case counts[DispositionActive] > 0:
		return DispositionActive, "at least one worktree in this effort is still in use"
	case counts[DispositionReview] > 0:
		return DispositionReview, "at least one worktree holds uncommitted or missing state"
	case counts[DispositionRemove] == len(worktrees):
		return DispositionRemove, "every worktree in this effort has landed"
	default:
		return DispositionDecide, "this effort has unmerged idle work and needs a decision"
	}
}

func orphanLayoutOf(ctx context.Context, home string, clone canonicalClone, worktree, currentRoot, legacyRoot string) string {
	switch {
	case pathWithin(currentRoot, worktree):
		return LayoutCurrent
	case pathWithin(legacyRoot, worktree):
		return LayoutLegacy
	case pathWithin(filepath.Join(clone.path, ".worktrees"), worktree):
		return LayoutLocal
	}
	// A changed worktrees.root must not turn a live managed member into an
	// external checkout. The active claim is the ownership proof; a path shape
	// alone is never enough to make an arbitrary external worktree managed.
	if claim, _, _, err := activeWorkLogClaim(home, worktree); err == nil {
		// Adoption retains an explicit durable provenance marker in its claim.
		// Its external path can coincidentally have the shared-root shape, but
		// that does not relocate it under WB management: the pointer registration
		// remains its authority for List/Cleanup/Abort and for idempotent adopt.
		if claim.AcquiredVia != "adopted" {
			if _, layoutErr := claimedSharedWorktreeLayout(worktree, claim); layoutErr == nil {
				return LayoutShared
			}
		}
	}
	return LayoutExternal
}

// rootEffort is the first segment of a dotted effort path: the feature effort a
// family of sub-agent task efforts belongs to.
func rootEffort(effort string) string {
	if index := strings.Index(effort, "."); index > 0 {
		return effort[:index]
	}
	return effort
}

// BackfillOptions drives the one-time adoption sweep. It defaults to a dry run
// because it writes into worktrees that may have live agents in them.
type BackfillOptions struct {
	ProjectsRoot string
	Base         string
	Apply        bool
}

// BackfillResult is what happened, or would happen, to one worktree.
type BackfillResult struct {
	Path       string `json:"path"`
	Repository string `json:"repository"`
	EffortID   string `json:"effort_id,omitempty"`
	Layout     string `json:"layout"`
	Action     string `json:"action"`
	Reason     string `json:"reason,omitempty"`
}

// Backfill actions.
const (
	BackfillWouldWrite = "would_write"
	BackfillWritten    = "written"
	BackfillPresent    = "already_present"
	BackfillSkipped    = "skipped"
)

// Backfill gives every reachable worktree a manifest so the fleet becomes
// explicable without anyone stopping work.
//
// It is additive and idempotent by construction: `.wb/local/` is a new path, no
// existing file moves, and no working tree is touched, so a worktree holding
// uncommitted changes is unaffected. Re-running it is safe, which matters
// because a sweep over hundreds of worktrees will be interrupted.
//
// It never fabricates a prompt. A worktree whose instructions were never
// recorded genuinely has none; the admission gate's remedy is how it gets its
// first real one.
func Backfill(ctx context.Context, options BackfillOptions) ([]BackfillResult, error) {
	report, err := Orphans(ctx, OrphanOptions{
		ProjectsRoot: options.ProjectsRoot,
		Base:         options.Base,
	})
	if err != nil {
		return nil, err
	}
	var results []BackfillResult
	for _, family := range report.Families {
		for _, worktree := range family.Worktrees {
			result := BackfillResult{
				Path: worktree.Path, Repository: worktree.Repository,
				EffortID: worktree.EffortID, Layout: worktree.Layout,
			}
			switch {
			case worktree.HasManifest:
				result.Action = BackfillPresent
			case worktree.Missing:
				result.Action = BackfillSkipped
				result.Reason = "working tree is gone; git worktree prune would clear the registration"
			case !options.Apply:
				result.Action = BackfillWouldWrite
			default:
				manifest, writeErr := ReconstructManifest(ctx, worktree.Path)
				if writeErr != nil {
					result.Action = BackfillSkipped
					result.Reason = writeErr.Error()
				} else {
					result.Action = BackfillWritten
					result.EffortID = manifest.EffortID
				}
			}
			results = append(results, result)
		}
	}
	return results, nil
}
