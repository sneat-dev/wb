package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

const (
	repositoryTransferCleanupReceiptVersion = 1
	repositoryTransferCleanupReceiptType    = "repository.transfer-replacement-cleanup"
	repositoryTransferCleanupPending        = "cleanup_pending"
	repositoryTransferCleanupRetired        = "retired"
	repositoryTransferCleanupRestored       = "restored"
)

type repositoryTransferCleanupReceipt struct {
	Version               int       `json:"version"`
	Type                  string    `json:"type"`
	OperationID           string    `json:"operation_id"`
	SourceRepository      string    `json:"source_repository"`
	DestinationRepository string    `json:"destination_repository"`
	DestinationDir        string    `json:"destination_dir"`
	QuarantineDir         string    `json:"quarantine_dir"`
	RemoteURL             string    `json:"remote_url"`
	DefaultBranch         string    `json:"default_branch"`
	RemoteHead            string    `json:"remote_head"`
	QuarantineDevice      uint64    `json:"quarantine_device"`
	QuarantineInode       uint64    `json:"quarantine_inode"`
	Status                string    `json:"status"`
	RecordedAt            time.Time `json:"recorded_at"`
	CompletedAt           time.Time `json:"completed_at,omitempty"`
}

type RepositoryTransferCleanupOptions struct {
	ProjectsRoot string
	ReceiptPath  string
	Apply        bool
}

