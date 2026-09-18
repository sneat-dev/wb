package layout

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/checkoutmarker"
	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/repopath"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// migrationsDirName holds every migration's manifest under <root>/.wb/.
const migrationsDirName = "layout-migrations"

// MigrateOptions configures wb layout migrate.
type MigrateOptions struct {
	// Repositories restricts migration to these owner/repository slugs. Empty
	// means every legacy clone under the root.
	Repositories []string
	// Apply moves clones; without it the command only plans.
	Apply bool
	// ClonesOnly skips relocating managed task checkouts: clones move and
	// their linked worktrees repoint, exactly as before this option existed.
	ClonesOnly bool
	// UndoID reverses a previously completed manifest instead of migrating.
	UndoID string
	Now    func() time.Time
}

// MigrateWorktree is one linked worktree a clone move repoints.
type MigrateWorktree struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// MigrateClone is one legacy or host-level clone's migration plan or outcome.
type MigrateClone struct {
	Repository  string            `json:"repository"`
	Source      string            `json:"source"`
	Destination string            `json:"destination"`
	Worktrees   []MigrateWorktree `json:"worktrees,omitempty"`
	// Status is one of: planned, done, already_done, needs_repair, repaired,
	// skipped, failed, reversed. needs_repair is a dry-run-only finding: a
	// clone already at its host-level path whose worktree registration was
	// left stranded by an earlier or partial move; like planned, it is not a
	// failure — it names what `--apply` will fix.
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	// Relocations lists this clone's managed task checkouts whose placement
	// differs from the store-mode placement, and what became of each: moved
	// via the existing `wb worktree relocate` implementation, left in place
	// with a finding, or reported unmanaged.
	Relocations []MigrateRelocation `json:"relocations,omitempty"`
	// Head is the clone's own HEAD at plan time, carried through to the
	// undo manifest. It is not part of the report's public JSON shape.
	Head string `json:"-"`
}

