package layout

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	// Status is one of: planned, done, already_done, skipped, failed, reversed.
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
}

// MigrateFailed reports whether a migrate run left any clone refused or
// failed, which is the condition the command exits with the findings code
// for.
func MigrateFailed(report MigrateReport) bool {
	for _, clone := range report.Clones {
		if clone.Status == "skipped" || clone.Status == "failed" {
			return true
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
	Repository  string            `json:"repository"`
	Source      string            `json:"source"`
	Destination string            `json:"destination"`
	Head        string            `json:"head"`
	Worktrees   []MigrateWorktree `json:"worktrees,omitempty"`
	Status      string            `json:"status"`
	Reason      string            `json:"reason,omitempty"`
	CompletedAt *time.Time        `json:"completed_at,omitempty"`
	Reversed    bool              `json:"reversed,omitempty"`
	ReversedAt  *time.Time        `json:"reversed_at,omitempty"`
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
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
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
			report.Clones = append(report.Clones, MigrateClone{
				Repository: slug, Source: path, Destination: path,
				Status: "already_done", Reason: "already at the host-level path",
			})
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
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
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
			applyOneClone(ctx, root, &planned[index], options.Now())
			if manifest != nil {
				updateManifestClone(manifest, planned[index])
				_ = writeManifest(manifestPath, manifest)
			}
		}
		invalidateLocalIndex(root)
	}

	report.Clones = append(report.Clones, planned...)
	sort.Slice(report.Clones, func(i, j int) bool { return report.Clones[i].Repository < report.Clones[j].Repository })
	report.DaemonRestartRequired = daemonRunning(root)
	return report, nil
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
	destination := filepath.Join(root, address.Host, address.Org, address.Repo)
	clone.Destination = destination
	if _, statErr := os.Lstat(destination); statErr == nil {
		clone.Status = "skipped"
		clone.Reason = "destination already exists: " + destination
		return clone
	}
	if reason, err := worktrees.GitOperationInProgress(ctx, path); err != nil {
		clone.Status = "skipped"
		clone.Reason = "cannot inspect Git state: " + err.Error()
		return clone
	} else if reason != "" {
		clone.Status = "skipped"
		clone.Reason = reason
		return clone
	}
	plan, err := worktrees.PlanCloneMove(ctx, path, destination)
	if err != nil {
		clone.Status = "skipped"
		clone.Reason = "cannot enumerate linked worktrees: " + err.Error()
		return clone
	}
	for _, worktree := range plan.Worktrees {
		if worktree.Source == path {
			// The clone's own entry in its worktree registry; already
			// accounted for by Source/Destination above.
			continue
		}
		clone.Worktrees = append(clone.Worktrees, MigrateWorktree{Source: worktree.Source, Destination: worktree.Destination})
		if reason, err := worktrees.GitOperationInProgress(ctx, worktree.Source); err == nil && reason != "" {
			clone.Status = "skipped"
			clone.Reason = "linked worktree " + worktree.Source + ": " + reason
			return clone
		}
	}
	if claim := liveClaimReason(root, slug); claim != "" {
		clone.Status = "skipped"
		clone.Reason = claim
		return clone
	}
	clone.Status = "planned"
	return clone
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
	_, err := worktrees.ApplyCloneMove(ctx, clone.Source, clone.Destination)
	if err != nil {
		clone.Status = "failed"
		clone.Reason = err.Error()
		return
	}
	regenerateMarkers(root, clone.Destination)
	for _, worktree := range clone.Worktrees {
		regenerateMarkers(root, worktree.Destination)
	}
	removeEmptyLegacyOwner(filepath.Dir(clone.Source))
	clone.Status = "done"
	clone.Reason = ""
	_ = now
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
func removeEmptyLegacyOwner(ownerPath string) {
	entries, err := os.ReadDir(ownerPath)
	if err != nil || len(entries) > 0 {
		return
	}
	_ = os.Remove(ownerPath)
}

func migrateUndo(ctx context.Context, root string, options MigrateOptions) (MigrateReport, error) {
	report := MigrateReport{
		SchemaVersion: 1, ProjectsRoot: root, ObservedAt: options.Now(),
		DryRun: !options.Apply, Undo: true, ManifestID: options.UndoID,
	}
	manifestPath := filepath.Join(root, ".wb", migrationsDirName, options.UndoID, "manifest.json")
	report.ManifestPath = manifestPath
	manifest, err := readManifest(manifestPath)
	if err != nil {
		return MigrateReport{}, fmt.Errorf("read migration manifest %s: %w", options.UndoID, err)
	}
	for index := range manifest.Clones {
		entry := manifest.Clones[index]
		clone := MigrateClone{Repository: entry.Repository, Source: entry.Destination, Destination: entry.Source}
		for _, worktree := range entry.Worktrees {
			clone.Worktrees = append(clone.Worktrees, MigrateWorktree{Source: worktree.Destination, Destination: worktree.Source})
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
		if _, err := worktrees.ApplyCloneMove(ctx, entry.Destination, entry.Source); err != nil {
			clone.Status = "failed"
			clone.Reason = err.Error()
			report.Clones = append(report.Clones, clone)
			continue
		}
		regenerateMarkers(root, entry.Source)
		for _, worktree := range entry.Worktrees {
			regenerateMarkers(root, worktree.Source)
		}
		removeEmptyLegacyOwner(filepath.Dir(entry.Destination))
		clone.Status = "reversed"
		report.Clones = append(report.Clones, clone)
		now := options.Now()
		manifest.Clones[index].Reversed = true
		manifest.Clones[index].ReversedAt = &now
	}
	if options.Apply {
		if err := writeManifest(manifestPath, manifest); err != nil {
			return report, err
		}
		invalidateLocalIndex(root)
	}
	report.DaemonRestartRequired = daemonRunning(root)
	return report, nil
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
			Status: "planned",
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

func updateManifestClone(manifest *migrationManifest, outcome MigrateClone) {
	for index := range manifest.Clones {
		if manifest.Clones[index].Source == outcome.Source && manifest.Clones[index].Destination == outcome.Destination {
			manifest.Clones[index].Status = outcome.Status
			manifest.Clones[index].Reason = outcome.Reason
			return
		}
	}
}

func writeManifest(path string, manifest *migrationManifest) error {
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
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
