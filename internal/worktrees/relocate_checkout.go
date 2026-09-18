package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// resolvedClaimHomes lists every home a checkout's Work Log claim might be
// recorded under -- the current write home first, then every other home
// wbhome.Resolve reports (including a retired legacy one), deduplicated. A
// claim recorded under a retired legacy home is still live (or still
// terminal) work; matching REQ: clone-migration-refusals, which requires the
// same breadth for the live-claim refusal (see ListActiveClaimSummaries).
func resolvedClaimHomes(resolution wbhome.Resolution) []string {
	homes := make([]string, 0, len(resolution.Read)+1)
	tried := map[string]bool{}
	addHome := func(home string) {
		if home == "" || tried[home] {
			return
		}
		tried[home] = true
		homes = append(homes, home)
	}
	addHome(resolution.Write.Home)
	for _, layout := range resolution.Read {
		addHome(layout.Home)
	}
	return homes
}

// claimForRelocation resolves either an active or a terminal (sealed,
// finished-task) Work Log claim for a checkout. The founder's decision
// (2026-09-18): a finished task has no live session and will not be resumed,
// so relocating its leftover checkout is the safest case, not an unsafe one
// -- activeWorkLogClaim's Lifecycle=="active" requirement is a limitation of
// what it corroborates, not a safety reason to refuse a terminal claim.
// terminal is non-nil exactly when the resolved claim is terminal. This is
// used only by `wb layout migrate`'s own checkout relocation (RelocateCheckout)
// and by RecordCloneMoveRelocationIntents/ReverseRelocation -- never by
// `wb worktree relocate` (Relocate), whose own claim resolution
// (activeWorkLogClaimAcrossHomes) is unchanged and still requires an active
// claim: an operator naming a task explicitly is a different case from
// migrate's own automatic sweep.
func claimForRelocation(home, worktree string) (workLogClaim, *workLogTerminalRecord, error) {
	claim, _, _, activeErr := activeWorkLogClaim(home, worktree)
	if activeErr == nil {
		return claim, nil, nil
	}
	terminal, terminalErr := readWorkLogTerminalRecord(home, worktree)
	if terminalErr != nil {
		return workLogClaim{}, nil, terminalErr
	}
	if terminal != nil {
		return terminal.workLogClaim, terminal, nil
	}
	// Neither active nor terminal corroborated (no projection, corrupted
	// evidence, ...): the active-claim error is the actionable one to report.
	return workLogClaim{}, nil, activeErr
}