// MigrateRelocation is one managed task checkout's relocation plan or
// outcome, considered after its clone reached its host-level placement.
type MigrateRelocation struct {
	Task        string `json:"task,omitempty"`
	Source      string `json:"source"`
	Destination string `json:"destination,omitempty"`
	// Status is one of: planned, done, skipped, failed, unmanaged. unmanaged
	// names a linked worktree with no WB task identity: Git repointed it
	// along with the clone, but it is never relocated.
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// MigrateReport is the result of one wb layout migrate run.
type MigrateReport struct {
	SchemaVersion         int            `json:"schema_version"`
	ProjectsRoot          string         `json:"projects_root"`
	ObservedAt            time.Time      `json:"observed_at"`
	DryRun                bool           `json:"dry_run"`
	Undo                  bool           `json:"undo,omitempty"`
	ManifestID            string         `json:"manifest_id,omitempty"`
	ManifestPath          string         `json:"manifest_path,omitempty"`
	DaemonRestartRequired bool           `json:"daemon_restart_required,omitempty"`
	Clones                []MigrateClone `json:"clones"`
	// KeptOwners lists every legacy owner directory a migration left in
	// place because it still holds something, with why.
	KeptOwners []MigrateKeptOwner `json:"kept_owners,omitempty"`
	// Notes carries run-level observations that are not per-clone, such as
	// the busy-process check being unsupported on this OS.
	Notes []string `json:"notes,omitempty"`
}

// MigrateKeptOwner is one legacy owner directory a migration did not remove,
// with the reason it was kept.
type MigrateKeptOwner struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// MigrateFailed reports whether a migrate run left any clone refused or
// failed, which is the condition the command exits with the findings code
// for.
func MigrateFailed(report MigrateReport) bool {
	for _, clone := range report.Clones {
		if clone.Status == "skipped" || clone.Status == "failed" {
			return true
		}
		for _, relocation := range clone.Relocations {
			if relocation.Status == "skipped" || relocation.Status == "failed" {
				return true
			}
		}
	}
	return false
}

// migrationManifest is the durable record `--apply` writes before its first
// move and `--undo` reads back. It is written once per clone outcome so an
// interrupted run leaves a readable partial manifest, but re-running
// `--apply` never consults it: what is safe to migrate is always recomputed
// from the live filesystem and Git state, never from a previous run's record.
type migrationManifest struct {
	SchemaVersion int             `json:"schema_version"`
	ID            string          `json:"id"`
	ProjectsRoot  string          `json:"projects_root"`
	CreatedAt     time.Time       `json:"created_at"`
	Clones        []manifestClone `json:"clones"`
}

type manifestClone struct {
	Repository  string               `json:"repository"`
	Source      string               `json:"source"`
	Destination string               `json:"destination"`
	Head        string               `json:"head"`
	Worktrees   []MigrateWorktree    `json:"worktrees,omitempty"`
	Status      string               `json:"status"`
	Reason      string               `json:"reason,omitempty"`
	CompletedAt *time.Time           `json:"completed_at,omitempty"`
	Reversed    bool                 `json:"reversed,omitempty"`
	ReversedAt  *time.Time           `json:"reversed_at,omitempty"`
	Relocations []manifestRelocation `json:"relocations,omitempty"`
}

// manifestRelocation is one manifestClone's durable relocation record: what
// the relocation post-pass found or did for one managed task checkout, and
// (once --undo reverses it) whether it has been reversed.
type manifestRelocation struct {
	Task        string `json:"task,omitempty"`
	Source      string `json:"source"`
	Destination string `json:"destination,omitempty"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	Reversed    bool   `json:"reversed,omitempty"`
}

// Migrate plans, and with Apply performs, moving every legacy
// <root>/<org>/<repo> canonical clone under root to its host-level
// <root>/<host>/<org>/<repo> placement, repointing every worktree Git has
// registered against it. See the "Clone migration" section of the
// projects-root-layout Feature for the full contract.
func Migrate(ctx context.Context, projectsRoot string, options MigrateOptions) (MigrateReport, error) {
	root, err := absoluteRoot(projectsRoot)
	if err != nil {
		return MigrateReport{}, err
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if options.UndoID != "" {
		return migrateUndo(ctx, root, options)
	}
	return migrateApply(ctx, root, options)
}

func migrateApply(ctx context.Context, root string, options MigrateOptions) (MigrateReport, error) {
	report := MigrateReport{
		SchemaVersion: 1,
		ProjectsRoot:  root,
		ObservedAt:    options.Now(),
		DryRun:        !options.Apply,
	}
	if !worktrees.BusyProcessCheckSupported {
		report.Notes = append(report.Notes, busyProcessUnsupportedNote())
	}
	if options.Apply {
		lock, lockErr := acquireMigrationLock(root)
		if lockErr != nil {
			return MigrateReport{}, lockErr
		}
		defer lock.release()
	}
	wanted := repositorySet(options.Repositories)
	owners, _ := repopath.Owners(root)

	// Clones already at the host level are done and untouched, whether or not
	// this run migrates anything else: an interrupted or repeated run must
	// report them exactly this way instead of them silently vanishing from
	// the report.
	for _, owner := range owners {
		if owner.Host == "" {
			continue
		}
		repositories, readErr := os.ReadDir(owner.Path)
		if readErr != nil {
			continue
		}
		for _, entry := range repositories {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(owner.Path, entry.Name())
			if !isCanonicalGitDir(path) {
				continue
			}
			slug := owner.Name + "/" + entry.Name()
			if !wanted.matches(slug) {
				continue
			}
			clone := MigrateClone{Repository: slug, Source: path, Destination: path}
			// legacyClonePath is this clone's pre-migration <root>/<org>/<repo>
			// path, derived from its current host-level location regardless
			// of whether it was ever actually migrated through here. It need
			// not exist; ReconcileClonePlacement only uses it to recognise a
			// nested worktree that moved along with an earlier, unrepaired
			// rename.
			legacyClonePath := filepath.Join(root, owner.Name, entry.Name())
			status, informational, reconcileErr := worktrees.ReconcileClonePlacement(ctx, path, legacyClonePath, options.Apply)
			switch {
			case reconcileErr != nil:
				clone.Status = "failed"
				clone.Reason = reconcileErr.Error()
			case status == "repaired":
				clone.Status = "repaired"
				clone.Reason = "worktree registration was stranded after an earlier or partial move; repaired in place"
			case status == "needs_repair":
				clone.Status = "needs_repair"
				clone.Reason = "worktree registration was stranded after an earlier or partial move; run --apply to repair"
			default:
				clone.Status = "already_done"
				clone.Reason = "already at the host-level path"
			}
			if len(informational) > 0 {
				report.Notes = append(report.Notes, fmt.Sprintf(
					"%s: worktree breakage not caused by migration, left as-is: %s",
					slug, strings.Join(informational, ", ")))
			}
			// A clone already at the host level -- moved by this run or an
			// earlier one -- is still in scope for relocating its managed
			// checkouts (REQ: migration-relocates-managed-worktrees says "moved
			// by this run or earlier"): populate its linked-worktree list the
			// same way a freshly-planned clone move does, so relocateClones
			// below does not skip it for having none.
			if clone.Status != "failed" {
				if plan, planErr := worktrees.PlanCloneMove(ctx, path, path); planErr == nil {
					for _, worktree := range plan.Worktrees {
						if worktree.Source == path {
							continue
						}
						clone.Worktrees = append(clone.Worktrees, MigrateWorktree{Source: worktree.Source, Destination: worktree.Destination})
					}
				}
			}
			report.Clones = append(report.Clones, clone)
		}
	}

	type candidate struct {
		slug, path, ownerName, repoName string
	}
	var candidates []candidate
	for _, owner := range owners {
		if owner.Host != "" {
			continue
		}
		repositories, readErr := os.ReadDir(owner.Path)
		if readErr != nil {
			continue
		}
		for _, entry := range repositories {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(owner.Path, entry.Name())
			if !isCanonicalGitDir(path) {
				continue
			}
			slug := owner.Name + "/" + entry.Name()
			if !wanted.matches(slug) {
				continue
			}
			candidates = append(candidates, candidate{slug: slug, path: path, ownerName: owner.Name, repoName: entry.Name()})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].path < candidates[j].path })

	var manifest *migrationManifest
	var manifestPath string
	planned := make([]MigrateClone, 0, len(candidates))
	for _, cand := range candidates {
		clone := planLegacyClone(ctx, root, cand.path, cand.ownerName, cand.repoName)
		planned = append(planned, clone)
	}

	if options.Apply {
		var eligible []MigrateClone
		for _, clone := range planned {
			if clone.Status == "planned" {
				eligible = append(eligible, clone)
			}
		}
		if len(eligible) > 0 {
			var writeErr error
			manifest, manifestPath, writeErr = createMigrationManifest(root, eligible, options.Now())
			if writeErr != nil {
				return MigrateReport{}, writeErr
			}
			report.ManifestID = manifest.ID
			report.ManifestPath = manifestPath
		}
		for index := range planned {
			if planned[index].Status != "planned" {
				continue
			}
			now := options.Now()
			applyOneClone(ctx, root, &planned[index], now)
			if manifest != nil {
				updateManifestClone(manifest, planned[index], now)
				if writeErr := writeManifest(manifestPath, manifest); writeErr != nil {
					// The manifest is the durable audit/undo record; a run
					// that cannot keep it up to date must stop rather than
					// keep moving clones it can no longer account for. What
					// was already recorded and applied stays in report so the
					// caller is not left blind about it.
					report.Clones = append(report.Clones, planned...)
					report.ManifestID, report.ManifestPath = manifest.ID, manifestPath
					return report, fmt.Errorf("record migration manifest outcome for %s: %w", planned[index].Repository, writeErr)
				}
			}
		}
		invalidateLocalIndex(root)
	}

	report.Clones = append(report.Clones, planned...)
	sort.Slice(report.Clones, func(i, j int) bool { return report.Clones[i].Repository < report.Clones[j].Repository })

	if !options.ClonesOnly {
		relocateClones(ctx, root, report.Clones, options.Apply, options.Now())
		if options.Apply {
			var withRelocations []MigrateClone
			for _, clone := range report.Clones {
				if len(clone.Relocations) > 0 {
					withRelocations = append(withRelocations, clone)
				}
			}
			if len(withRelocations) > 0 {
				if manifest == nil {
					var writeErr error
					manifest, manifestPath, writeErr = createMigrationManifest(root, nil, options.Now())
					if writeErr != nil {
						return report, writeErr
					}
				}
				for _, clone := range withRelocations {
					setManifestCloneRelocations(manifest, clone)
				}
				if writeErr := writeManifest(manifestPath, manifest); writeErr != nil {
					return report, fmt.Errorf("record migration manifest relocations: %w", writeErr)
				}
				report.ManifestID, report.ManifestPath = manifest.ID, manifestPath
			}
		}
	}

	if options.Apply {
		var ownerCandidates []string
		for _, clone := range planned {
			if clone.Status == "done" {
				ownerCandidates = append(ownerCandidates, filepath.Dir(clone.Source))
			}
		}
		report.KeptOwners = finalizeKeptOwners(ownerCandidates)
	}
	report.DaemonRestartRequired = daemonRunning(root)
	return report, nil
}

// finalizeKeptOwners recomputes, once at the very end of a migrate or undo
// run, which of the given candidate owner (or host) directories are still on
// disk and non-empty. This is the only trustworthy way to report it: a
// directory one clone observed non-empty earlier in the same run, which a
// later clone in that same run then emptied and removed, must never still be
// listed as kept just because of what an earlier clone saw before that later
// clone ran. Deduplicates by path; candidates are tried in the given order,
// so a caller that lists an owner directory before the host directory above
// it gives the owner directory a chance to be removed first, making the host
// directory's own check accurate.
func finalizeKeptOwners(candidates []string) []MigrateKeptOwner {
	seen := make(map[string]bool, len(candidates))
	var kept []MigrateKeptOwner
	for _, dir := range candidates {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		if _, statErr := os.Stat(dir); statErr != nil {
			continue
		}
		if removed, reason := removeEmptyLegacyOwner(dir); !removed {
			kept = append(kept, MigrateKeptOwner{Path: dir, Reason: reason})
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Path < kept[j].Path })
	return kept
}

// busyProcessUnsupportedNote is the single run-level note migrateApply and
// migrateUndo add when the busy-process check cannot run on this OS, so the
// report says so once instead of silently skipping the check.
func busyProcessUnsupportedNote() string {
	return "busy-process check skipped: not supported on " + runtime.GOOS
}

// planLegacyClone evaluates one legacy clone for the refusal reasons the
// Feature names, returning it either "planned" (eligible to migrate) or
// "skipped" with the reason.
func planLegacyClone(ctx context.Context, root, path, ownerName, repoName string) MigrateClone {
	slug := ownerName + "/" + repoName
	clone := MigrateClone{Repository: slug, Source: path}
	address, err := OriginAddress(ctx, path)
	if err != nil {
		clone.Status = "skipped"
		clone.Reason = "no usable origin remote: " + err.Error()
		return clone
	}
	if !strings.EqualFold(address.Org, ownerName) || !strings.EqualFold(address.Repo, repoName) {
		clone.Status = "skipped"
		clone.Reason = fmt.Sprintf("origin owner/repository %s does not match clone path %s", address.Slug(), slug)
		return clone
	}
	if address.Host == "" {
		clone.Status = "skipped"
		clone.Reason = "origin remote does not identify a forge host"
		return clone
	}
	destination := filepath.Join(root, address.Host, address.Org, address.Repo)
	clone.Destination = destination
	if _, statErr := os.Lstat(destination); statErr == nil {
		clone.Status = "skipped"
		clone.Reason = "destination already exists: " + destination
		return clone
	}
	plan, err := worktrees.PlanCloneMove(ctx, path, destination)
	if err != nil {
		clone.Status = "skipped"
		clone.Reason = "cannot enumerate linked worktrees: " + err.Error()
		return clone
	}
	var worktreePaths []string
	for _, worktree := range plan.Worktrees {
		if worktree.Source == path {
			// The clone's own entry in its worktree registry; already
			// accounted for by Source/Destination above.
			clone.Head = worktree.Head
			continue
		}
		clone.Worktrees = append(clone.Worktrees, MigrateWorktree{Source: worktree.Source, Destination: worktree.Destination})
		worktreePaths = append(worktreePaths, worktree.Source)
	}
	if reason := refuseClone(ctx, root, slug, path, worktreePaths); reason != "" {
		clone.Status = "skipped"
		clone.Reason = reason
		return clone
	}
	clone.Status = "planned"
	return clone
}

// refuseClone runs every refusal check REQ: clone-migration-refusals names
// against clonePath and its linked worktreePaths, in the same order
// planLegacyClone applies at plan time. applyOneClone and migrateUndo call
// this again immediately before a clone actually moves, so a condition that
// changed between planning and execution is caught instead of assumed still
// true.
//
// Every one of these conditions — on the canonical clone path or on any
// linked worktree — refuses the whole clone, including all of its
// checkouts, in every mode: a clone move carries its in-clone worktrees with
// it, so moving a clone out from under a live task pulls the directory out
// from under a running session. Only the relocation-specific conditions
// (task lock held, relocation destination exists) leave a single checkout
// in place while its clone still migrates; see relocateClones.
func refuseClone(ctx context.Context, root, slug, clonePath string, worktreePaths []string) string {
	if reason, err := worktrees.GitOperationInProgress(ctx, clonePath); err != nil {
		return "cannot inspect Git state: " + err.Error()
	} else if reason != "" {
		return reason
	}
	for _, worktree := range worktreePaths {
		if reason, err := worktrees.GitOperationInProgress(ctx, worktree); err != nil {
			return "linked worktree " + worktree + ": cannot inspect Git state: " + err.Error()
		} else if reason != "" {
			return "linked worktree " + worktree + ": " + reason
		}
	}
	if claim := liveClaimReason(root, slug); claim != "" {
		return claim
	}
	paths := append([]string{clonePath}, worktreePaths...)
	if parked := worktrees.ParkedSessionReason(root, paths); parked != "" {
		return parked
	}
	if worktrees.BusyProcessCheckSupported {
		if busy := worktrees.BusyProcessReason(paths); busy != "" {
			return busy
		}
	}
	return ""
}

func liveClaimReason(root, slug string) string {
	claims, err := worktrees.ListActiveClaimSummaries(root, "")
	if err != nil {
		return ""
	}
	for _, claim := range claims {
		if strings.EqualFold(claim.Repository, slug) {
			return fmt.Sprintf("a live Work Log claim (task %q) holds this clone or a linked worktree", claim.Task)
		}
	}
	return ""
}

func applyOneClone(ctx context.Context, root string, clone *MigrateClone, now time.Time) {
	// Re-derive the worktree list and remote immediately before moving,
	// rather than trusting the plan computed earlier in this run: a legacy
	// worktree could have been added, removed, or re-registered since. This
	// also gives RecordCloneMoveRelocationIntents the current HeadSHA per
	// worktree, which MigrateWorktree does not carry.
	plan, err := worktrees.PlanCloneMove(ctx, clone.Source, clone.Destination)
	if err != nil {
		clone.Status = "failed"
		clone.Reason = "cannot re-verify linked worktrees immediately before move: " + err.Error()
		return
	}
	var worktreePaths []string
	for _, worktree := range plan.Worktrees {
		if worktree.Source != clone.Source {
			worktreePaths = append(worktreePaths, worktree.Source)
		}
	}
	// Re-run every refusal check immediately before moving, not only at plan
	// time: a claim, a Git operation, or a parked session can all appear in
	// the time between planning the whole run and reaching this one clone.
	if reason := refuseClone(ctx, root, clone.Repository, clone.Source, worktreePaths); reason != "" {
		clone.Status = "skipped"
		clone.Reason = "refused immediately before move: " + reason
		return
	}
	pending, intentErr := worktrees.RecordCloneMoveRelocationIntents(root, plan.Worktrees, now)
	if intentErr != nil {
		clone.Status = "failed"
		clone.Reason = intentErr.Error()
		return
	}
	result, err := worktrees.ApplyCloneMove(ctx, clone.Source, clone.Destination)
	if err != nil {
		clone.Status = "failed"
		clone.Reason = err.Error()
		return
	}
	if err := worktrees.FinalizeCloneMoveRelocationReceipts(pending, now); err != nil {
		// The clone has already moved and been verified; a claim's location
		// resolution falling back to its frozen path is the only consequence
		// of a receipt failure here, so this is reported, not treated as a
		// failed move.
		clone.Reason = "moved, but recording the relocation receipt failed: " + err.Error()
	}
	// Use the fresh worktree list ApplyCloneMove actually verified, rather
	// than the plan-time list computed earlier in this run.
	clone.Worktrees = nil
	for _, worktree := range result.Worktrees {
		if worktree.Source == clone.Source {
			continue
		}
		clone.Worktrees = append(clone.Worktrees, MigrateWorktree{Source: worktree.Source, Destination: worktree.Destination})
	}
	regenerateMarkers(root, clone.Destination)
	for _, worktree := range clone.Worktrees {
		regenerateMarkers(root, worktree.Destination)
	}
	// Best-effort immediate cleanup so a long run frees directories as it
	// goes; migrateApply's own final pass (finalizeKeptOwners), not this
	// attempt, is what the report's KeptOwners is built from — a sibling
	// clone processed later in the same run can still empty this same
	// directory, which only a pass after the whole loop can see accurately.
	_, _ = removeEmptyLegacyOwner(filepath.Dir(clone.Source))
	clone.Status = "done"
}

// relocateClones relocates every managed task checkout under clones whose
// placement differs from its store-mode placement, for every clone that
// reached (or, in a dry run, is planned to reach) its host-level placement
// this run or earlier. It calls the existing `wb worktree relocate`
// implementation per checkout — never a copy of it — and mutates clones in
// place with what it planned or did. Repository-local store mode leaves
// in-clone checkouts where they are: no relocation is attempted for such a
// clone. A linked worktree with no WB task identity is reported unmanaged;
// Git already repointed it along with its clone, and it is never relocated.
func relocateClones(ctx context.Context, root string, clones []MigrateClone, apply bool, now time.Time) {
	listed, listErr := worktrees.ListWithDiagnostics(ctx, worktrees.ListOptions{ProjectsRoot: root})
	managed := map[string]worktrees.ListResult{}
	if listErr == nil {
		for _, entry := range listed.Results {
			managed[filepath.Clean(entry.WorktreeDir)] = entry
		}
	}
	for index := range clones {
		clone := &clones[index]
		switch clone.Status {
		case "done", "already_done", "repaired", "needs_repair", "planned":
		default:
			continue
		}
		if len(clone.Worktrees) == 0 || clone.Destination == "" {
			continue
		}
		placement, placementErr := worktrees.ResolveUserWorktreePlacement(root, clone.Destination)
		if placementErr != nil || placement.RepositoryLocal {
			continue
		}
		moved := clone.Status == "done" || clone.Status == "repaired"
		for _, worktree := range clone.Worktrees {
			matchPath := worktree.Source
			if moved {
				matchPath = worktree.Destination
			}
			entry, isManaged := managed[filepath.Clean(matchPath)]
			if !isManaged {
				clone.Relocations = append(clone.Relocations, MigrateRelocation{
					Source: matchPath, Status: "unmanaged",
					Reason: "linked worktree has no WB task identity; repointed only",
				})
				continue
			}
			destination, destErr := placement.Path(entry.Task, entry.Repository)
			if destErr != nil {
				clone.Relocations = append(clone.Relocations, MigrateRelocation{
					Task: entry.Task, Source: matchPath, Status: "skipped",
					Reason: "cannot resolve store-mode placement: " + destErr.Error(),
				})
				continue
			}
			if filepath.Clean(destination) == filepath.Clean(matchPath) {
				continue
			}
			relocation := MigrateRelocation{Task: entry.Task, Source: matchPath, Destination: destination}
			outcome, relocateErr := worktrees.Relocate(ctx, worktrees.RelocateOptions{
				ProjectsRoot: root, Task: entry.Task, Filter: entry.Repository, To: "shared",
				Apply: apply, Now: func() time.Time { return now },
				LeaveActiveTasksInPlace: true,
			})
			result, found := findRelocateResult(outcome.Results, matchPath)
			switch {
			case relocateErr != nil:
				relocation.Status, relocation.Reason = "failed", relocateErr.Error()
			case !found:
				relocation.Status, relocation.Reason = "skipped", "relocation plan did not include this checkout"
			case !result.Eligible:
				// The founder's decision (2026-09-18): a finished task's
				// checkout is the safe case and is relocated (see Relocate's
				// claimForRelocation); only an ACTIVE task's checkout, or one
				// a per-checkout safety check refuses, is left in place here,
				// and result.Reason already says which and why (e.g. "active
				// task — relocate after it finishes").
				relocation.Status = "skipped"
				relocation.Reason = result.Reason
			case result.AlreadyThere:
				continue
			case !apply:
				relocation.Status = "planned"
			case result.Applied && result.Finalized:
				relocation.Status, relocation.Destination = "done", result.Destination
			default:
				relocation.Status = "failed"
				relocation.Reason = result.Reason
				if relocation.Reason == "" {
					relocation.Reason = "relocation did not complete"
				}
			}
			clone.Relocations = append(clone.Relocations, relocation)
		}
	}
}

func findRelocateResult(results []worktrees.RelocateResult, worktreeDir string) (worktrees.RelocateResult, bool) {
	for _, result := range results {
		if filepath.Clean(result.WorktreeDir) == filepath.Clean(worktreeDir) {
			return result, true
		}
	}
	return worktrees.RelocateResult{}, false
}

// setManifestCloneRelocations records clone's relocation outcomes on its
// manifest entry, creating one when clone had no clone move of its own this
// run (an already-host-level clone whose managed checkouts still needed
// relocating) — the only way such a checkout gets an undo-able record.
func setManifestCloneRelocations(manifest *migrationManifest, clone MigrateClone) {
	for index := range manifest.Clones {
		if manifest.Clones[index].Source == clone.Source && manifest.Clones[index].Destination == clone.Destination {
			manifest.Clones[index].Relocations = convertRelocations(clone.Relocations)
			return
		}
	}
	entry := manifestClone{
		Repository: clone.Repository, Source: clone.Source, Destination: clone.Destination,
		Head: clone.Head, Status: clone.Status, Reason: clone.Reason,
		Relocations: convertRelocations(clone.Relocations),
	}
	entry.Worktrees = append(entry.Worktrees, clone.Worktrees...)
	manifest.Clones = append(manifest.Clones, entry)
}

func convertRelocations(relocations []MigrateRelocation) []manifestRelocation {
	var out []manifestRelocation
	for _, relocation := range relocations {
		out = append(out, manifestRelocation{
			Task: relocation.Task, Source: relocation.Source, Destination: relocation.Destination,
			Status: relocation.Status, Reason: relocation.Reason,
		})
	}
	return out
}

func regenerateMarkers(root, path string) {
	options := checkoutmarker.DescribeOptions{ProjectsRoot: root, Version: "wb"}
	inspection, err := checkoutmarker.Describe(path, options)
	if err != nil {
		return
	}
	_, _ = checkoutmarker.Apply(inspection.Descriptor, inspection.ExcludePath)
}

// removeEmptyLegacyOwner removes a legacy owner directory left empty by a
// migration. A directory that still holds anything — another clone this run
// did not touch, stray files, or its own `.git` because the owner directory
// is itself a checkout — is kept untouched; emptiness is the only test.
// removed is true only when the directory was actually removed; reason
// explains why it was kept (or why it could not be inspected or removed), for
// the caller to report — the Feature requires every kept legacy owner
// directory to be named, not silently left out of the report.
func removeEmptyLegacyOwner(ownerPath string) (removed bool, reason string) {
	entries, err := os.ReadDir(ownerPath)
	if err != nil {
		return false, "cannot inspect legacy owner directory: " + err.Error()
	}
	if len(entries) > 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		return false, "not empty; still holds " + strings.Join(names, ", ")
	}
	if err := os.Remove(ownerPath); err != nil {
		return false, "could not remove empty legacy owner directory: " + err.Error()
	}
	return true, ""
}

func migrateUndo(ctx context.Context, root string, options MigrateOptions) (MigrateReport, error) {
	if err := validateMigrationID(options.UndoID); err != nil {
		return MigrateReport{}, err
	}
	report := MigrateReport{
		SchemaVersion: 1, ProjectsRoot: root, ObservedAt: options.Now(),
		DryRun: !options.Apply, Undo: true, ManifestID: options.UndoID,
	}
	if !worktrees.BusyProcessCheckSupported {
		report.Notes = append(report.Notes, busyProcessUnsupportedNote())
	}
	var lock *migrationLock
	if options.Apply {
		acquired, lockErr := acquireMigrationLock(root)
		if lockErr != nil {
			return MigrateReport{}, lockErr
		}
		lock = acquired
		defer lock.release()
	}
	manifestPath := filepath.Join(root, ".wb", migrationsDirName, options.UndoID, "manifest.json")
	report.ManifestPath = manifestPath
	manifest, err := readManifest(manifestPath)
	if err != nil {
		return MigrateReport{}, fmt.Errorf("read migration manifest %s: %w", options.UndoID, err)
	}
	var reversedDestinations []string
	for index := range manifest.Clones {
		entry := manifest.Clones[index]
		clone := MigrateClone{Repository: entry.Repository, Source: entry.Destination, Destination: entry.Source}
		for _, worktree := range entry.Worktrees {
			clone.Worktrees = append(clone.Worktrees, MigrateWorktree{Source: worktree.Destination, Destination: worktree.Source})
		}
		relocationsOK := true
		if !options.ClonesOnly {
			for relIndex := range entry.Relocations {
				reloc := entry.Relocations[relIndex]
				if reloc.Status != "done" || reloc.Reversed {
					continue
				}
				// A relocation reverses back to its pre-relocation source: the
				// manifest's Source/Destination naming stays forward-oriented,
				// so the reversal's own source/destination are swapped from it.
				migReloc := MigrateRelocation{Task: reloc.Task, Source: reloc.Destination, Destination: reloc.Source, Status: "planned"}
				if !options.Apply {
					clone.Relocations = append(clone.Relocations, migReloc)
					continue
				}
				if reason := refuseClone(ctx, root, entry.Repository, reloc.Destination, nil); reason != "" {
					migReloc.Status, migReloc.Reason = "skipped", "refused immediately before relocation reversal: "+reason
					clone.Relocations = append(clone.Relocations, migReloc)
					relocationsOK = false
					continue
				}
				if err := worktrees.ReverseRelocation(ctx, root, entry.Destination, reloc.Destination, reloc.Source, options.Now()); err != nil {
					migReloc.Status, migReloc.Reason = "failed", err.Error()
					clone.Relocations = append(clone.Relocations, migReloc)
					relocationsOK = false
					continue
				}
				regenerateMarkers(root, reloc.Source)
				manifest.Clones[index].Relocations[relIndex].Reversed = true
				migReloc.Status = "done"
				clone.Relocations = append(clone.Relocations, migReloc)
			}
		}
		if !relocationsOK {
			clone.Status = "skipped"
			clone.Reason = "one or more relocations could not be reversed; clone move left in place"
			report.Clones = append(report.Clones, clone)
			if writeErr := writeManifest(manifestPath, manifest); writeErr != nil {
				report.Clones = append(report.Clones, manifestUnprocessedClones(manifest, index+1)...)
				return report, fmt.Errorf("record migration manifest outcome for %s: %w", entry.Repository, writeErr)
			}
			continue
		}
		if options.Apply && len(entry.Relocations) > 0 {
			// Persist reversed relocations now: an already-host-level clone
			// (entry.Status != "done") never reaches one of the writeManifest
			// calls further down, and its relocation reversals still need to
			// survive an interruption exactly like a clone move's does.
			if writeErr := writeManifest(manifestPath, manifest); writeErr != nil {
				report.Clones = append(report.Clones, manifestUnprocessedClones(manifest, index+1)...)
				return report, fmt.Errorf("record migration manifest outcome for %s: %w", entry.Repository, writeErr)
			}
		}
		if entry.Status != "done" || entry.Reversed {
			clone.Status = "skipped"
			clone.Reason = "manifest does not record this clone as a completed, unreversed move"
			report.Clones = append(report.Clones, clone)
			continue
		}
		if !options.Apply {
			clone.Status = "planned"
			report.Clones = append(report.Clones, clone)
			continue
		}
		// Re-run every refusal check immediately before moving back, against
		// the clone's CURRENT (host-level) location, not only at plan time —
		// the same hazard applyOneClone guards against for the forward move.
		var currentWorktreePaths []string
		for _, worktree := range entry.Worktrees {
			currentWorktreePaths = append(currentWorktreePaths, worktree.Destination)
		}
		if reason := refuseClone(ctx, root, entry.Repository, entry.Destination, currentWorktreePaths); reason != "" {
			clone.Status = "skipped"
			clone.Reason = "refused immediately before move back: " + reason
			report.Clones = append(report.Clones, clone)
			if writeErr := writeManifest(manifestPath, manifest); writeErr != nil {
				report.Clones = append(report.Clones, manifestUnprocessedClones(manifest, index+1)...)
				return report, fmt.Errorf("record migration manifest outcome for %s: %w", entry.Repository, writeErr)
			}
			continue
		}
		if _, err := worktrees.ApplyCloneMove(ctx, entry.Destination, entry.Source); err != nil {
			clone.Status = "failed"
			clone.Reason = err.Error()
			report.Clones = append(report.Clones, clone)
			if writeErr := writeManifest(manifestPath, manifest); writeErr != nil {
				report.Clones = append(report.Clones, manifestUnprocessedClones(manifest, index+1)...)
				return report, fmt.Errorf("record migration manifest outcome for %s: %w", entry.Repository, writeErr)
			}
			continue
		}
		regenerateMarkers(root, entry.Source)
		for _, worktree := range entry.Worktrees {
			regenerateMarkers(root, worktree.Source)
		}
		// The forward move created <root>/<host>/<org> (and possibly
		// <root>/<host> itself, its first clone). Reversing the last clone
		// under either must remove them, mirroring the legacy owner-directory
		// cleanup the forward move performs, or they are left behind forever.
		// This is only a best-effort immediate attempt; migrateUndo's own
		// final pass (finalizeKeptOwners), not this attempt, is what the
		// report's KeptOwners is built from once every entry is processed.
		_, _ = removeEmptyLegacyOwner(filepath.Dir(entry.Destination))
		_, _ = removeEmptyLegacyOwner(filepath.Dir(filepath.Dir(entry.Destination)))
		reversedDestinations = append(reversedDestinations, entry.Destination)
		clone.Status = "reversed"
		report.Clones = append(report.Clones, clone)
		now := options.Now()
		manifest.Clones[index].Reversed = true
		manifest.Clones[index].ReversedAt = &now
		// Persist this clone's outcome as it completes, atomically, exactly
		// as the forward apply does: an interrupted undo run must leave a
		// manifest that already reflects every clone it finished, not only
		// what a final write at the very end would have captured.
		if writeErr := writeManifest(manifestPath, manifest); writeErr != nil {
			report.Clones = append(report.Clones, manifestUnprocessedClones(manifest, index+1)...)
			return report, fmt.Errorf("record migration manifest outcome for %s: %w", entry.Repository, writeErr)
		}
	}
	if options.Apply {
		invalidateLocalIndex(root)
		var orgCandidates, hostCandidates []string
		for _, destination := range reversedDestinations {
			orgDir := filepath.Dir(destination)
			orgCandidates = append(orgCandidates, orgDir)
			hostCandidates = append(hostCandidates, filepath.Dir(orgDir))
		}
		report.KeptOwners = finalizeKeptOwners(append(orgCandidates, hostCandidates...))
	}
	report.DaemonRestartRequired = daemonRunning(root)
	return report, nil
}

// manifestUnprocessedClones reports every manifest entry from index onward as
// not yet processed, for the report a manifest-write failure returns midway
// through undo.
func manifestUnprocessedClones(manifest *migrationManifest, index int) []MigrateClone {
	var clones []MigrateClone
	for _, entry := range manifest.Clones[index:] {
		clone := MigrateClone{Repository: entry.Repository, Source: entry.Destination, Destination: entry.Source, Status: "skipped", Reason: "not reached before the manifest write failure"}
		clones = append(clones, clone)
	}
	return clones
}

// validateMigrationID rejects anything that is not exactly one safe path
// segment: undo interpolates this value directly into a filesystem path
// under <root>/.wb/layout-migrations/, so an id containing a path separator
// or a "." / ".." segment must never reach that join.
func validateMigrationID(id string) error {
	if id == "" {
		return fmt.Errorf("migration id is required")
	}
	if id == "." || id == ".." {
		return fmt.Errorf("invalid migration id %q", id)
	}
	if strings.ContainsAny(id, "/\\") {
		return fmt.Errorf("invalid migration id %q: must be a single path segment", id)
	}
	if clean := filepath.Clean(id); clean != id {
		return fmt.Errorf("invalid migration id %q", id)
	}
	return nil
}

type repoFilter map[string]bool

func (filter repoFilter) matches(slug string) bool {
	if len(filter) == 0 {
		return true
	}
	return filter[strings.ToLower(slug)]
}

func repositorySet(repositories []string) repoFilter {
	filter := repoFilter{}
	for _, repository := range repositories {
		filter[strings.ToLower(strings.TrimSpace(repository))] = true
	}
	return filter
}

func createMigrationManifest(root string, clones []MigrateClone, now time.Time) (*migrationManifest, string, error) {
	home, err := wbhome.EnsureRoot(root)
	if err != nil {
		return nil, "", err
	}
	id, err := newMigrationID(now)
	if err != nil {
		return nil, "", err
	}
	directory := filepath.Join(home, migrationsDirName, id)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, "", err
	}
	manifest := &migrationManifest{SchemaVersion: 1, ID: id, ProjectsRoot: root, CreatedAt: now}
	for _, clone := range clones {
		entry := manifestClone{
			Repository: clone.Repository, Source: clone.Source, Destination: clone.Destination,
			Head: clone.Head, Status: "planned",
		}
		entry.Worktrees = append(entry.Worktrees, clone.Worktrees...)
		manifest.Clones = append(manifest.Clones, entry)
	}
	path := filepath.Join(directory, "manifest.json")
	if err := writeManifest(path, manifest); err != nil {
		return nil, "", err
	}
	return manifest, path, nil
}

func updateManifestClone(manifest *migrationManifest, outcome MigrateClone, now time.Time) {
	for index := range manifest.Clones {
		if manifest.Clones[index].Source == outcome.Source && manifest.Clones[index].Destination == outcome.Destination {
			manifest.Clones[index].Status = outcome.Status
			manifest.Clones[index].Reason = outcome.Reason
			manifest.Clones[index].CompletedAt = &now
			return
		}
	}
}

// writeManifest writes manifest to path atomically: a temp file in the same
// directory (so the rename is same-filesystem) followed by a rename, so a
// reader — or a process crash mid-write — never observes a half-written
// manifest.
func writeManifest(path string, manifest *migrationManifest) error {
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	temp := path + ".tmp-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := os.WriteFile(temp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}

func readManifest(path string) (*migrationManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest migrationManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func newMigrationID(now time.Time) (string, error) {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(suffix), nil
}

// invalidateLocalIndex drops WB's cached repository-path index so the next
// discovery walk observes the moved clones instead of the pre-migration
// snapshot.
func invalidateLocalIndex(root string) {
	home, err := wbhome.Root(root)
	if err != nil {
		return
	}
	_ = os.Remove(filepath.Join(home, "cache", "fleet-inventory-v1.json"))
}

// daemonRunning reports whether a wb daemon is currently recorded ready for
// root, which means it must be restarted to see the paths this migration
// changed.
func daemonRunning(root string) bool {
	path, err := daemon.StatePath(root)
	if err != nil {
		return false
	}
	state, ok, err := (daemon.Store{Path: path}).Load()
	if err != nil || !ok {
		return false
	}
	return state.Status == daemon.StatusReady
}
