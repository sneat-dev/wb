package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// RelocateOptions moves a managed checkout without changing its task, branch,
// or immutable Work Log claim. It deliberately has no new-branch or prompt
// fields: relocation is a physical-layout operation, not recycle.
type RelocateOptions struct {
	ProjectsRoot string
	Task         string
	Filter       string
	To           string // local or shared
	Apply        bool
	Now          func() time.Time
	// afterWorktreeMoveBeforeReceipt is a test-only crash-window seam. A real
	// process interruption has the same durable state: an intent exists and Git
	// has registered the destination, but no completion receipt exists yet.
	afterWorktreeMoveBeforeReceipt func() error
	ports                          *relocationEntryPorts
	planPorts                      *relocationPlanPorts
	applyPorts                     *relocationApplyPorts
	finalizePorts                  *relocationFinalizePorts
}

type RelocateResult struct {
	Task            string `json:"task"`
	Repository      string `json:"repository"`
	CanonicalDir    string `json:"canonical_dir"`
	WorktreeDir     string `json:"worktree_dir"`
	Destination     string `json:"destination"`
	Branch          string `json:"branch"`
	HeadSHA         string `json:"head_sha"`
	To              string `json:"to"`
	ClaimID         string `json:"claim_id,omitempty"`
	Eligible        bool   `json:"eligible"`
	Applied         bool   `json:"applied"`
	AlreadyThere    bool   `json:"already_there,omitempty"`
	RecoveryPending bool   `json:"recovery_pending,omitempty"`
	Finalized       bool   `json:"finalized,omitempty"`
	Repaired        bool   `json:"repaired,omitempty"`
	ReceiptPath     string `json:"receipt_path,omitempty"`
	Reason          string `json:"reason,omitempty"`
	// claimHome is the home under which this checkout's Work Log claim was
	// actually corroborated -- not necessarily resolution.Write.Home. A claim
	// recorded under a retired legacy home is still live, and every claim
	// record it already owns (runs, claims, relocation receipts) lives there;
	// writing a new relocation intent/receipt anywhere else would split that
	// history. Unexported: it is plumbing between planRelocation and
	// applyRelocation/finalizeInterruptedRelocation, never a public result.
	claimHome string `json:"-"`
}