type RepositoryTransferCleanupResult struct {
	ReceiptPath   string `json:"receipt_path"`
	QuarantineDir string `json:"quarantine_dir"`
	Eligible      bool   `json:"eligible"`
	Applied       bool   `json:"applied"`
	Outcome       string `json:"outcome,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

func repositoryTransferCleanupDirectory(projectsRoot string) (string, error) {
	home, err := wbhome.EnsureRoot(projectsRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "reports", "repository-transfers"), nil
}

func recordRepositoryTransferCleanupIntent(options RepositoryRelocateOptions, result RepositoryRelocateResult, remoteHead string, held *os.File) (repositoryTransferCleanupReceipt, string, error) {
	device, inode, err := repositoryTransferCleanupIdentity(held)
	if err != nil {
		return repositoryTransferCleanupReceipt{}, "", err
	}
	digest := sha256.Sum256([]byte(result.SourceRepository + "\x00" + result.DestinationRepository + "\x00" + remoteHead + "\x00" + result.RetiredDestinationDir))
	receipt := repositoryTransferCleanupReceipt{
		Version: repositoryTransferCleanupReceiptVersion, Type: repositoryTransferCleanupReceiptType,
		OperationID: hex.EncodeToString(digest[:12]), SourceRepository: result.SourceRepository,
		DestinationRepository: result.DestinationRepository, DestinationDir: result.DestinationDir,
		QuarantineDir: result.RetiredDestinationDir, RemoteURL: result.RemoteURL,
		DefaultBranch: result.DefaultBranch, RemoteHead: remoteHead,
		QuarantineDevice: device, QuarantineInode: inode,
		Status: repositoryTransferCleanupPending, RecordedAt: options.Now().UTC(),
	}
	directoryPath, err := repositoryTransferCleanupDirectory(options.ProjectsRoot)
	if err != nil {
		return repositoryTransferCleanupReceipt{}, "", err
	}
	directory, err := openAbsoluteDirectoryNoFollow(directoryPath, true)
	if err != nil {
		return repositoryTransferCleanupReceipt{}, "", err
	}
	defer func() { _ = directory.Close() }()
	name := receipt.OperationID + "-cleanup-pending.json"
	if err := writeJSONImmutableAt(directory, name, receipt, true); err != nil {
		return repositoryTransferCleanupReceipt{}, "", err
	}
	return receipt, filepath.Join(directoryPath, name), nil
}

func recordRepositoryTransferCleanupCompleted(projectsRoot string, receipt repositoryTransferCleanupReceipt, now time.Time) (string, error) {
	return recordRepositoryTransferCleanupTerminal(projectsRoot, receipt, repositoryTransferCleanupRetired, now)
}

func recordRepositoryTransferCleanupTerminal(projectsRoot string, receipt repositoryTransferCleanupReceipt, status string, now time.Time) (string, error) {
	if status != repositoryTransferCleanupRetired && status != repositoryTransferCleanupRestored {
		return "", fmt.Errorf("invalid repository-transfer cleanup outcome %q", status)
	}
	receipt.Status = status
	receipt.CompletedAt = now
	directoryPath, err := repositoryTransferCleanupDirectory(projectsRoot)
	if err != nil {
		return "", err
	}
	directory, err := openAbsoluteDirectoryNoFollow(directoryPath, false)
	if err != nil {
		return "", err
	}
	defer func() { _ = directory.Close() }()
	name := receipt.OperationID + "-cleanup-" + status + ".json"
	var existing repositoryTransferCleanupReceipt
	if err := readJSONAt(directory, name, &existing); err == nil {
		if existing.OperationID == receipt.OperationID && existing.Status == status && existing.QuarantineDevice == receipt.QuarantineDevice && existing.QuarantineInode == receipt.QuarantineInode {
			return filepath.Join(directoryPath, name), nil
		}
		return "", fmt.Errorf("terminal repository-transfer receipt conflicts with operation %s", receipt.OperationID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := writeJSONImmutableAt(directory, name, receipt, true); err != nil {
		return "", err
	}
	return filepath.Join(directoryPath, name), nil
}

func repositoryTransferCleanupCommand(projectsRoot, receiptPath string) string {
	return "wb --projects-root " + shellQuoteRepositoryTransfer(projectsRoot) + " --non-interactive repo transfer cleanup --receipt " + shellQuoteRepositoryTransfer(receiptPath) + " --apply"
}

func shellQuoteRepositoryTransfer(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// RecoverRepositoryTransferCleanup securely retires the exact quarantined
// replacement clone named by an immutable WB receipt. It refuses a changed
// path or inode and is safe to preview before applying.
func RecoverRepositoryTransferCleanup(ctx context.Context, options RepositoryTransferCleanupOptions) (RepositoryTransferCleanupResult, error) {
	result := RepositoryTransferCleanupResult{ReceiptPath: options.ReceiptPath}
	directoryPath, err := repositoryTransferCleanupDirectory(options.ProjectsRoot)
	if err != nil {
		return result, err
	}
	receiptPath, err := filepath.Abs(options.ReceiptPath)
	if err != nil || filepath.Dir(receiptPath) != filepath.Clean(directoryPath) || !strings.HasSuffix(filepath.Base(receiptPath), "-cleanup-pending.json") {
		return result, fmt.Errorf("cleanup receipt must be a pending repository-transfer receipt below %s", directoryPath)
	}
	directory, err := openAbsoluteDirectoryNoFollow(directoryPath, false)
	if err != nil {
		return result, err
	}
	defer func() { _ = directory.Close() }()
	var receipt repositoryTransferCleanupReceipt
	if err := readJSONAt(directory, filepath.Base(receiptPath), &receipt); err != nil {
		return result, err
	}
	result.QuarantineDir = receipt.QuarantineDir
	if receipt.Version != repositoryTransferCleanupReceiptVersion || receipt.Type != repositoryTransferCleanupReceiptType || receipt.Status != repositoryTransferCleanupPending {
		return result, fmt.Errorf("receipt is not a pending repository-transfer cleanup")
	}
	expectedDestination, err := CanonicalRepositoryPath(options.ProjectsRoot, receipt.DestinationRepository)
	if err != nil || expectedDestination != receipt.DestinationDir || filepath.Dir(receipt.QuarantineDir) != filepath.Dir(expectedDestination) || !strings.HasPrefix(filepath.Base(receipt.QuarantineDir), ".wb-replaced-"+filepath.Base(expectedDestination)+"-") {
		return result, fmt.Errorf("cleanup receipt paths do not match repository identity")
	}
	parent, err := openAbsoluteDirectoryNoFollow(filepath.Dir(receipt.QuarantineDir), false)
	if err != nil {
		return result, err
	}
	defer func() { _ = parent.Close() }()
	held, absent, err := openRepositoryTransferCleanupQuarantine(parent, filepath.Dir(receipt.QuarantineDir), filepath.Base(receipt.QuarantineDir))
	if err != nil {
		return result, err
	}
	if absent {
		outcome, verifyErr := absentRepositoryTransferCleanupOutcome(ctx, receipt)
		if verifyErr != nil {
			result.Reason = verifyErr.Error()
			return result, nil
		}
		result.Eligible = true
		result.Outcome = outcome
		if !options.Apply {
			result.Reason = "replacement quarantine is absent; append " + outcome + " terminal evidence"
			return result, nil
		}
		completed, completeErr := recordRepositoryTransferCleanupTerminal(options.ProjectsRoot, receipt, outcome, time.Now().UTC())
		if completeErr != nil {
			return result, completeErr
		}
		result.ReceiptPath = completed
		result.Applied = true
		return result, nil
	}
	defer func() { _ = held.Close() }()
	if !repositoryTransferCleanupIdentityMatches(held, receipt.QuarantineDevice, receipt.QuarantineInode) {
		result.Reason = "replacement quarantine no longer matches the recorded inode"
		return result, nil
	}
	result.Eligible = true
	result.Outcome = repositoryTransferCleanupRetired
	if !options.Apply {
		return result, nil
	}
	if err := retireRepositoryTransferReplacement(receipt.QuarantineDir, held); err != nil {
		result.Reason = err.Error()
		return result, nil
	}
	completed, err := recordRepositoryTransferCleanupCompleted(options.ProjectsRoot, receipt, time.Now().UTC())
	if err != nil {
		return result, err
	}
	result.ReceiptPath = completed
	result.Applied = true
	return result, nil
}

func absentRepositoryTransferCleanupOutcome(ctx context.Context, receipt repositoryTransferCleanupReceipt) (string, error) {
	if destination, err := openAbsoluteDirectoryNoFollow(receipt.DestinationDir, false); err == nil {
		matches := repositoryTransferCleanupIdentityMatches(destination, receipt.QuarantineDevice, receipt.QuarantineInode)
		_ = destination.Close()
		if matches {
			return repositoryTransferCleanupRestored, nil
		}
	}
	for _, push := range []bool{false, true} {
		urls, err := exactOriginURLs(ctx, receipt.DestinationDir, push)
		if err != nil || len(urls) != 1 || urls[0] != receipt.RemoteURL {
			return "", fmt.Errorf("quarantine is absent but destination origin does not match the completed transfer")
		}
	}
	head, err := remoteDefaultHead(ctx, receipt.DestinationDir, receipt.RemoteURL, receipt.DefaultBranch)
	if err != nil || head != receipt.RemoteHead {
		return "", fmt.Errorf("quarantine is absent but destination remote HEAD does not match the completed transfer")
	}
	fetched, err := git(ctx, receipt.DestinationDir, "rev-parse", "refs/remotes/origin/"+receipt.DefaultBranch)
	if err != nil || fetched != receipt.RemoteHead {
		return "", fmt.Errorf("quarantine is absent but destination fetched default branch does not match the completed transfer")
	}
	return repositoryTransferCleanupRetired, nil
}
