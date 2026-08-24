package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// Adoption brings an external (pre-WB) linked worktree under WB management
// so the existing cleanup/abort machinery — dirty/unlanded/open-PR/locked/
// awaiting-push refusal, and the destructive removal machinery behind them —
// applies to it exactly as it does to a worktree `wb worktree create` made
// directly, without a second parallel removal path.
//
// `wb worktree orphans` already discovers and classifies these worktrees.
// `wb worktree backfill` already gives one a manifest, but the identity
// half of adoption alone: no task directory or Work Log claim exists for
// cleanup/abort's own preflight to resolve, so both refuse it with "task …
// was not found". Adopt writes that task directory, manifest, and claim,
// reusing Backfill's ReconstructManifest for the manifest rather than
// duplicating its inference — and it never fabricates a prompt, exactly as
// Backfill does not.
//
// It is additive, like Backfill: the working tree, its index, and its
// checked-out branch are never moved, modified, or otherwise touched. Only a
// small registration entry — a <task>/<owner>/<repository> directory under
// the WB home holding one pointer file that names the real, unmoved worktree
// path — is created, alongside the ordinary Work Log claim and
// `.wb-worklog` projection every WB task carries, written into the
// worktree's own `.wb/local` and `.wb-worklog` directories exactly as
// ReconstructManifest and Create already do. See readAdoptedWorktreePointer,
// listLayout, and locateAdoptedWorktree for how List/Cleanup/Abort resolve
// that registration back to the real worktree, and
// openAdoptedCleanupWorktree/removeAdoptedRegistration for how a later
// cleanup or abort removes both once it decides to.

// adoptedWorktreePointerName is the one file an adoption registration
// directory holds: the absolute, real, never-relocated path of the external
// worktree it names.
const adoptedWorktreePointerName = ".wb-adopted-worktree.json"

// adoptedWorktreePointer is untrusted, exactly like the Work Log projection
// it sits beside: it names where to look, and every consumer independently
// verifies what it finds there (Git metadata, then the immutable Work Log
// claim) before trusting it for anything destructive.
type adoptedWorktreePointer struct {
	Version   int       `json:"version"`
	Worktree  string    `json:"worktree"`
	AdoptedAt time.Time `json:"adopted_at"`
}

// readAdoptedWorktreePointer reads and minimally validates an adoption
// registration at path, returning the real worktree path it names. This is
// read-only reconnaissance during the inventory walk — matching
// hasGitMetadata/isGitRoot's own plain os.Stat/os.ReadFile style — not the
// destructive boundary; the no-follow, descriptor-anchored discipline lives
// at openAdoptedCleanupWorktree and createAdoptionRegistration instead.
func readAdoptedWorktreePointer(path string) (string, bool) {
	content, err := os.ReadFile(filepath.Join(path, adoptedWorktreePointerName))
	if err != nil {
		return "", false
	}
	var pointer adoptedWorktreePointer
	if err := json.Unmarshal(content, &pointer); err != nil {
		return "", false
	}
	if pointer.Version != 1 || !filepath.IsAbs(pointer.Worktree) {
		return "", false
	}
	return filepath.Clean(pointer.Worktree), true
}

