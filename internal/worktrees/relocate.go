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
}

type RelocateResult struct {
	Task         string `json:"task"`
	Repository   string `json:"repository"`
	CanonicalDir string `json:"canonical_dir"`
	WorktreeDir  string `json:"worktree_dir"`
	Destination  string `json:"destination"`
	Branch       string `json:"branch"`
	HeadSHA      string `json:"head_sha"`
	To           string `json:"to"`
	ClaimID      string `json:"claim_id,omitempty"`
	Eligible     bool   `json:"eligible"`
	Applied      bool   `json:"applied"`
	AlreadyThere bool   `json:"already_there,omitempty"`
	Repaired     bool   `json:"repaired,omitempty"`
	ReceiptPath  string `json:"receipt_path,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

type RelocateOutcome struct {
	Results     []RelocateResult `json:"results"`
	Diagnostics []ListDiagnostic `json:"diagnostics,omitempty"`
}

// workLogRelocationReceipt is immutable, append-only evidence that changes the
// path through which an active claim is corroborated. The claim itself retains
// the original checkout path as historical identity evidence.
type workLogRelocationReceipt struct {
	Version     int       `json:"version"`
	Type        string    `json:"type"`
	ClaimID     string    `json:"claim_id"`
	Task        string    `json:"task"`
	Repository  string    `json:"repository"`
	Branch      string    `json:"branch"`
	HeadSHA     string    `json:"head_sha"`
	Source      string    `json:"source"`
	Destination string    `json:"destination"`
	To          string    `json:"to"`
	At          time.Time `json:"at"`
}

const workLogRelocationType = "worktree.relocated"

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
	resolution, err := wbhome.Resolve(options.ProjectsRoot)
	if err != nil {
		return RelocateOutcome{}, err
	}
	listed, err := ListWithDiagnostics(ctx, ListOptions{ProjectsRoot: options.ProjectsRoot, Task: options.Task, Filter: options.Filter, GitHub: false})
	if err != nil {
		return RelocateOutcome{}, err
	}
	if len(listed.Results) == 0 && len(listed.Diagnostics) == 0 {
		return RelocateOutcome{}, fmt.Errorf("WB worktree task %q was not found", options.Task)
	}
	outcome := RelocateOutcome{Diagnostics: listed.Diagnostics}
	for _, entry := range listed.Results {
		result, planErr := planRelocation(ctx, resolution.Write.Home, options, entry)
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
		if !result.Eligible || result.AlreadyThere {
			continue
		}
		entry, found := findRelocationEntry(listed.Results, result.WorktreeDir)
		if !found {
			return outcome, fmt.Errorf("relocation plan lost worktree %s", result.WorktreeDir)
		}
		if err := applyRelocation(ctx, resolution.Write.Home, options, entry, result); err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

func findRelocationEntry(entries []ListResult, path string) (ListResult, bool) {
	for _, entry := range entries {
		if filepath.Clean(entry.WorktreeDir) == filepath.Clean(path) {
			return entry, true
		}
	}
	return ListResult{}, false
}

func planRelocation(ctx context.Context, home string, options RelocateOptions, entry ListResult) (RelocateResult, error) {
	result := RelocateResult{Task: entry.Task, Repository: entry.Repository, CanonicalDir: entry.CanonicalDir,
		WorktreeDir: entry.WorktreeDir, Branch: entry.Branch, HeadSHA: entry.HeadSHA, To: options.To}
	claim, _, _, claimErr := activeWorkLogClaim(home, entry.WorktreeDir)
	if claimErr != nil {
		result.Reason = "active Work Log claim is not corroborated: " + claimErr.Error()
		return result, nil
	}
	result.ClaimID = claim.ClaimID
	placement, err := ResolveUserWorktreePlacement(entry.CanonicalDir)
	if err != nil {
		return result, err
	}
	if options.To == "shared" && placement.RepositoryLocal {
		result.Reason = "--to=shared requires worktrees.root in the user worktrees configuration"
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
		if receipt, path, receiptErr := latestRelocationReceipt(home, claim, destination); receiptErr == nil && receipt != nil {
			result.ReceiptPath = path
		}
		return result, nil
	}
	if _, statErr := os.Lstat(destination); statErr == nil {
		result.Reason = "destination already exists: " + destination
		return result, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return result, fmt.Errorf("inspect relocation destination %s: %w", destination, statErr)
	}
	result.Eligible = true
	return result, nil
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
	// Re-list through the exact original layout while holding the task lock.
	refreshed, err := inspectLifecycleWorktree(ctx, options.ProjectsRoot, "", wbhome.Layout{WorktreesRoot: planned.WorktreesRoot, Local: planned.Local},
		planned.Task, planned.WorktreeDir, planned.Base, "", false, false, false, inspectPolicy{})
	if err != nil {
		return fmt.Errorf("recheck %s before relocation: %w", planned.Repository, err)
	}
	if refreshed.Locked || refreshed.OwnerState == "active" || !refreshed.Clean || refreshed.HeadSHA != result.HeadSHA || refreshed.Branch != result.Branch {
		return fmt.Errorf("relocation safety changed for %s; rerun the plan", planned.Repository)
	}
	claim, _, _, err := activeWorkLogClaim(home, refreshed.WorktreeDir)
	if err != nil {
		return fmt.Errorf("recheck active Work Log claim for %s: %w", planned.Repository, err)
	}
	if claim.ClaimID != result.ClaimID {
		return fmt.Errorf("recheck active Work Log claim for %s: claim identity changed", planned.Repository)
	}
	if _, statErr := os.Lstat(result.Destination); !errors.Is(statErr, os.ErrNotExist) {
		if statErr == nil {
			return fmt.Errorf("relocation destination already exists: %s", result.Destination)
		}
		return fmt.Errorf("recheck relocation destination %s: %w", result.Destination, statErr)
	}
	if err := prepareRelocationDestination(ctx, refreshed, claim.BaseSHA, result.Destination, options.To); err != nil {
		return err
	}
	move, err := moveWorktree(ctx, refreshed.CanonicalDir, relocationDestinationRoot(result.Destination, options.To), refreshed.WorktreeDir, result.Destination, worktreeMoveHooks{})
	result.Repaired = move.Repaired
	if err != nil {
		return err
	}
	receipt, path, err := appendRelocationReceipt(home, claim, result.WorktreeDir, result.Destination, options.To, result.HeadSHA, options.Now().UTC())
	if err != nil {
		return fmt.Errorf("record relocation receipt after moving %s: %w", refreshed.Repository, err)
	}
	if receipt == nil {
		return fmt.Errorf("record relocation receipt after moving %s returned no receipt", refreshed.Repository)
	}
	result.Applied, result.ReceiptPath = true, path
	return nil
}

func relocationDestinationRoot(destination, to string) string {
	if to == "local" {
		return filepath.Dir(destination)
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(destination)))
}

func prepareRelocationDestination(ctx context.Context, entry ListResult, baseSHA, destination, to string) error {
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
		defer directory.Close()
		if filepath.Clean(root) != filepath.Clean(filepath.Dir(destination)) || !directoryStillMatches(root, directory) {
			return fmt.Errorf("local relocation destination root changed: %s", filepath.Dir(destination))
		}
		return requireAbsentNoFollowChild(int(directory.Fd()), filepath.Base(destination))
	}
	root, err := openAbsoluteDirectoryNoFollow(filepath.Dir(filepath.Dir(filepath.Dir(destination))), true)
	if err != nil {
		return fmt.Errorf("open shared relocation root: %w", err)
	}
	defer root.Close()
	if !directoryStillMatches(filepath.Dir(filepath.Dir(filepath.Dir(destination))), root) {
		return fmt.Errorf("shared relocation root changed: %s", filepath.Dir(filepath.Dir(filepath.Dir(destination))))
	}
	owner := filepath.Base(filepath.Dir(destination))
	task := filepath.Base(filepath.Dir(filepath.Dir(destination)))
	taskFD, err := openOrCreateNoFollowDirectory(int(root.Fd()), task)
	if err != nil {
		return err
	}
	taskDir := os.NewFile(uintptr(taskFD), "wb-relocate-task")
	defer taskDir.Close()
	ownerFD, err := openOrCreateNoFollowDirectory(int(taskDir.Fd()), owner)
	if err != nil {
		return err
	}
	ownerDir := os.NewFile(uintptr(ownerFD), "wb-relocate-owner")
	defer ownerDir.Close()
	return requireAbsentNoFollowChild(int(ownerDir.Fd()), filepath.Base(destination))
}

func relocationReceiptName(claimID, source, destination string) string {
	hash := sha256.Sum256([]byte(claimID + "\x00" + filepath.Clean(source) + "\x00" + filepath.Clean(destination)))
	return claimID + "-" + hex.EncodeToString(hash[:]) + ".json"
}

func appendRelocationReceipt(home string, claim workLogClaim, source, destination, to, head string, at time.Time) (*workLogRelocationReceipt, string, error) {
	run, runPath, err := openWorkLogRun(home, claim.EffortID, claim.RunID, false)
	if err != nil {
		return nil, "", err
	}
	defer run.Close()
	unlock, err := lockClaim(run, claim.ClaimID)
	if err != nil {
		return nil, "", err
	}
	defer unlock()
	receipts, err := openPrivateChild(run, "relocations", true)
	if err != nil {
		return nil, "", err
	}
	defer receipts.Close()
	name := relocationReceiptName(claim.ClaimID, source, destination)
	if existingBytes, readErr := readBytesAt(receipts, name); readErr == nil {
		var existing workLogRelocationReceipt
		if err := json.Unmarshal(existingBytes, &existing); err != nil {
			return nil, "", fmt.Errorf("decode existing relocation receipt: %w", err)
		}
		if existing.Version == 1 && existing.Type == workLogRelocationType && existing.ClaimID == claim.ClaimID &&
			existing.Task == claim.Task && existing.Repository == claim.Repository && existing.Branch == claim.Branch &&
			existing.HeadSHA == head && filepath.Clean(existing.Source) == filepath.Clean(source) &&
			filepath.Clean(existing.Destination) == filepath.Clean(destination) && existing.To == to {
			return &existing, filepath.Join(runPath, "relocations", name), nil
		}
		return nil, "", fmt.Errorf("relocation receipt collision: %s", name)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, "", readErr
	}
	receipt := &workLogRelocationReceipt{Version: 1, Type: workLogRelocationType, ClaimID: claim.ClaimID, Task: claim.Task,
		Repository: claim.Repository, Branch: claim.Branch, HeadSHA: head, Source: filepath.Clean(source), Destination: filepath.Clean(destination), To: to, At: at}
	if err := writeJSONImmutableAt(receipts, name, receipt, true); err != nil {
		return nil, "", err
	}
	return receipt, filepath.Join(runPath, "relocations", name), nil
}

func latestRelocationReceipt(home string, claim workLogClaim, destination string) (*workLogRelocationReceipt, string, error) {
	run, runPath, err := openWorkLogRun(home, claim.EffortID, claim.RunID, false)
	if err != nil {
		return nil, "", err
	}
	defer run.Close()
	receipts, err := openPrivateChild(run, "relocations", false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	defer receipts.Close()
	names, err := receipts.Readdirnames(-1)
	if err != nil {
		return nil, "", err
	}
	sort.Strings(names)
	for _, name := range names {
		if !strings.HasPrefix(name, claim.ClaimID+"-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		var receipt workLogRelocationReceipt
		if err := readJSONAt(receipts, name, &receipt); err != nil {
			return nil, "", err
		}
		if receipt.Version == 1 && receipt.Type == workLogRelocationType && receipt.ClaimID == claim.ClaimID && receipt.Task == claim.Task && receipt.Repository == claim.Repository && receipt.Branch == claim.Branch && filepath.Clean(receipt.Destination) == filepath.Clean(destination) {
			return &receipt, filepath.Join(runPath, "relocations", name), nil
		}
	}
	return nil, "", nil
}