// claimForRelocationAcrossHomes is claimForRelocation, tried across every
// home wbhome.Resolve reports, not only the current write home -- matching
// REQ: clone-migration-refusals, which requires the same breadth for the
// live-claim refusal (see ListActiveClaimSummaries). It returns the home
// under which the claim was found so callers write any new relocation
// record to that same home.
func claimForRelocationAcrossHomes(resolution wbhome.Resolution, worktree string) (workLogClaim, *workLogTerminalRecord, string, error) {
	var lastErr error
	for _, home := range resolvedClaimHomes(resolution) {
		claim, terminal, err := claimForRelocation(home, worktree)
		if err == nil {
			return claim, terminal, home, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no resolved home could corroborate a Work Log claim")
	}
	return workLogClaim{}, nil, "", lastErr
}

// checkoutSafetyRefusal runs the per-checkout safety checks REQ:
// migration-relocates-managed-worktrees names, immediately before a checkout
// actually moves as well as at plan time: a Git operation in progress, or a
// live process (on an OS that exposes one) with its working directory inside
// the checkout, or an un-picked-up parked session naming it. A checkout's
// task lock and its destination already existing are checked by
// RelocateCheckout itself, not here.
func checkoutSafetyRefusal(ctx context.Context, projectsRoot, worktree string) (string, error) {
	if reason, err := GitOperationInProgress(ctx, worktree); err != nil {
		return "", fmt.Errorf("cannot inspect Git state: %w", err)
	} else if reason != "" {
		return reason, nil
	}
	if BusyProcessCheckSupported {
		if reason := BusyProcessReason([]string{worktree}); reason != "" {
			return reason, nil
		}
	}
	if reason := ParkedSessionReason(projectsRoot, []string{worktree}); reason != "" {
		return reason, nil
	}
	return "", nil
}

// IdentifyManagedCheckout resolves the exact-path checkout's WB task identity
// and whether its Work Log claim is still active, searching every home
// wbhome.Resolve reports for projectsRoot -- active or terminal (finished
// task) alike. ok is false when the path carries no WB task identity at all
// (an adopted/foreign linked worktree, which `wb layout migrate` reports
// unmanaged and repoints only, never relocates).
//
// This never uses List()'s own worktree discovery: `wb layout migrate`
// matches each candidate checkout by its exact path from the clone's own
// `git worktree list`, not by scanning every managed worktree the machine
// currently recognises, which is the wrong unit for a per-checkout decision
// and previously let a finished task's checkout under a de-configured store
// root go undiscovered.
func IdentifyManagedCheckout(projectsRoot, worktree string) (task string, active bool, ok bool, err error) {
	resolution, resolveErr := wbhome.Resolve(projectsRoot)
	if resolveErr != nil {
		return "", false, false, resolveErr
	}
	claim, terminal, _, claimErr := claimForRelocationAcrossHomes(resolution, worktree)
	if claimErr != nil {
		// Not every linked worktree carries a WB task identity; a checkout
		// with none is reported unmanaged by the caller, not as an error.
		return "", false, false, nil
	}
	return claim.Task, terminal == nil, true, nil
}

// RelocateCheckoutOptions moves exactly one checkout -- identified by its
// exact current path, never by task or by a repository filter -- to an
// exact destination path. This is the primitive `wb layout migrate` uses per
// checkout it decided to relocate: the same no-replace move, Git worktree
// repair, registration verification, and relocation-receipt journal (intent
// before the move, completion after) that `wb worktree relocate` (Relocate)
// applies, generalized so it needs no ListResult/task lookup and accepts a
// checkout whose Work Log claim has gone terminal (the safe, finished-task
// case) exactly as it accepts an active one. It is never called by Relocate,
// and Relocate is never called by `wb layout migrate`.
type RelocateCheckoutOptions struct {
	ProjectsRoot string
	CanonicalDir string
	Source       string
	Destination  string
	To           string // local or shared; drives destination-root bookkeeping only
	Apply        bool
	Now          func() time.Time
}

// RelocateCheckoutResult is one checkout's relocation plan or outcome.
type RelocateCheckoutResult struct {
	Eligible    bool
	Reason      string
	Applied     bool
	Repaired    bool
	ReceiptPath string
}

// RelocateCheckout plans, and when Apply is set applies, moving exactly one
// checkout to Destination. See RelocateCheckoutOptions.
func RelocateCheckout(ctx context.Context, options RelocateCheckoutOptions) (RelocateCheckoutResult, error) {
	if options.Now == nil {
		options.Now = time.Now
	}
	result := RelocateCheckoutResult{}
	resolution, err := wbhome.Resolve(options.ProjectsRoot)
	if err != nil {
		return result, err
	}
	claim, _, home, claimErr := claimForRelocationAcrossHomes(resolution, options.Source)
	if claimErr != nil {
		result.Reason = "Work Log claim is not corroborated: " + claimErr.Error()
		return result, nil
	}
	if reason, err := checkoutSafetyRefusal(ctx, options.ProjectsRoot, options.Source); err != nil {
		return result, err
	} else if reason != "" {
		result.Reason = reason
		return result, nil
	}
	if _, statErr := os.Lstat(options.Destination); !errors.Is(statErr, os.ErrNotExist) {
		if statErr == nil {
			result.Reason = "destination already exists: " + options.Destination
			return result, nil
		}
		return result, fmt.Errorf("inspect relocation destination %s: %w", options.Destination, statErr)
	}
	operation, err := prepareOperationRoot(home, claim.Task, nil)
	if err != nil {
		return result, fmt.Errorf("prepare task lock for %q: %w", claim.Task, err)
	}
	defer operation.close()
	lock, lockErr := acquireLockAt(operation.Directory, claim.Task)
	if lockErr != nil {
		result.Reason = "task lock held: " + lockErr.Error()
		return result, nil
	}
	result.Eligible = true
	if !options.Apply {
		_ = lock.release()
		return result, nil
	}
	defer func() { _ = lock.release() }()
	// Every refusal condition is re-checked immediately before the actual
	// move, not only at plan time -- the interval between planning and
	// moving is exactly when a Git operation or a busy process can start,
	// matching refuseClone's own convention.
	if reason, err := checkoutSafetyRefusal(ctx, options.ProjectsRoot, options.Source); err != nil {
		return result, err
	} else if reason != "" {
		return result, fmt.Errorf("refused immediately before move: %s", reason)
	}
	headOutput, err := git(ctx, options.Source, "rev-parse", "HEAD")
	if err != nil {
		return result, fmt.Errorf("read HEAD before relocating %s: %w", options.Source, err)
	}
	head := strings.TrimSpace(headOutput)
	destinationRelative, relativeErr := canonicalRelativeAddress(ctx, options.ProjectsRoot, options.CanonicalDir)
	if relativeErr != nil {
		return result, relativeErr
	}
	destinationRoot := relocationDestinationRoot(options.Destination, destinationRelative, options.To)
	if err := prepareRelocationDestination(ctx, ListResult{CanonicalDir: options.CanonicalDir}, "", options.Destination, destinationRelative, options.To); err != nil {
		return result, err
	}
	// Record the destination relative to the root that produced it, so the
	// receipt still names the checkout after `worktrees.root` is reconfigured.
	placementRelative, placementErr := filepath.Rel(destinationRoot, options.Destination)
	if placementErr != nil || placementRelative == "." || strings.HasPrefix(placementRelative, "..") {
		placementRelative = ""
	}
	intent, _, err := appendRelocationIntent(home, claim, options.Source, options.Destination, options.To, head,
		relocationPlacementRecord{Root: destinationRoot, Relative: placementRelative}, options.Now().UTC())
	if err != nil {
		return result, fmt.Errorf("record relocation intent before moving %s: %w", options.Source, err)
	}
	move, err := moveWorktree(ctx, options.CanonicalDir, destinationRoot, options.Source, options.Destination, worktreeMoveHooks{})
	result.Repaired = move.Repaired
	if err != nil {
		return result, err
	}
	receipt, path, err := appendRelocationReceipt(home, claim, intent, options.Now().UTC())
	if err != nil {
		return result, fmt.Errorf("record relocation receipt after moving %s: %w", options.Source, err)
	}
	if receipt == nil {
		return result, fmt.Errorf("record relocation receipt after moving %s returned no receipt", options.Source)
	}
	result.Applied, result.ReceiptPath = true, path
	return result, nil
}
