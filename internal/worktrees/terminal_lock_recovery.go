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

// These ports keep the trust checks unchanged while allowing tests to prove
// failure handling at filesystem boundaries that cannot be induced reliably
// with permissions or timing races.
type terminalLockRecoveryPorts struct {
	abs          func(string) (string, error)
	readReceipt  func(string) ([]byte, error)
	resolve      func(string) (wbhome.Resolution, error)
	validateLock func(*cleanupTaskHandle) error
	seekTask     func(*os.File) error
	list         func(context.Context, ListOptions) (ListOutcome, error)
	hasBacklog   func(context.Context, wbhome.Resolution, string, string, string) (bool, error)
	fetchTarget  func(context.Context, string, string) (string, error)
	marshalAudit func(any) ([]byte, error)
	createAudit  func(string, string) (*os.File, error)
	writeAudit   func(*os.File, []byte) (int, error)
	syncAudit    func(*os.File) error
	closeAudit   func(*os.File) error
	releaseLock  func(*cleanupTaskHandle) error
	removeShell  func(*cleanupTaskHandle) bool
}

func defaultTerminalLockRecoveryPorts() terminalLockRecoveryPorts {
	return terminalLockRecoveryPorts{
		abs: filepath.Abs, readReceipt: os.ReadFile, resolve: wbhome.Resolve,
		validateLock: func(handle *cleanupTaskHandle) error { return handle.validateHeldLock() },
		seekTask:     func(file *os.File) error { _, err := file.Seek(0, 0); return err },
		list:         ListWithDiagnostics,
		hasBacklog: func(ctx context.Context, resolution wbhome.Resolution, root, task, worktreesRoot string) (bool, error) {
			backlog, quarantine, err := loadResumableLifecycleBacklog(ctx, resolution.Write.Home, root,
				[]string{worktreesRoot}, map[string]bool{task: true}, "", "removed")
			return len(backlog) != 0 || len(quarantine) != 0, err
		},
		fetchTarget:  fetchRemoteTargetHead,
		marshalAudit: func(value any) ([]byte, error) { return json.MarshalIndent(value, "", "  ") },
		createAudit:  os.CreateTemp,
		writeAudit:   func(file *os.File, content []byte) (int, error) { return file.Write(content) },
		syncAudit:    func(file *os.File) error { return file.Sync() },
		closeAudit:   func(file *os.File) error { return file.Close() },
		releaseLock:  func(handle *cleanupTaskHandle) error { return handle.lock.release() },
		removeShell:  removeEmptyTaskDirectory,
	}
}

// RecoverTerminalOperationLock retires only an inert, lock-only task shell.
// Normal --resume-interrupted intentionally needs an eligible worktree; this
// path cannot change a merge receipt's status or retire any repository ref.
func RecoverTerminalOperationLock(ctx context.Context, options TerminalLockRecoveryOptions) (TerminalLockRecoveryOutcome, error) {
	return recoverTerminalOperationLock(ctx, options, defaultTerminalLockRecoveryPorts())
}

func recoverTerminalOperationLock(ctx context.Context, options TerminalLockRecoveryOptions, ports terminalLockRecoveryPorts) (TerminalLockRecoveryOutcome, error) {
	var outcome TerminalLockRecoveryOutcome
	if !validSafeSegment(options.Task) || options.ReceiptPath == "" {
		return outcome, fmt.Errorf("terminal lock recovery requires one safe task name and an exact merge receipt")
	}
	root, err := ports.abs(options.ProjectsRoot)
	if err != nil {
		return outcome, err
	}
	receiptsRoot := filepath.Join(root, ".wb", "reports", "worktree-merge")
	receiptPath, err := ports.abs(options.ReceiptPath)
	if err != nil || filepath.Dir(receiptPath) != receiptsRoot {
		return outcome, fmt.Errorf("merge receipt must be an exact file under %s", receiptsRoot)
	}
	receiptInfo, err := os.Lstat(receiptPath)
	if err != nil || !receiptInfo.Mode().IsRegular() {
		return outcome, fmt.Errorf("merge receipt must be a regular file: %v", err)
	}
	receiptBytes, err := ports.readReceipt(receiptPath)
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
	resolution, err := ports.resolve(root)
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
	if err := ports.validateLock(handle); err != nil {
		return outcome, err
	}
	if err := ports.seekTask(handle.task); err != nil {
		return outcome, err
	}
	entries, err := handle.task.ReadDir(-1)
	if err != nil || len(entries) != 1 || entries[0].Name() != ".lock" {
		return outcome, fmt.Errorf("operation shell is not lock-only")
	}
	listed, err := ports.list(ctx, ListOptions{ProjectsRoot: root, Task: options.Task, Workers: 1})
	if err != nil {
		return outcome, err
	}
	if len(listed.Results) != 0 || len(listed.Diagnostics) != 0 {
		return outcome, fmt.Errorf("operation still has worktree candidates or incomplete discovery")
	}
	hasBacklog, err := ports.hasBacklog(ctx, resolution, root, options.Task, recovery.WorktreesRoot)
	if err != nil || hasBacklog {
		return outcome, fmt.Errorf("operation still has cleanup backlog or uncertain records: %v", err)
	}
	canonical := filepath.Join(root, filepath.FromSlash(receipt.Repository))
	targetSHA, err := ports.fetchTarget(ctx, canonical, receipt.Target)
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
	if err := ports.validateLock(handle); err != nil {
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
	auditBytes, err := ports.marshalAudit(audit)
	if err != nil {
		return outcome, err
	}
	auditFile, err := ports.createAudit(auditRoot, options.Task+"-*.json")
	if err != nil {
		return outcome, err
	}
	outcome.AuditPath = auditFile.Name()
	if _, err := ports.writeAudit(auditFile, append(auditBytes, '\n')); err != nil {
		_ = auditFile.Close()
		return outcome, err
	}
	if err := ports.syncAudit(auditFile); err != nil {
		_ = auditFile.Close()
		return outcome, err
	}
	if err := ports.closeAudit(auditFile); err != nil {
		return outcome, err
	}
	if err := ports.releaseLock(handle); err != nil {
		return outcome, err
	}
	purgeTerminalTaskLockDebris(handle)
	if !ports.removeShell(handle) {
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