// activeWorkLogClaimAcrossHomes resolves a checkout's active Work Log claim by
// trying every home WB resolves for the root, not only the current write
// home -- matching REQ: clone-migration-refusals, which requires the same
// breadth for the live-claim refusal (see ListActiveClaimSummaries). A claim
// recorded under a retired legacy home is still a live task, and Relocate's
// own eligibility check must recognise it exactly as the refusal check does,
// or a genuinely active, corroborated claim is misreported as uncorroborated
// solely because the machine has since adopted a new write home. It returns
// the home under which the claim was found so callers write any new
// relocation record to that same home.
func activeWorkLogClaimAcrossHomes(resolution wbhome.Resolution, worktree string) (workLogClaim, workLogProjection, string, string, error) {
	var lastErr error
	for _, home := range resolvedClaimHomes(resolution) {
		claim, projection, claimPath, err := activeWorkLogClaim(home, worktree)
		if err == nil {
			return claim, projection, claimPath, home, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no resolved home could corroborate an active Work Log claim")
	}
	return workLogClaim{}, workLogProjection{}, "", "", lastErr
}

type RelocateOutcome struct {
	SchemaVersion int              `json:"schema_version"`
	Results       []RelocateResult `json:"results"`
	Diagnostics   []ListDiagnostic `json:"diagnostics,omitempty"`
}

// workLogRelocationReceipt is immutable, append-only evidence that changes the
// path through which an active claim is corroborated. The claim itself retains
// the original checkout path as historical identity evidence.
type workLogRelocationIntent struct {
	Version     int    `json:"version"`
	Type        string `json:"type"`
	OperationID string `json:"operation_id"`
	ClaimID     string `json:"claim_id"`
	Task        string `json:"task"`
	Repository  string `json:"repository"`
	Branch      string `json:"branch"`
	HeadSHA     string `json:"head_sha"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	// DestinationRoot and DestinationRelative record the destination relative
	// to the placement root that produced it: the central store root for
	// --to=shared, the repository-local <canonical>/.worktrees root for
	// --to=local. The absolute Destination stays the historical fact, while the
	// relative pair keeps the receipt interpretable after the store root is
	// reconfigured — a receipt written before the reconfigure still names where
	// its checkout sits below whatever root is configured later. Both are empty
	// together for a record that is not placement-relative.
	DestinationRoot       string    `json:"destination_root,omitempty"`
	DestinationRelative   string    `json:"destination_relative,omitempty"`
	To                    string    `json:"to"`
	SourceRepository      string    `json:"source_repository,omitempty"`
	DestinationRepository string    `json:"destination_repository,omitempty"`
	RemoteURL             string    `json:"remote_url,omitempty"`
	At                    time.Time `json:"at"`
}

// relocationPlacementRecord is the root-relative half of a relocation
// destination. The zero value records nothing, which is what every relocation
// that is not a managed worktree move supplies.
type relocationPlacementRecord struct {
	Root     string
	Relative string
}

// workLogRelocationReceipt is written only after Git has registered the new
// path. The intent is written first so an interruption in between can be
// corroborated and finalized without ever changing the immutable claim.
type workLogRelocationReceipt = workLogRelocationIntent

const (
	workLogRelocationIntentType = "worktree.relocation-intent"
	workLogRelocationType       = "worktree.relocated"
	// workLogRelocationLegacyCheckout marks an attestation for a checkout
	// already found at a new repository identity before WB could record a
	// repository-transfer receipt. It is deliberately not a transfer claim:
	// the old origin is no longer observable, so WB records only the immutable
	// old claim identity and the verified current origin.
	workLogRelocationLegacyCheckout = "legacy_checkout"
)

func Relocate(ctx context.Context, options RelocateOptions) (RelocateOutcome, error) {
	if !validSafeSegment(options.Task) {
		return RelocateOutcome{}, fmt.Errorf("task is required")
	}
	if options.To != "local" && options.To != "shared" {
		return RelocateOutcome{}, fmt.Errorf("--to must be local or shared")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	ports := productionRelocationEntryPorts()
	if options.ports != nil {
		ports = *options.ports
	}
	resolution, err := ports.resolve(options.ProjectsRoot)
	if err != nil {
		return RelocateOutcome{}, err
	}
	listed, err := ports.list(ctx, ListOptions{ProjectsRoot: options.ProjectsRoot, Task: options.Task, Filter: options.Filter, GitHub: false})
	if err != nil {
		return RelocateOutcome{}, err
	}
	if len(listed.Results) == 0 && len(listed.Diagnostics) == 0 {
		return RelocateOutcome{}, fmt.Errorf("WB worktree task %q was not found", options.Task)
	}
	outcome := RelocateOutcome{SchemaVersion: 1, Diagnostics: listed.Diagnostics}
	for _, entry := range listed.Results {
		result, planErr := ports.plan(ctx, resolution, options, entry)
		if planErr != nil {
			return outcome, planErr
		}
		outcome.Results = append(outcome.Results, result)
	}
	if !options.Apply {
		return outcome, nil
	}

	operation, err := prepareOperationRoot(resolution.Write.Home, options.Task, nil)
	if err != nil {
		return outcome, err
	}
	defer operation.close()
	lock, err := acquireLockAt(operation.Directory, options.Task)
	if err != nil {
		return outcome, fmt.Errorf("lock task %q: %w", options.Task, err)
	}
	defer func() { _ = lock.release() }()

	for index := range outcome.Results {
		result := &outcome.Results[index]
		if !result.Eligible {
			continue
		}
		entry, found := findRelocationEntry(listed.Results, result.WorktreeDir)
		if !found {
			return outcome, fmt.Errorf("relocation plan lost worktree %s", result.WorktreeDir)
		}
		if result.AlreadyThere {
			if !result.RecoveryPending {
				continue
			}
			if err := ports.finalize(result.claimHome, options, entry, result); err != nil {
				return outcome, err
			}
			continue
		}
		if err := ports.apply(ctx, result.claimHome, options, entry, result); err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

type relocationEntryPorts struct {
	resolve  func(string) (wbhome.Resolution, error)
	list     func(context.Context, ListOptions) (ListOutcome, error)
	plan     func(context.Context, wbhome.Resolution, RelocateOptions, ListResult) (RelocateResult, error)
	finalize func(string, RelocateOptions, ListResult, *RelocateResult) error
	apply    func(context.Context, string, RelocateOptions, ListResult, *RelocateResult) error
}

func productionRelocationEntryPorts() relocationEntryPorts {
	return relocationEntryPorts{resolve: wbhome.Resolve, list: ListWithDiagnostics, plan: planRelocation,
		finalize: finalizeInterruptedRelocation, apply: applyRelocation}
}

func findRelocationEntry(entries []ListResult, path string) (ListResult, bool) {
	for _, entry := range entries {
		if filepath.Clean(entry.WorktreeDir) == filepath.Clean(path) {
			return entry, true
		}
	}
	return ListResult{}, false
}

func planRelocation(ctx context.Context, resolution wbhome.Resolution, options RelocateOptions, entry ListResult) (RelocateResult, error) {
	ports := productionRelocationPlanPorts()
	if options.planPorts != nil {
		ports = *options.planPorts
	}
	result := RelocateResult{Task: entry.Task, Repository: entry.Repository, CanonicalDir: entry.CanonicalDir,
		WorktreeDir: entry.WorktreeDir, Branch: entry.Branch, HeadSHA: entry.HeadSHA, To: options.To}
	claim, _, _, claimHome, claimErr := ports.claim(resolution, entry.WorktreeDir)
	if claimErr != nil {
		result.Reason = "active Work Log claim is not corroborated: " + claimErr.Error()
		return result, nil
	}
	result.ClaimID = claim.ClaimID
	result.claimHome = claimHome
	home := claimHome
	placement, err := ports.placement(options.ProjectsRoot, entry.CanonicalDir)
	if err != nil {
		return result, err
	}
	if options.To == "shared" && placement.RepositoryLocal {
		result.Reason = "--to=shared needs a shared checkout store, but the machine-local worktrees configuration selects repository-local store mode"
		return result, nil
	}
	if options.To == "local" {
		placement = WorktreePlacement{Root: filepath.Join(entry.CanonicalDir, ".worktrees"), RepositoryLocal: true}
	}
	destination, err := placement.Path(entry.Task, entry.Repository)
	if err != nil {
		return result, err
	}
	result.Destination = destination
	if eligible, reason := relocationEligibility(entry); !eligible {
		result.Reason = reason
		return result, nil
	}
	if filepath.Clean(destination) == filepath.Clean(entry.WorktreeDir) {
		result.Eligible, result.AlreadyThere = true, true
		if receipt, path, receiptErr := ports.receipt(home, claim, destination); receiptErr == nil && receipt != nil {
			result.ReceiptPath = path
		} else if receiptErr != nil {
			return result, receiptErr
		} else if intent, _, intentErr := ports.pending(home, claim, destination, entry.Branch, entry.HeadSHA); intentErr != nil {
			return result, intentErr
		} else if intent != nil {
			// The prior invocation moved Git's registry but was interrupted before
			// it could append the completion receipt. --apply is deliberately
			// retry-safe: it corroborates this intent then completes the journal.
			result.RecoveryPending = true
		}
		return result, nil
	}
	if _, statErr := ports.lstat(destination); statErr == nil {
		result.Reason = "destination already exists: " + destination
		return result, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return result, fmt.Errorf("inspect relocation destination %s: %w", destination, statErr)
	}
	result.Eligible = true
	return result, nil
}

type relocationPlanPorts struct {
	claim     func(wbhome.Resolution, string) (workLogClaim, workLogProjection, string, string, error)
	placement func(string, string) (WorktreePlacement, error)
	receipt   func(string, workLogClaim, string) (*workLogRelocationReceipt, string, error)
	pending   func(string, workLogClaim, string, string, string) (*workLogRelocationIntent, string, error)
	lstat     func(string) (os.FileInfo, error)
}

func productionRelocationPlanPorts() relocationPlanPorts {
	return relocationPlanPorts{claim: activeWorkLogClaimAcrossHomes, placement: ResolveUserWorktreePlacement,
		receipt: latestRelocationReceipt, pending: pendingRelocationIntent, lstat: os.Lstat}
}

func relocationEligibility(entry ListResult) (bool, string) {
	switch {
	case entry.External:
		return false, "adopted external worktree is not relocated automatically"
	case entry.Locked:
		return false, lockedReason(entry, resumeInterruptedCommand(entry.Task))
	case entry.OwnerState == "active":
		return false, "worktree has an active owner; hand off or stop that owner before relocating"
	case !entry.Clean:
		return false, "worktree has local changes"
	default:
		return true, ""
	}
}

func applyRelocation(ctx context.Context, home string, options RelocateOptions, planned ListResult, result *RelocateResult) error {
	ports := productionRelocationApplyPorts()
	if options.applyPorts != nil {
		ports = *options.applyPorts
	}
	// Re-list through the exact original layout while holding the task lock.
	refreshed, err := ports.recheck(ctx, options, planned)
	if err != nil {
		return fmt.Errorf("recheck %s before relocation: %w", planned.Repository, err)
	}
	if refreshed.Locked || refreshed.OwnerState == "active" || !refreshed.Clean || refreshed.HeadSHA != result.HeadSHA || refreshed.Branch != result.Branch {
		return fmt.Errorf("relocation safety changed for %s; rerun the plan", planned.Repository)
	}
	claim, _, _, err := ports.claim(home, refreshed.WorktreeDir)
	if err != nil {
		return fmt.Errorf("recheck active Work Log claim for %s: %w", planned.Repository, err)
	}
	if claim.ClaimID != result.ClaimID {
		return fmt.Errorf("recheck active Work Log claim for %s: claim identity changed", planned.Repository)
	}
	if _, statErr := ports.lstat(result.Destination); !errors.Is(statErr, os.ErrNotExist) {
		if statErr == nil {
			return fmt.Errorf("relocation destination already exists: %s", result.Destination)
		}
		return fmt.Errorf("recheck relocation destination %s: %w", result.Destination, statErr)
	}
	destinationRoot, placement, err := ports.prepare(ctx, options.ProjectsRoot, refreshed, claim.BaseSHA, result.Destination, options.To)
	if err != nil {
		return err
	}
	move, path, err := ports.move(ctx, relocationMoveRequest{
		home: home, claim: claim, canonicalDir: refreshed.CanonicalDir, destinationRoot: destinationRoot,
		source: refreshed.WorktreeDir, intentSource: result.WorktreeDir, destination: result.Destination, to: options.To, head: result.HeadSHA,
		placement: placement, now: options.Now, afterMove: options.afterWorktreeMoveBeforeReceipt,
		intentContext:  "record relocation intent before moving " + refreshed.Repository,
		receiptContext: "record relocation receipt after moving " + refreshed.Repository,
		missingReceipt: "record relocation receipt after moving " + refreshed.Repository + " returned no receipt",
	})
	result.Repaired = move.Repaired
	if err != nil {
		return err
	}
	result.Applied, result.Finalized, result.ReceiptPath = true, true, path
	return nil
}

type relocationApplyPorts struct {
	recheck func(context.Context, RelocateOptions, ListResult) (ListResult, error)
	claim   func(string, string) (workLogClaim, workLogProjection, string, error)
	lstat   func(string) (os.FileInfo, error)
	prepare func(context.Context, string, ListResult, string, string, string) (string, relocationPlacementRecord, error)
	move    func(context.Context, relocationMoveRequest) (worktreeMoveOutcome, string, error)
}

func productionRelocationApplyPorts() relocationApplyPorts {
	return relocationApplyPorts{
		recheck: func(ctx context.Context, options RelocateOptions, planned ListResult) (ListResult, error) {
			return inspectLifecycleWorktree(ctx, options.ProjectsRoot, "", wbhome.Layout{WorktreesRoot: planned.WorktreesRoot, Local: planned.Local},
				planned.Task, planned.WorktreeDir, planned.Base, "", false, false, false, inspectPolicy{})
		},
		claim: activeWorkLogClaim, lstat: os.Lstat, prepare: prepareRelocationMove,
		move: func(ctx context.Context, request relocationMoveRequest) (worktreeMoveOutcome, string, error) {
			return runRelocationMove(ctx, request, productionRelocationMovePorts())
		},
	}
}

type relocationMoveRequest struct {
	home, canonicalDir, destinationRoot, source, intentSource, destination, to, head string
	claim                                                                            workLogClaim
	placement                                                                        relocationPlacementRecord
	now                                                                              func() time.Time
	prepareMove, afterMove                                                           func() error
	intentContext, receiptContext, missingReceipt                                    string
}

type relocationMovePorts struct {
	appendIntent  func(string, workLogClaim, string, string, string, string, relocationPlacementRecord, time.Time) (*workLogRelocationIntent, string, error)
	move          func(context.Context, string, string, string, string, worktreeMoveHooks) (worktreeMoveOutcome, error)
	appendReceipt func(string, workLogClaim, *workLogRelocationIntent, time.Time) (*workLogRelocationReceipt, string, error)
}

func productionRelocationMovePorts() relocationMovePorts {
	return relocationMovePorts{appendIntent: appendRelocationIntent, move: moveWorktree, appendReceipt: appendRelocationReceipt}
}

func runRelocationMove(ctx context.Context, request relocationMoveRequest, ports relocationMovePorts) (worktreeMoveOutcome, string, error) {
	intent, _, err := ports.appendIntent(request.home, request.claim, request.intentSource, request.destination, request.to, request.head, request.placement, request.now().UTC())
	if err != nil {
		return worktreeMoveOutcome{}, "", fmt.Errorf("%s: %w", request.intentContext, err)
	}
	if request.prepareMove != nil {
		if err := request.prepareMove(); err != nil {
			return worktreeMoveOutcome{}, "", err
		}
	}
	move, err := ports.move(ctx, request.canonicalDir, request.destinationRoot, request.source, request.destination, worktreeMoveHooks{})
	if err != nil {
		return move, "", err
	}
	if request.afterMove != nil {
		if err := request.afterMove(); err != nil {
			return move, "", err
		}
	}
	receipt, path, err := ports.appendReceipt(request.home, request.claim, intent, request.now().UTC())
	if err != nil {
		return move, "", fmt.Errorf("%s: %w", request.receiptContext, err)
	}
	if request.missingReceipt != "" && receipt == nil {
		return move, "", errors.New(request.missingReceipt)
	}
	return move, path, nil
}

// prepareRelocationMove records a path relative to the root that produced it,
// so a later worktrees.root change cannot reinterpret the durable receipt.
func prepareRelocationMove(ctx context.Context, projectsRoot string, entry ListResult, baseSHA, destination, to string) (string, relocationPlacementRecord, error) {
	relative, err := canonicalRelativeAddress(ctx, projectsRoot, entry.CanonicalDir)
	if err != nil {
		return "", relocationPlacementRecord{}, err
	}
	root := relocationDestinationRoot(destination, relative, to)
	if err := prepareRelocationDestination(ctx, entry, baseSHA, destination, relative, to); err != nil {
		return "", relocationPlacementRecord{}, err
	}
	return root, relocationPlacement(root, destination), nil
}

func relocationPlacement(root, destination string) relocationPlacementRecord {
	placementRelative, err := filepath.Rel(root, destination)
	if err != nil || placementRelative == "." || strings.HasPrefix(placementRelative, "..") {
		placementRelative = ""
	}
	return relocationPlacementRecord{Root: root, Relative: placementRelative}
}

func finalizeInterruptedRelocation(home string, options RelocateOptions, entry ListResult, result *RelocateResult) error {
	ports := productionRelocationFinalizePorts()
	if options.finalizePorts != nil {
		ports = *options.finalizePorts
	}
	if eligible, reason := relocationEligibility(entry); !eligible {
		return fmt.Errorf("interrupted relocation safety changed for %s: %s", entry.Repository, reason)
	}
	claim, _, _, err := ports.claim(home, entry.WorktreeDir)
	if err != nil {
		return fmt.Errorf("recheck active Work Log claim for %s: %w", entry.Repository, err)
	}
	if claim.ClaimID != result.ClaimID {
		return fmt.Errorf("recheck active Work Log claim for %s: claim identity changed", entry.Repository)
	}
	intent, _, err := ports.pending(home, claim, entry.WorktreeDir, entry.Branch, entry.HeadSHA)
	if err != nil {
		return err
	}
	if intent == nil {
		return fmt.Errorf("interrupted relocation for %s has no matching durable intent", entry.Repository)
	}
	_, path, err := ports.receipt(home, claim, intent, options.Now().UTC())
	if err != nil {
		return fmt.Errorf("finalize interrupted relocation for %s: %w", entry.Repository, err)
	}
	result.Applied, result.Finalized, result.ReceiptPath = true, true, path
	return nil
}

type relocationFinalizePorts struct {
	claim   func(string, string) (workLogClaim, workLogProjection, string, error)
	pending func(string, workLogClaim, string, string, string) (*workLogRelocationIntent, string, error)
	receipt func(string, workLogClaim, *workLogRelocationIntent, time.Time) (*workLogRelocationReceipt, string, error)
}

func productionRelocationFinalizePorts() relocationFinalizePorts {
	return relocationFinalizePorts{claim: activeWorkLogClaim, pending: pendingRelocationIntent, receipt: appendRelocationReceipt}
}

// relocationDestinationRoot returns the worktrees root a relocation
// destination sits below. relative is the destination's repository suffix below
// its task directory, which carries the literal host level in central mode; a
// legacy two-level suffix keeps working.
func relocationDestinationRoot(destination, relative, to string) string {
	if to == "local" {
		return filepath.Dir(destination)
	}
	suffix := string(filepath.Separator) + filepath.FromSlash(relative)
	taskPath, trimmed := strings.CutSuffix(destination, suffix)
	if !trimmed {
		return filepath.Dir(filepath.Dir(filepath.Dir(destination)))
	}
	return filepath.Dir(taskPath)
}

func prepareRelocationDestination(ctx context.Context, entry ListResult, baseSHA, destination, relative, to string) error {
	return prepareRelocationDestinationWithHooks(ctx, entry, baseSHA, destination, relative, to, relocationDestinationHooks{})
}

type relocationDestinationHooks struct {
	afterSharedRootOpen func()
}

func prepareRelocationDestinationWithHooks(ctx context.Context, entry ListResult, baseSHA, destination, relative, to string, hooks relocationDestinationHooks) error {
	if to == "local" {
		canonical, err := openCanonicalRepository(entry.CanonicalDir)
		if err != nil {
			return err
		}
		defer canonical.close()
		root, directory, err := prepareCanonicalWorktreesRoot(ctx, canonical, baseSHA)
		if err != nil {
			return err
		}
		defer func() { _ = directory.Close() }()
		if filepath.Clean(root) != filepath.Clean(filepath.Dir(destination)) || !directoryStillMatches(root, directory) {
			return fmt.Errorf("local relocation destination root changed: %s", filepath.Dir(destination))
		}
		return requireAbsentNoFollowChild(int(directory.Fd()), filepath.Base(destination))
	}
	suffix := string(filepath.Separator) + filepath.FromSlash(relative)
	taskPath, trimmed := strings.CutSuffix(destination, suffix)
	if !trimmed {
		return fmt.Errorf("shared relocation destination %s has no repository suffix below its task", destination)
	}
	rootPath, task := filepath.Dir(taskPath), filepath.Base(taskPath)
	root, err := openAbsoluteDirectoryNoFollow(rootPath, true)
	if err != nil {
		return fmt.Errorf("open shared relocation root: %w", err)
	}
	defer func() { _ = root.Close() }()
	if hooks.afterSharedRootOpen != nil {
		hooks.afterSharedRootOpen()
	}
	if !directoryStillMatches(rootPath, root) {
		return fmt.Errorf("shared relocation root changed: %s", rootPath)
	}
	if !validSafeSegment(task) {
		return fmt.Errorf("shared relocation destination has an invalid task segment %q", task)
	}
	taskFD, err := openOrCreateNoFollowDirectory(int(root.Fd()), task)
	if err != nil {
		return err
	}
	// A successful OpenOrCreateNoFollowDirectory returns an opened descriptor.
	// os.NewFile returns nil only for an invalid descriptor/handle; the opener
	// reports those as errors rather than returning them on this path.
	taskDir := os.NewFile(uintptr(taskFD), "wb-relocate-task")
	defer func() { _ = taskDir.Close() }()
	parent, repository := splitCloneRelative(relative)
	parentDirectory, _, err := openRelativeParentDirectory(taskDir, taskPath, parent)
	if err != nil {
		return err
	}
	defer func() { _ = parentDirectory.Close() }()
	return requireAbsentNoFollowChild(int(parentDirectory.Fd()), repository)
}

func relocationOperationID(claimID, source, destination, head string, at time.Time) string {
	hash := sha256.Sum256([]byte(strings.Join([]string{claimID, filepath.Clean(source), filepath.Clean(destination), head, at.UTC().Format(time.RFC3339Nano), randomHexToken(12)}, "\x00")))
	return hex.EncodeToString(hash[:16])
}

func relocationIntentName(claimID, operationID string) string {
	return claimID + "-" + operationID + ".intent.json"
}

func relocationReceiptName(claimID, operationID string) string {
	return claimID + "-" + operationID + ".completed.json"
}

type relocationJournal struct {
	intents  map[string]workLogRelocationIntent
	receipts map[string]workLogRelocationReceipt
	paths    map[string]string
}

func openRelocationJournal(run *os.File, runPath string, claim workLogClaim) (relocationJournal, error) {
	return openRelocationJournalWithNames(run, runPath, claim, func(directory *os.File) ([]string, error) {
		return directory.Readdirnames(-1)
	})
}

// openRelocationJournalWithNames preserves defensive validation for arbitrary
// enumeration input. The native caller reads names from the owned directory;
// a caller-local enumerator also exposes directory read failures without a
// global filesystem hook.
func openRelocationJournalWithNames(run *os.File, runPath string, claim workLogClaim, namesFrom func(*os.File) ([]string, error)) (relocationJournal, error) {
	journal := relocationJournal{intents: map[string]workLogRelocationIntent{}, receipts: map[string]workLogRelocationReceipt{}, paths: map[string]string{}}
	directory, err := openPrivateChild(run, "relocations", false)
	if errors.Is(err, os.ErrNotExist) {
		return journal, nil
	}
	if err != nil {
		return journal, err
	}
	defer func() { _ = directory.Close() }()
	names, err := namesFrom(directory)
	if err != nil {
		return journal, err
	}
	sort.Strings(names)
	for _, name := range names {
		var kind string
		switch {
		case strings.HasPrefix(name, claim.ClaimID+"-") && strings.HasSuffix(name, ".intent.json"):
			kind = "intent"
		case strings.HasPrefix(name, claim.ClaimID+"-") && strings.HasSuffix(name, ".completed.json"):
			kind = "receipt"
		default:
			continue
		}
		var record workLogRelocationIntent
		if err := readJSONAt(directory, name, &record); err != nil {
			return journal, fmt.Errorf("decode relocation journal %s: %w", name, err)
		}
		if err := validateRelocationRecord(record, claim, kind == "intent"); err != nil {
			return journal, fmt.Errorf("validate relocation journal %s: %w", name, err)
		}
		if name != relocationIntentName(claim.ClaimID, record.OperationID) && kind == "intent" || name != relocationReceiptName(claim.ClaimID, record.OperationID) && kind == "receipt" {
			return journal, fmt.Errorf("relocation journal filename does not bind operation %s", record.OperationID)
		}
		if kind == "intent" {
			if _, exists := journal.intents[record.OperationID]; exists {
				return journal, fmt.Errorf("duplicate relocation intent %s", record.OperationID)
			}
			journal.intents[record.OperationID] = record
		} else {
			if _, exists := journal.receipts[record.OperationID]; exists {
				return journal, fmt.Errorf("duplicate relocation completion %s", record.OperationID)
			}
			journal.receipts[record.OperationID] = record
		}
		journal.paths[record.OperationID+"/"+kind] = filepath.Join(runPath, "relocations", name)
	}
	for operationID, receipt := range journal.receipts {
		intent, exists := journal.intents[operationID]
		if !exists || !sameRelocationBinding(intent, receipt) {
			return journal, fmt.Errorf("relocation completion %s is not bound to its intent", operationID)
		}
	}
	return journal, nil
}

func sameRelocationBinding(intent workLogRelocationIntent, receipt workLogRelocationReceipt) bool {
	return intent.OperationID == receipt.OperationID && intent.ClaimID == receipt.ClaimID && intent.Task == receipt.Task &&
		intent.Repository == receipt.Repository && intent.Branch == receipt.Branch && intent.HeadSHA == receipt.HeadSHA &&
		intent.To == receipt.To && filepath.Clean(intent.Source) == filepath.Clean(receipt.Source) &&
		filepath.Clean(intent.Destination) == filepath.Clean(receipt.Destination) &&
		intent.DestinationRoot == receipt.DestinationRoot && intent.DestinationRelative == receipt.DestinationRelative &&
		intent.SourceRepository == receipt.SourceRepository && intent.DestinationRepository == receipt.DestinationRepository &&
		intent.RemoteURL == receipt.RemoteURL
}

func validateRelocationRecord(record workLogRelocationIntent, claim workLogClaim, intent bool) error {
	wantType := workLogRelocationType
	if intent {
		wantType = workLogRelocationIntentType
	}
	if record.Version != 1 || record.Type != wantType || !validSafeSegment(record.OperationID) || record.ClaimID != claim.ClaimID ||
		record.Task != claim.Task || record.Repository != claim.Repository || record.Branch != claim.Branch || record.HeadSHA == "" ||
		record.To != "local" && record.To != "shared" && record.To != "repository" && record.To != workLogRelocationLegacyCheckout || !canonicalRelocationPath(record.Source) ||
		!canonicalRelocationPath(record.Destination) || record.At.IsZero() {
		return errors.New("record identity is incomplete or does not match immutable claim")
	}
	if (record.DestinationRoot == "") != (record.DestinationRelative == "") {
		return errors.New("relocation destination placement record is incomplete")
	}
	if record.DestinationRoot != "" {
		if !canonicalRelocationPath(record.DestinationRoot) || filepath.IsAbs(record.DestinationRelative) ||
			filepath.Clean(record.DestinationRelative) != record.DestinationRelative ||
			filepath.Join(record.DestinationRoot, record.DestinationRelative) != filepath.Clean(record.Destination) {
			return errors.New("relocation destination placement record does not describe its destination")
		}
	}
	if record.To == "repository" || record.To == workLogRelocationLegacyCheckout {
		if _, _, err := splitRepository(record.SourceRepository); err != nil {
			return errors.New("repository relocation source identity is invalid")
		}
		if _, _, err := splitRepository(record.DestinationRepository); err != nil {
			return errors.New("repository relocation destination identity is invalid")
		}
		remote, err := gitremote.Parse(record.RemoteURL)
		if err != nil || record.DestinationRepository == record.SourceRepository || remote.Identity.Repository != record.DestinationRepository {
			return errors.New("repository relocation identity is incomplete or invalid")
		}
	} else if record.SourceRepository != "" || record.DestinationRepository != "" || record.RemoteURL != "" {
		return errors.New("worktree relocation unexpectedly changes repository identity")
	}
	if record.To != "repository" && record.To != workLogRelocationLegacyCheckout && filepath.Clean(record.Source) == filepath.Clean(record.Destination) {
		return errors.New("record source and destination are identical")
	}
	return nil
}

func canonicalRelocationPath(path string) bool {
	return filepath.IsAbs(path) && path == filepath.Clean(path)
}

func matchingPendingIntent(journal relocationJournal, claim workLogClaim, source, destination, to, branch, head string) (*workLogRelocationIntent, string, error) {
	var found *workLogRelocationIntent
	for operationID, intent := range journal.intents {
		if _, complete := journal.receipts[operationID]; complete {
			continue
		}
		if intent.ClaimID == claim.ClaimID && intent.Branch == branch && intent.HeadSHA == head && intent.To == to &&
			filepath.Clean(intent.Source) == filepath.Clean(source) && filepath.Clean(intent.Destination) == filepath.Clean(destination) {
			if found != nil {
				return nil, "", fmt.Errorf("multiple pending relocation intents for %s", destination)
			}
			copy := intent
			found = &copy
		}
	}
	if found == nil {
		return nil, "", nil
	}
	return found, journal.paths[found.OperationID+"/intent"], nil
}

func appendRelocationIntent(home string, claim workLogClaim, source, destination, to, head string, placement relocationPlacementRecord, at time.Time) (*workLogRelocationIntent, string, error) {
	return appendRelocationIntentForRepository(home, claim, source, destination, to, head, "", "", "", placement, at)
}

// lockedRelocationJournal owns both the claim lock/run descriptor and the
// private journal descriptor. Failed admission releases both; successful callers
// defer close until their immutable publication or replay decision is complete.
type lockedRelocationJournal struct {
	locked  *lockedWorkLogRun
	records *os.File
	journal relocationJournal
}

func openLockedRelocationJournal(home string, claim workLogClaim) (*lockedRelocationJournal, error) {
	locked, err := openLockedWorkLogRun(home, claim.EffortID, claim.RunID, claim.ClaimID, false)
	if err != nil {
		return nil, err
	}
	records, err := openPrivateChild(locked.directory, "relocations", true)
	if err != nil {
		locked.close()
		return nil, err
	}
	journal, err := openRelocationJournal(locked.directory, locked.path, claim)
	if err != nil {
		_ = records.Close()
		locked.close()
		return nil, err
	}
	return &lockedRelocationJournal{locked: locked, records: records, journal: journal}, nil
}

func (journal *lockedRelocationJournal) close() {
	_ = journal.records.Close()
	journal.locked.close()
}

func appendRelocationIntentForRepository(home string, claim workLogClaim, source, destination, to, head, sourceRepository, destinationRepository, remoteURL string, placement relocationPlacementRecord, at time.Time) (*workLogRelocationIntent, string, error) {
	owned, err := openLockedRelocationJournal(home, claim)
	if err != nil {
		return nil, "", err
	}
	defer owned.close()
	receipts, runPath, journal := owned.records, owned.locked.path, owned.journal
	if existing, path, err := matchingPendingIntent(journal, claim, source, destination, to, claim.Branch, head); err != nil || existing != nil {
		return existing, path, err
	}
	operationID := relocationOperationID(claim.ClaimID, source, destination, head, at)
	intent := &workLogRelocationIntent{Version: 1, Type: workLogRelocationIntentType, OperationID: operationID, ClaimID: claim.ClaimID, Task: claim.Task,
		Repository: claim.Repository, Branch: claim.Branch, HeadSHA: head, Source: filepath.Clean(source), Destination: filepath.Clean(destination), To: to,
		SourceRepository: sourceRepository, DestinationRepository: destinationRepository, RemoteURL: remoteURL, At: at}
	if placement.Root != "" && placement.Relative != "" {
		intent.DestinationRoot = filepath.Clean(placement.Root)
		intent.DestinationRelative = placement.Relative
	}
	name := relocationIntentName(claim.ClaimID, operationID)
	if err := writeJSONImmutableAt(receipts, name, intent, true); err != nil {
		return nil, "", err
	}
	return intent, filepath.Join(runPath, "relocations", name), nil
}

func appendRelocationReceipt(home string, claim workLogClaim, intent *workLogRelocationIntent, at time.Time) (*workLogRelocationReceipt, string, error) {
	return appendRelocationReceiptWithRead(home, claim, intent, at, readBytesAt)
}

// appendRelocationReceiptWithRead retains the post-snapshot byte read. A
// caller-local reader can expose real permission/replacement failures between
// journal validation and replay without a process-global filesystem hook.
func appendRelocationReceiptWithRead(home string, claim workLogClaim, intent *workLogRelocationIntent, at time.Time, read func(*os.File, string) ([]byte, error)) (*workLogRelocationReceipt, string, error) {
	if err := validateRelocationRecord(*intent, claim, true); err != nil {
		return nil, "", err
	}
	owned, err := openLockedRelocationJournal(home, claim)
	if err != nil {
		return nil, "", err
	}
	defer owned.close()
	receipts, runPath, journal := owned.records, owned.locked.path, owned.journal
	durableIntent, exists := journal.intents[intent.OperationID]
	if !exists || durableIntent != *intent {
		return nil, "", fmt.Errorf("relocation completion is not bound to its durable intent %s", intent.OperationID)
	}
	name := relocationReceiptName(claim.ClaimID, intent.OperationID)
	if existingBytes, readErr := read(receipts, name); readErr == nil {
		var existing workLogRelocationReceipt
		if err := json.Unmarshal(existingBytes, &existing); err != nil {
			return nil, "", fmt.Errorf("decode existing relocation receipt: %w", err)
		}
		if err := validateRelocationRecord(existing, claim, false); err == nil && existing.OperationID == intent.OperationID &&
			existing.HeadSHA == intent.HeadSHA && existing.To == intent.To && filepath.Clean(existing.Source) == filepath.Clean(intent.Source) && filepath.Clean(existing.Destination) == filepath.Clean(intent.Destination) {
			return &existing, filepath.Join(runPath, "relocations", name), nil
		}
		return nil, "", fmt.Errorf("relocation completion collision: %s", name)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, "", readErr
	}
	receipt := *intent
	receipt.Type = workLogRelocationType
	receipt.At = at
	if err := writeJSONImmutableAt(receipts, name, &receipt, true); err != nil {
		return nil, "", err
	}
	return &receipt, filepath.Join(runPath, "relocations", name), nil
}

type workLogRelocationResolution struct {
	receipt     *workLogRelocationReceipt
	receiptPath string
	repository  string
	worktree    string
}

func latestRelocationResolution(home string, claim workLogClaim, destination string) (workLogRelocationResolution, error) {
	resolution, err := resolveRelocationChain(home, claim)
	if err != nil {
		return workLogRelocationResolution{repository: claim.Repository, worktree: filepath.Clean(claim.Worktree)}, err
	}
	if resolution.receipt == nil || resolution.worktree != filepath.Clean(destination) {
		return workLogRelocationResolution{repository: claim.Repository, worktree: filepath.Clean(claim.Worktree)}, nil
	}
	return resolution, nil
}

// resolveRelocationChain walks every completed relocation receipt for claim,
// oldest first, and returns where the claim's worktree and repository
// currently are — following the chain from claim.Worktree (the immutable,
// frozen path recorded at claim creation) through every verified move since.
// Unlike latestRelocationResolution it does not require the caller to already
// know the current location: a caller resolving "where is this claim's
// worktree right now" (as opposed to "does it match this one candidate
// destination") uses this directly.
//
// It returns claim.Worktree/claim.Repository unchanged, with no error, when
// no relocation was ever recorded for this claim.
func resolveRelocationChain(home string, claim workLogClaim) (workLogRelocationResolution, error) {
	resolution := workLogRelocationResolution{repository: claim.Repository, worktree: filepath.Clean(claim.Worktree)}
	run, runPath, err := openWorkLogRun(home, claim.EffortID, claim.RunID, false)
	if err != nil {
		return resolution, err
	}
	defer func() { _ = run.Close() }()
	journal, err := openRelocationJournal(run, runPath, claim)
	if err != nil {
		return resolution, err
	}
	ordered := make([]workLogRelocationReceipt, 0, len(journal.receipts))
	for _, receipt := range journal.receipts {
		ordered = append(ordered, receipt)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].At.Equal(ordered[j].At) {
			return ordered[i].OperationID < ordered[j].OperationID
		}
		return ordered[i].At.Before(ordered[j].At)
	})
	for _, receipt := range ordered {
		if filepath.Clean(receipt.Source) != resolution.worktree {
			return resolution, fmt.Errorf("relocation receipt %s does not continue the immutable claim path", receipt.OperationID)
		}
		if receipt.To == "repository" || receipt.To == workLogRelocationLegacyCheckout {
			if receipt.SourceRepository != resolution.repository {
				return resolution, fmt.Errorf("repository relocation receipt %s does not continue the immutable claim repository", receipt.OperationID)
			}
			resolution.repository = receipt.DestinationRepository
		}
		resolution.worktree = filepath.Clean(receipt.Destination)
		copy := receipt
		resolution.receipt = &copy
		resolution.receiptPath = journal.paths[receipt.OperationID+"/receipt"]
	}
	return resolution, nil
}

func latestRelocationReceipt(home string, claim workLogClaim, destination string) (*workLogRelocationReceipt, string, error) {
	resolution, err := latestRelocationResolution(home, claim, destination)
	return resolution.receipt, resolution.receiptPath, err
}

func corroborateRepositoryRelocation(ctx context.Context, worktree, repository string) error {
	for _, push := range []bool{false, true} {
		urls, err := exactOriginURLs(ctx, worktree, push)
		if err != nil || len(urls) != 1 {
			return fmt.Errorf("relocated repository origin is ambiguous")
		}
		remote, err := gitremote.Parse(urls[0])
		if err != nil || remote.Identity.Repository != repository {
			return fmt.Errorf("relocated repository origin does not identify %s", repository)
		}
	}
	return nil
}

// ReverseRelocation moves a checkout back from destination to source, using
// the same move-then-repair-then-verify primitive Relocate applies forward,
// and appends to the exact same Work Log relocation journal Relocate does —
// an intent before the move, a receipt after it verifies — so a claim's
// current location resolves correctly (via resolveRelocationChain) once the
// reversal completes. It is the small, deliberate exception to "relocation
// goes through Relocate, never a copy of it": Relocate always computes its
// destination from the machine's CURRENT configured placement, so it has no
// way to target an arbitrary historical path on its own. It does not take a
// task lock of its own; a caller reversing a completed relocation (such as
// `wb layout migrate --undo`) already holds its own run-wide lock.
func ReverseRelocation(ctx context.Context, projectsRoot, canonicalDir, destination, source string, now time.Time) error {
	return reverseRelocationWithPorts(ctx, projectsRoot, canonicalDir, destination, source, now, productionRelocationReversePorts())
}

type relocationReversePorts struct {
	lstat         func(string) (os.FileInfo, error)
	resolve       func(string) (wbhome.Resolution, error)
	claim         func(wbhome.Resolution, string) (workLogClaim, *workLogTerminalRecord, string, error)
	head          func(context.Context, string) (string, error)
	prepareParent func(string) error
	move          func(context.Context, relocationMoveRequest) (worktreeMoveOutcome, string, error)
}

func productionRelocationReversePorts() relocationReversePorts {
	return relocationReversePorts{
		lstat: os.Lstat, resolve: wbhome.Resolve, claim: claimForRelocationAcrossHomes,
		head:          func(ctx context.Context, path string) (string, error) { return git(ctx, path, "rev-parse", "HEAD") },
		prepareParent: func(path string) error { return os.MkdirAll(path, 0o755) },
		move: func(ctx context.Context, request relocationMoveRequest) (worktreeMoveOutcome, string, error) {
			return runRelocationMove(ctx, request, productionRelocationMovePorts())
		},
	}
}

func reverseRelocationWithPorts(ctx context.Context, projectsRoot, canonicalDir, destination, source string, now time.Time, ports relocationReversePorts) error {
	if !filepath.IsAbs(destination) || !filepath.IsAbs(source) {
		return fmt.Errorf("relocation reversal paths must be absolute")
	}
	if _, statErr := ports.lstat(source); statErr == nil {
		return fmt.Errorf("relocation-reversal destination already exists: %s", source)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect relocation-reversal destination %s: %w", source, statErr)
	}
	resolution, err := ports.resolve(projectsRoot)
	if err != nil {
		return err
	}
	// claimForRelocationAcrossHomes, not activeWorkLogClaimAcrossHomes: a
	// caller reversing `wb layout migrate`'s own relocation (its only
	// caller) must be able to reverse a finished task's relocation too, per
	// the founder's decision (2026-09-18) that a finished task's checkout is
	// the safe case. This does not change `wb worktree relocate` itself,
	// which never calls ReverseRelocation.
	claim, _, home, err := ports.claim(resolution, destination)
	if err != nil {
		return fmt.Errorf("recheck Work Log claim before relocation reversal: %w", err)
	}
	headOutput, err := ports.head(ctx, destination)
	if err != nil {
		return fmt.Errorf("read HEAD before relocation reversal: %w", err)
	}
	head := strings.TrimSpace(headOutput)
	// filepath.Dir(source), not filepath.Dir(destination): moveWorktree's
	// worktreesRoot argument must bound the NEW path (here, source, the
	// restoration target) so the secure Git helper's write-capability root
	// covers where `worktree repair` must write the restored checkout's
	// .git file. Every other moveWorktree caller bounds its own new path
	// the same way; this reversal path had it backwards, which only
	// surfaced where Landlock actually confines the child (CI), not on a
	// machine where the secure Git capability is unavailable.
	_, _, err = ports.move(ctx, relocationMoveRequest{
		home: home, claim: claim, canonicalDir: canonicalDir, destinationRoot: filepath.Dir(source),
		source: destination, intentSource: destination, destination: source, to: "local", head: head, now: func() time.Time { return now },
		intentContext: "record relocation-reversal intent", receiptContext: "record relocation-reversal receipt",
		prepareMove: func() error {
			if err := ports.prepareParent(filepath.Dir(source)); err != nil {
				return fmt.Errorf("prepare relocation-reversal destination parent: %w", err)
			}
			return nil
		},
	})
	return err
}

func pendingRelocationIntent(home string, claim workLogClaim, destination, branch, head string) (*workLogRelocationIntent, string, error) {
	run, runPath, err := openWorkLogRun(home, claim.EffortID, claim.RunID, false)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = run.Close() }()
	journal, err := openRelocationJournal(run, runPath, claim)
	if err != nil {
		return nil, "", err
	}
	for operationID, intent := range journal.intents {
		if _, complete := journal.receipts[operationID]; complete {
			continue
		}
		if intent.Branch == branch && intent.HeadSHA == head && filepath.Clean(intent.Destination) == filepath.Clean(destination) {
			copy := intent
			return &copy, journal.paths[operationID+"/intent"], nil
		}
	}
	return nil, "", nil
}
