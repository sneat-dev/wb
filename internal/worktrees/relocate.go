package worktrees

import (
	"context"
	"crypto/rand"
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
	// afterWorktreeMoveBeforeReceipt is a test-only crash-window seam. A real
	// process interruption has the same durable state: an intent exists and Git
	// has registered the destination, but no completion receipt exists yet.
	afterWorktreeMoveBeforeReceipt func() error
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
}

type RelocateOutcome struct {
	Results     []RelocateResult `json:"results"`
	Diagnostics []ListDiagnostic `json:"diagnostics,omitempty"`
}

// workLogRelocationReceipt is immutable, append-only evidence that changes the
// path through which an active claim is corroborated. The claim itself retains
// the original checkout path as historical identity evidence.
type workLogRelocationIntent struct {
	Version     int       `json:"version"`
	Type        string    `json:"type"`
	OperationID string    `json:"operation_id"`
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

// workLogRelocationReceipt is written only after Git has registered the new
// path. The intent is written first so an interruption in between can be
// corroborated and finalized without ever changing the immutable claim.
type workLogRelocationReceipt = workLogRelocationIntent

const (
	workLogRelocationIntentType = "worktree.relocation-intent"
	workLogRelocationType       = "worktree.relocated"
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
			if err := finalizeInterruptedRelocation(resolution.Write.Home, options, entry, result); err != nil {
				return outcome, err
			}
			continue
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
		} else if receiptErr != nil {
			return result, receiptErr
		} else if intent, _, intentErr := pendingRelocationIntent(home, claim, destination, entry.Branch, entry.HeadSHA); intentErr != nil {
			return result, intentErr
		} else if intent != nil {
			// The prior invocation moved Git's registry but was interrupted before
			// it could append the completion receipt. --apply is deliberately
			// retry-safe: it corroborates this intent then completes the journal.
			result.RecoveryPending = true
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
	intent, _, err := appendRelocationIntent(home, claim, result.WorktreeDir, result.Destination, options.To, result.HeadSHA, options.Now().UTC())
	if err != nil {
		return fmt.Errorf("record relocation intent before moving %s: %w", refreshed.Repository, err)
	}
	move, err := moveWorktree(ctx, refreshed.CanonicalDir, relocationDestinationRoot(result.Destination, options.To), refreshed.WorktreeDir, result.Destination, worktreeMoveHooks{})
	result.Repaired = move.Repaired
	if err != nil {
		return err
	}
	if options.afterWorktreeMoveBeforeReceipt != nil {
		if err := options.afterWorktreeMoveBeforeReceipt(); err != nil {
			return err
		}
	}
	receipt, path, err := appendRelocationReceipt(home, claim, intent, options.Now().UTC())
	if err != nil {
		return fmt.Errorf("record relocation receipt after moving %s: %w", refreshed.Repository, err)
	}
	if receipt == nil {
		return fmt.Errorf("record relocation receipt after moving %s returned no receipt", refreshed.Repository)
	}
	result.Applied, result.Finalized, result.ReceiptPath = true, true, path
	return nil
}

func finalizeInterruptedRelocation(home string, options RelocateOptions, entry ListResult, result *RelocateResult) error {
	if eligible, reason := relocationEligibility(entry); !eligible {
		return fmt.Errorf("interrupted relocation safety changed for %s: %s", entry.Repository, reason)
	}
	claim, _, _, err := activeWorkLogClaim(home, entry.WorktreeDir)
	if err != nil {
		return fmt.Errorf("recheck active Work Log claim for %s: %w", entry.Repository, err)
	}
	if claim.ClaimID != result.ClaimID {
		return fmt.Errorf("recheck active Work Log claim for %s: claim identity changed", entry.Repository)
	}
	intent, _, err := pendingRelocationIntent(home, claim, entry.WorktreeDir, entry.Branch, entry.HeadSHA)
	if err != nil {
		return err
	}
	if intent == nil {
		return fmt.Errorf("interrupted relocation for %s has no matching durable intent", entry.Repository)
	}
	_, path, err := appendRelocationReceipt(home, claim, intent, options.Now().UTC())
	if err != nil {
		return fmt.Errorf("finalize interrupted relocation for %s: %w", entry.Repository, err)
	}
	result.Applied, result.Finalized, result.ReceiptPath = true, true, path
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

func relocationOperationID(claimID, source, destination, head string, at time.Time) (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(strings.Join([]string{claimID, filepath.Clean(source), filepath.Clean(destination), head, at.UTC().Format(time.RFC3339Nano), hex.EncodeToString(random)}, "\x00")))
	return hex.EncodeToString(hash[:16]), nil
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
	journal := relocationJournal{intents: map[string]workLogRelocationIntent{}, receipts: map[string]workLogRelocationReceipt{}, paths: map[string]string{}}
	directory, err := openPrivateChild(run, "relocations", false)
	if errors.Is(err, os.ErrNotExist) {
		return journal, nil
	}
	if err != nil {
		return journal, err
	}
	defer directory.Close()
	names, err := directory.Readdirnames(-1)
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
		filepath.Clean(intent.Destination) == filepath.Clean(receipt.Destination)
}

func validateRelocationRecord(record workLogRelocationIntent, claim workLogClaim, intent bool) error {
	wantType := workLogRelocationType
	if intent {
		wantType = workLogRelocationIntentType
	}
	if record.Version != 1 || record.Type != wantType || !validSafeSegment(record.OperationID) || record.ClaimID != claim.ClaimID ||
		record.Task != claim.Task || record.Repository != claim.Repository || record.Branch != claim.Branch || record.HeadSHA == "" ||
		record.To != "local" && record.To != "shared" || record.Source == "" || record.Destination == "" || record.At.IsZero() {
		return errors.New("record identity is incomplete or does not match immutable claim")
	}
	if filepath.Clean(record.Source) == filepath.Clean(record.Destination) {
		return errors.New("record source and destination are identical")
	}
	return nil
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

func appendRelocationIntent(home string, claim workLogClaim, source, destination, to, head string, at time.Time) (*workLogRelocationIntent, string, error) {
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
	journal, err := openRelocationJournal(run, runPath, claim)
	if err != nil {
		return nil, "", err
	}
	if existing, path, err := matchingPendingIntent(journal, claim, source, destination, to, claim.Branch, head); err != nil || existing != nil {
		return existing, path, err
	}
	operationID, err := relocationOperationID(claim.ClaimID, source, destination, head, at)
	if err != nil {
		return nil, "", err
	}
	intent := &workLogRelocationIntent{Version: 1, Type: workLogRelocationIntentType, OperationID: operationID, ClaimID: claim.ClaimID, Task: claim.Task,
		Repository: claim.Repository, Branch: claim.Branch, HeadSHA: head, Source: filepath.Clean(source), Destination: filepath.Clean(destination), To: to, At: at}
	name := relocationIntentName(claim.ClaimID, operationID)
	if err := writeJSONImmutableAt(receipts, name, intent, true); err != nil {
		return nil, "", err
	}
	return intent, filepath.Join(runPath, "relocations", name), nil
}

func appendRelocationReceipt(home string, claim workLogClaim, intent *workLogRelocationIntent, at time.Time) (*workLogRelocationReceipt, string, error) {
	if err := validateRelocationRecord(*intent, claim, true); err != nil {
		return nil, "", err
	}
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
	journal, err := openRelocationJournal(run, runPath, claim)
	if err != nil {
		return nil, "", err
	}
	durableIntent, exists := journal.intents[intent.OperationID]
	if !exists || durableIntent != *intent {
		return nil, "", fmt.Errorf("relocation completion is not bound to its durable intent %s", intent.OperationID)
	}
	name := relocationReceiptName(claim.ClaimID, intent.OperationID)
	if existingBytes, readErr := readBytesAt(receipts, name); readErr == nil {
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

func latestRelocationReceipt(home string, claim workLogClaim, destination string) (*workLogRelocationReceipt, string, error) {
	run, runPath, err := openWorkLogRun(home, claim.EffortID, claim.RunID, false)
	if err != nil {
		return nil, "", err
	}
	defer run.Close()
	journal, err := openRelocationJournal(run, runPath, claim)
	if err != nil {
		return nil, "", err
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
	current := filepath.Clean(claim.Worktree)
	var latest *workLogRelocationReceipt
	for _, receipt := range ordered {
		if filepath.Clean(receipt.Source) != current {
			return nil, "", fmt.Errorf("relocation receipt %s does not continue the immutable claim path", receipt.OperationID)
		}
		current = filepath.Clean(receipt.Destination)
		copy := receipt
		latest = &copy
	}
	if latest == nil || current != filepath.Clean(destination) {
		return nil, "", nil
	}
	return latest, journal.paths[latest.OperationID+"/receipt"], nil
}

func pendingRelocationIntent(home string, claim workLogClaim, destination, branch, head string) (*workLogRelocationIntent, string, error) {
	run, runPath, err := openWorkLogRun(home, claim.EffortID, claim.RunID, false)
	if err != nil {
		return nil, "", err
	}
	defer run.Close()
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