// createAdoptionRegistration writes, or idempotently confirms, the WB-home
// registration entry for an adopted worktree, without creating, moving, or
// otherwise touching the worktree itself. operationDirectory is the held,
// locked task directory (see prepareOperationRoot/acquireLockAt).
func createAdoptionRegistration(operationDirectory *os.File, operationRoot, owner, repository, worktree string, now time.Time) (string, error) {
	ownerFD, err := openOrCreateNoFollowDirectory(int(operationDirectory.Fd()), owner)
	if err != nil {
		return "", err
	}
	ownerDirectory := os.NewFile(uintptr(ownerFD), "wb-adopt-owner")
	defer func() { _ = ownerDirectory.Close() }()
	ownerPath := filepath.Join(operationRoot, owner)
	if !directoryStillMatches(ownerPath, ownerDirectory) {
		return "", fmt.Errorf("adopted worktree registration owner path changed before writing; refusing redirected registration")
	}
	repositoryFD, err := openOrCreateNoFollowDirectory(ownerFD, repository)
	if err != nil {
		return "", err
	}
	repositoryDirectory := os.NewFile(uintptr(repositoryFD), "wb-adopt-registration")
	defer func() { _ = repositoryDirectory.Close() }()
	registrationPath := filepath.Join(ownerPath, repository)
	if !directoryStillMatches(registrationPath, repositoryDirectory) {
		return "", fmt.Errorf("adopted worktree registration path changed before writing; refusing redirected registration")
	}
	// A registration directory must never hold a real Git worktree: that
	// would mean this exact task/owner/repository name collides with a
	// worktree WB manages some other way.
	if hasGitMetadata(registrationPath) {
		return "", fmt.Errorf("registration path %s already holds a Git worktree; choose a different task or resolve the conflict manually", registrationPath)
	}
	if existing, readErr := readBytesAt(repositoryDirectory, adoptedWorktreePointerName); readErr == nil {
		var current adoptedWorktreePointer
		if json.Unmarshal(existing, &current) == nil && filepath.Clean(current.Worktree) == filepath.Clean(worktree) {
			return registrationPath, nil // Already registered for this exact worktree.
		}
		return "", fmt.Errorf("registration path %s is already adopted for a different worktree", registrationPath)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return "", fmt.Errorf("inspect existing adoption registration at %s: %w", registrationPath, readErr)
	}
	pointer := adoptedWorktreePointer{Version: 1, Worktree: worktree, AdoptedAt: now}
	if err := writeJSONAtomicAt(repositoryDirectory, adoptedWorktreePointerName, pointer, 0o600); err != nil {
		return "", fmt.Errorf("write adoption registration at %s: %w", registrationPath, err)
	}
	return registrationPath, nil
}

// AdoptOptions selects and, with Apply, mutates one adoption sweep. It is
// dry-run by default, like every other mutating WB verb.
type AdoptOptions struct {
	ProjectsRoot string
	Base         string
	// Path adopts exactly this one worktree. Mutually exclusive with
	// AllExternal.
	Path string
	// AllExternal sweeps every worktree Orphans classifies as external.
	AllExternal bool
	// Filter narrows the sweep to candidates whose repository slug or path
	// contains this substring — the same contract as ListOptions.Filter and
	// the root --filter flag.
	Filter string
	Apply  bool
	// Now is injectable so the recorded adoption time is deterministic under
	// test.
	Now func() time.Time
}

// Adopt dispositions.
const (
	AdoptWouldAdopt     = "would_adopt"
	AdoptAdopted        = "adopted"
	AdoptAlreadyAdopted = "already_adopted"
	AdoptSkipped        = "skipped"
)

