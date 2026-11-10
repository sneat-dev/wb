package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// TerminalLockRecoveryOptions names one abandoned operation shell and the
// historical landing receipt that proves its work reached the remote target.
type TerminalLockRecoveryOptions struct {
	ProjectsRoot string
	Task         string
	ReceiptPath  string
	Apply        bool
}

type TerminalLockRecoveryOutcome struct {
	Task       string `json:"task"`
	Receipt    string `json:"receipt"`
	Repository string `json:"repository"`
	Target     string `json:"target"`
	TargetSHA  string `json:"target_sha"`
	LandingSHA string `json:"landing_sha"`
	LockPath   string `json:"lock_path"`
	PID        int    `json:"pid"`
	Applied    bool   `json:"applied"`
	AuditPath  string `json:"audit_path,omitempty"`
}

type terminalLockLandingReceipt struct {
	ID         string `json:"id"`
	Repository string `json:"repository"`
	Target     string `json:"target"`
	LandingSHA string `json:"landing_sha"`
	Status     string `json:"status"`
	Cleanup    bool   `json:"cleanup_requested"`
}

// RecoverTerminalOperationLock retires only an inert, lock-only task shell.
// Normal --resume-interrupted intentionally needs an eligible worktree; this
// path cannot change a merge receipt's status or retire any repository ref.
func RecoverTerminalOperationLock(ctx context.Context, options TerminalLockRecoveryOptions) (TerminalLockRecoveryOutcome, error) {
	var outcome TerminalLockRecoveryOutcome
	if !validSafeSegment(options.Task) || options.ReceiptPath == "" {
		return outcome, fmt.Errorf("terminal lock recovery requires one safe task name and an exact merge receipt")
	}
	root, err := filepath.Abs(options.ProjectsRoot)
	if err != nil {
		return outcome, err
	}
	receiptsRoot := filepath.Join(root, ".wb", "reports", "worktree-merge")
	receiptPath, err := filepath.Abs(options.ReceiptPath)
	if err != nil || filepath.Dir(receiptPath) != receiptsRoot {
		return outcome, fmt.Errorf("merge receipt must be an exact file under %s", receiptsRoot)
	}
	receiptInfo, err := os.Lstat(receiptPath)
	if err != nil || !receiptInfo.Mode().IsRegular() {
		return outcome, fmt.Errorf("merge receipt must be a regular file: %v", err)
	}
	receiptBytes, err := os.ReadFile(receiptPath)
	if err != nil {
		return outcome, fmt.Errorf("read merge receipt: %w", err)
	}
	var receipt terminalLockLandingReceipt
	if err := json.Unmarshal(receiptBytes, &receipt); err != nil {
		return outcome, fmt.Errorf("parse merge receipt: %w", err)
	}
	// Merge operations may retain their base task lock and an earlier candidate
	// lock. Both names are one 12-hex suffix from the same operation prefix.
	if filepath.Base(receiptPath) != receipt.ID+".json" ||
		!isTerminalLockOperationName(options.Task, receipt.ID) ||
		receipt.Status != "landed_canonical_sync_blocked" || !receipt.Cleanup ||
		!isGitObjectID(receipt.LandingSHA) || !validReceiptRepository(receipt.Repository) ||
		!validSafeSegment(receipt.Target) {
		return outcome, fmt.Errorf("merge receipt does not prove this terminal operation's landing")
	}
	resolution, err := wbhome.Resolve(root)
	if err != nil {
		return outcome, err
	}
	handle, recovery, err := reclaimNamedInterruptedCleanupTask(resolution, options.Task)
	if err != nil {
		return outcome, err
	}
	defer func() {
		if handle != nil {
			handle.preserveLock()
			handle.close()
		}
	}()
	if err := handle.validateHeldLock(); err != nil {
		return outcome, err
	}
	if _, err := handle.task.Seek(0, 0); err != nil {
		return outcome, err
	}
	entries, err := handle.task.ReadDir(-1)
	if err != nil || len(entries) != 1 || entries[0].Name() != ".lock" {
		return outcome, fmt.Errorf("operation shell is not lock-only")
	}
	listed, err := ListWithDiagnostics(ctx, ListOptions{ProjectsRoot: root, Task: options.Task, Workers: 1})
	if err != nil {
		return outcome, err
	}
	if len(listed.Results) != 0 || len(listed.Diagnostics) != 0 {
		return outcome, fmt.Errorf("operation still has worktree candidates or incomplete discovery")
	}
	backlog, quarantine, err := loadResumableLifecycleBacklog(ctx, resolution.Write.Home, root,
		[]string{recovery.WorktreesRoot}, map[string]bool{options.Task: true}, "", "removed")
	if err != nil || len(backlog) != 0 || len(quarantine) != 0 {
		return outcome, fmt.Errorf("operation still has cleanup backlog or uncertain records: %v", err)
	}
	canonical := filepath.Join(root, filepath.FromSlash(receipt.Repository))
	targetSHA, err := fetchRemoteTargetHead(ctx, canonical, receipt.Target)
	if err != nil {
		return outcome, fmt.Errorf("fresh target proof: %w", err)
	}
	contained, err := isAncestor(ctx, canonical, receipt.LandingSHA, targetSHA)
	if err != nil || !contained {
		return outcome, fmt.Errorf("receipt landing %s is not proven in freshly fetched origin/%s at %s: %v", receipt.LandingSHA, receipt.Target, targetSHA, err)
	}
	outcome = TerminalLockRecoveryOutcome{Task: options.Task, Receipt: receiptPath,
		Repository: receipt.Repository, Target: receipt.Target, TargetSHA: targetSHA,
		LandingSHA: receipt.LandingSHA, LockPath: recovery.Path, PID: recovery.PID}
	if !options.Apply {
		return outcome, nil
	}
	if err := handle.validateHeldLock(); err != nil {
		return outcome, err
	}
	// Durable pre-mutation evidence remains even if process death interrupts
	// the lock quarantine. The source merge receipt is never rewritten.
	auditRoot := filepath.Join(root, ".wb", "reports", "worktree-lock-recovery")
	if err := os.MkdirAll(auditRoot, 0700); err != nil {
		return outcome, err
	}
	digest := sha256.Sum256(receiptBytes)
	audit := struct {
		TerminalLockRecoveryOutcome
		ReceiptSHA256 string    `json:"receipt_sha256"`
		RecordedAt    time.Time `json:"recorded_at"`
	}{outcome, hex.EncodeToString(digest[:]), time.Now().UTC()}
	auditBytes, err := json.MarshalIndent(audit, "", "  ")
	if err != nil {
		return outcome, err
	}
	auditFile, err := os.CreateTemp(auditRoot, options.Task+"-*.json")
	if err != nil {
		return outcome, err
	}
	outcome.AuditPath = auditFile.Name()
	if _, err := auditFile.Write(append(auditBytes, '\n')); err != nil {
		_ = auditFile.Close()
		return outcome, err
	}
	if err := auditFile.Sync(); err != nil {
		_ = auditFile.Close()
		return outcome, err
	}
	if err := auditFile.Close(); err != nil {
		return outcome, err
	}
	if err := handle.lock.release(); err != nil {
		return outcome, err
	}
	purgeTerminalTaskLockDebris(handle)
	if !removeEmptyTaskDirectory(handle) {
		return outcome, fmt.Errorf("lock retired but task shell was not empty; audit: %s", outcome.AuditPath)
	}
	// Release closes the lock descriptor. Avoid trying to preserve it again.
	handle.lock = operationLock{}
	outcome.Applied = true
	return outcome, nil
}

func validReceiptRepository(repository string) bool {
	parts := strings.Split(repository, "/")
	return len(parts) == 2 && validSafeSegment(parts[0]) && validSafeSegment(parts[1])
}

func isTerminalLockOperationName(task, receiptID string) bool {
	base := receiptID
	index := strings.LastIndexByte(base, '-')
	if index < 0 || !isHex12(base[index+1:]) {
		return false
	}
	base = base[:index]
	if task == base {
		return true
	}
	if !strings.HasPrefix(task, base+"-") {
		return false
	}
	return isHex12(strings.TrimPrefix(task, base+"-"))
}

func isHex12(suffix string) bool {
	if len(suffix) != 12 {
		return false
	}
	for _, r := range suffix {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