// AdoptResult is what happened, or would happen, to one external worktree.
type AdoptResult struct {
	Path       string `json:"path"`
	Repository string `json:"repository,omitempty"`
	Task       string `json:"task,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Base       string `json:"base,omitempty"`
	Action     string `json:"action"`
	Reason     string `json:"reason,omitempty"`
}

// Adopt is documented on the package-level comment above.
func Adopt(ctx context.Context, options AdoptOptions) ([]AdoptResult, error) {
	projectsRoot, err := absoluteProjectsRoot(options.ProjectsRoot)
	if err != nil {
		return nil, err
	}
	base := strings.TrimSpace(options.Base)
	if base == "" {
		base = "main"
	}
	path := strings.TrimSpace(options.Path)
	if (path == "") == !options.AllExternal {
		return nil, fmt.Errorf("adopt requires exactly one of a worktree path or --all-external")
	}
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	report, err := Orphans(ctx, OrphanOptions{ProjectsRoot: projectsRoot, Base: base})
	if err != nil {
		return nil, err
	}

	var candidates []OrphanWorktree
	if options.AllExternal {
		for _, family := range report.Families {
			for _, worktree := range family.Worktrees {
				if worktree.Layout != LayoutExternal {
					continue
				}
				if !filterMatches(options.Filter, worktree.Path, worktree.Repository) {
					continue
				}
				candidates = append(candidates, worktree)
			}
		}
	} else {
		absolute, absErr := filepath.Abs(path)
		if absErr != nil {
			return nil, fmt.Errorf("resolve %s: %w", path, absErr)
		}
		absolute = filepath.Clean(absolute)
		if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
			absolute = resolved
		}
		found := false
		for _, family := range report.Families {
			for _, worktree := range family.Worktrees {
				if filepath.Clean(worktree.Path) == absolute {
					candidates = append(candidates, worktree)
					found = true
				}
			}
		}
		if !found {
			return nil, fmt.Errorf("%s is not a linked Git worktree of a canonical clone under %s", path, projectsRoot)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Path < candidates[j].Path })

	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return nil, err
	}
	results := make([]AdoptResult, 0, len(candidates))
	for _, candidate := range candidates {
		results = append(results, adoptOne(ctx, projectsRoot, home, candidate, options.Apply, now()))
	}
	return results, nil
}

func adoptOne(ctx context.Context, projectsRoot, home string, candidate OrphanWorktree, apply bool, now time.Time) AdoptResult {
	result := AdoptResult{Path: candidate.Path}
	if candidate.Layout != LayoutExternal {
		result.Action = AdoptSkipped
		result.Reason = fmt.Sprintf("layout is %q, not external; already under WB management", candidate.Layout)
		return result
	}
	if candidate.Missing {
		result.Action = AdoptSkipped
		result.Reason = "working tree is gone; git worktree prune would clear the registration"
		return result
	}
	// An already-claimed worktree — adopted before, or genuinely created by
	// `wb worktree create` and merely discovered here — is a no-op. Re-running
	// a sweep over hundreds of worktrees after an interruption must be safe.
	if _, _, _, err := activeWorkLogClaim(home, candidate.Path); err == nil {
		result.Action = AdoptAlreadyAdopted
		return result
	} else if !errors.Is(err, errWorkLogProjectionNotFound) {
		result.Action = AdoptSkipped
		result.Reason = fmt.Sprintf("existing Work Log state is inconsistent, refusing to adopt: %v", err)
		return result
	}
	owner, repository, err := managedWorktreeCanonicalCoordinates(ctx, projectsRoot, candidate.Path)
	if err != nil {
		result.Action = AdoptSkipped
		result.Reason = err.Error()
		return result
	}
	// A dry run must never write the reconstructed manifest — that would be a
	// mutation a plan is supposed to be free of — so it previews exactly what
	// ReconstructManifest would produce without persisting it. Only the apply
	// path calls ReconstructManifest itself, which is where the write belongs.
	reconstruct := PreviewReconstructedManifest
	if apply {
		reconstruct = ReconstructManifest
	}
	manifest, err := reconstruct(ctx, candidate.Path)
	if err != nil {
		result.Action = AdoptSkipped
		result.Reason = err.Error()
		return result
	}
	if manifest.BaseSHA == "" {
		result.Action = AdoptSkipped
		result.Reason = "cannot determine an ancestor base commit for this branch; fetch an ancestor of origin/main or origin/master and retry"
		return result
	}
	result.Repository = owner + "/" + repository
	result.Task = manifest.EffortID
	result.Branch = manifest.Branch
	result.Base = manifest.Base
	if !apply {
		result.Action = AdoptWouldAdopt
		return result
	}
	if err := adoptApply(home, owner, repository, manifest, candidate.Path, now); err != nil {
		result.Action = AdoptSkipped
		result.Reason = err.Error()
		return result
	}
	result.Action = AdoptAdopted
	return result
}

func adoptApply(home, owner, repository string, manifest Manifest, worktree string, now time.Time) error {
	operation, err := prepareOperationRoot(home, manifest.EffortID, nil)
	if err != nil {
		return err
	}
	defer operation.close()
	lock, err := acquireLockAt(operation.Directory, manifest.EffortID)
	if err != nil {
		return err
	}
	defer func() { _ = lock.release() }()
	// Re-check under the lock: another sweep or session may have adopted this
	// exact worktree in the window between the pre-lock check and here.
	if _, _, _, err := activeWorkLogClaim(home, worktree); err == nil {
		return nil
	} else if !errors.Is(err, errWorkLogProjectionNotFound) {
		return fmt.Errorf("existing Work Log state is inconsistent, refusing to adopt: %w", err)
	}
	if _, err := createAdoptionRegistration(operation.Directory, operation.Path, owner, repository, worktree, now); err != nil {
		return err
	}
	result := CreateResult{
		Repository: owner + "/" + repository, WorktreeDir: worktree, Branch: manifest.Branch,
		Base: manifest.Base, BaseSHA: manifest.BaseSHA,
	}
	workLogOptions := WorkLogOptions{EffortID: manifest.EffortID, Model: "unknown", AcquiredVia: "adopted"}
	if _, err := recordWorkLogWithHooks(home, manifest.EffortID, result, workLogOptions, workLogPublicationHooks{}); err != nil {
		return fmt.Errorf("record adoption Work Log claim: %w", err)
	}
	return nil
}
