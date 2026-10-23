package worktreeretire

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/worktreebranches"
	"github.com/sneat-dev/wb/internal/worktreeproof"
	"github.com/sneat-dev/wb/internal/worktreesecure"
)

// Transaction is the durable retirement receipt. It contains identities and
// hashes; private prompt and journal bytes belong only in the archive.
type Transaction struct {
	Version           int       `json:"version"`
	Task              string    `json:"task"`
	Repository        string    `json:"repository"`
	ArchiveRepository string    `json:"archive_repository"`
	Worktree          string    `json:"worktree"`
	Canonical         string    `json:"canonical"`
	WorktreesRoot     string    `json:"worktrees_root"`
	Local             bool      `json:"local"`
	Branch            string    `json:"branch"`
	Preserve          string    `json:"preserve,omitempty"`
	OriginalRemoteSHA string    `json:"original_remote_sha,omitempty"`
	DeleteIntentSHA   string    `json:"delete_intent_sha,omitempty"`
	SourceSHA         string    `json:"source_sha"`
	IntentParentSHA   string    `json:"intent_parent_sha,omitempty"`
	IntentTreeSHA     string    `json:"intent_tree_sha,omitempty"`
	IntentMessage     string    `json:"intent_message,omitempty"`
	IntentAt          time.Time `json:"intent_at,omitempty"`
	RetiredRef        string    `json:"retired_ref"`
	ArchiveRef        string    `json:"archive_ref"`
	ArchiveSHA        string    `json:"archive_sha,omitempty"`
	ClaimID           string    `json:"claim_id"`
	EffortID          string    `json:"effort_id"`
	RunID             string    `json:"run_id"`
	Phase             string    `json:"phase"`
	ReportPath        string    `json:"report_path,omitempty"`
}

func ArchiveRef(result Transaction) string {
	_, repository, _ := strings.Cut(result.Repository, "/")
	return "retired/" + repository + "/" + strings.TrimPrefix(result.RetiredRef, "retired/")
}

func PreserveMode(result Transaction) string {
	if result.Preserve == "" {
		return "branch"
	}
	return result.Preserve
}

func SourceRef(result Transaction) string {
	if PreserveMode(result) == "tag" {
		return "refs/tags/" + result.RetiredRef
	}
	return "refs/heads/" + result.RetiredRef
}

func ReportPath(home string, result Transaction) string {
	owner, repository, _ := strings.Cut(result.Repository, "/")
	return filepath.Join(home, "reports", "worktree-retire", result.Task, owner+"-"+repository+".json")
}

func DeletionProofRef(result Transaction) string {
	return "refs/tags/wb-retirement-deleted/" + strings.TrimPrefix(result.RetiredRef, "retired/")
}

func ReadReport(path string) (Transaction, error) {
	return readReportWithOps(path, reportReadOps{stat: (*os.File).Stat, read: io.ReadAll})
}

type reportReadOps struct {
	stat func(*os.File) (os.FileInfo, error)
	read func(io.Reader) ([]byte, error)
}

func readReportWithOps(path string, ops reportReadOps) (Transaction, error) {
	var result Transaction
	parent, err := worktreesecure.OpenAbsoluteDirectoryNoFollow(filepath.Dir(path), false)
	if err != nil {
		return result, err
	}
	defer func() { _ = parent.Close() }()
	fd, err := unix.Openat(int(parent.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return result, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer func() { _ = file.Close() }()
	info, err := ops.stat(file)
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return result, fmt.Errorf("invalid retirement receipt file")
	}
	body, err := ops.read(file)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return result, err
	}
	if result.Version != 1 || !worktreeproof.IsGitObjectID(result.SourceSHA) || (result.Preserve != "" && result.Preserve != "branch" && result.Preserve != "tag") {
		return result, fmt.Errorf("invalid retirement receipt")
	}
	if result.DeleteIntentSHA != "" && (result.DeleteIntentSHA != result.OriginalRemoteSHA || !worktreeproof.IsGitObjectID(result.DeleteIntentSHA)) {
		return result, fmt.Errorf("invalid retirement original-ref deletion intent")
	}
	if (result.Phase == "original_deleted" || result.Phase == "complete") && result.OriginalRemoteSHA != "" && result.DeleteIntentSHA != result.OriginalRemoteSHA {
		return result, fmt.Errorf("retirement deletion receipt has no durable intent")
	}
	if result.Phase == "commit_intent" {
		if result.SourceSHA != result.IntentParentSHA || !worktreeproof.IsGitObjectID(result.IntentTreeSHA) || result.IntentMessage == "" || result.IntentAt.IsZero() || result.RetiredRef != "" || result.ArchiveRef != "" || result.DeleteIntentSHA != "" {
			return result, fmt.Errorf("invalid retirement commit intent")
		}
		return result, nil
	}
	if !strings.HasPrefix(result.RetiredRef, "retired/") || result.ArchiveRef != ArchiveRef(result) {
		return result, fmt.Errorf("invalid retirement receipt")
	}
	stem := strings.TrimPrefix(result.RetiredRef, "retired/")
	if len(stem) < 9 {
		return result, fmt.Errorf("invalid retired ref date")
	}
	date, err := time.Parse("20060102", stem[:8])
	if err != nil || result.RetiredRef != worktreebranches.RetiredBranchDestination(date, result.Branch, result.SourceSHA) {
		return result, fmt.Errorf("retirement receipt ref does not bind branch and commit")
	}
	return result, nil
}

func WriteReport(result Transaction) error { return WriteReportInjected(result, nil) }

func WriteReportInjected(result Transaction, inj *filewrite.Injector) error {
	if err := os.MkdirAll(filepath.Dir(result.ReportPath), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := filewrite.CreateTemp(filepath.Dir(result.ReportPath), ".retire-*.tmp", inj)
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := filewrite.ChmodFile(temporary, 0o600, temporaryName, inj); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := filewrite.Write(temporary, append(body, '\n'), temporaryName, inj); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := filewrite.Sync(temporary, temporaryName, inj); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := filewrite.Close(temporary, temporaryName, inj); err != nil {
		return err
	}
	return filewrite.Rename(temporaryName, result.ReportPath, inj)
}

// TransactionPorts confines remote mutation to the facade's held canonical
// repository and secure Git helper. The leaf decides the lease and proof rules.
type TransactionPorts struct {
	RemoteSHA        func(context.Context, string, string, string) (string, error)
	PublishSourceRef func(context.Context, Transaction, string) error
	DeleteAndTag     func(context.Context, Transaction, string, string) error
}

func PublishSource(ctx context.Context, result *Transaction, ports TransactionPorts) error {
	ref := SourceRef(*result)
	current, err := ports.RemoteSHA(ctx, result.Canonical, "origin", ref)
	if err != nil {
		return err
	}
	if current != "" && current != result.SourceSHA {
		return fmt.Errorf("retired remote ref has conflicting commit")
	}
	if current == "" {
		if err := ports.PublishSourceRef(ctx, *result, ref); err != nil {
			return fmt.Errorf("publish retired source ref: %w", err)
		}
	}
	verified, err := ports.RemoteSHA(ctx, result.Canonical, "origin", ref)
	if err != nil || verified != result.SourceSHA {
		return fmt.Errorf("retired source ref verification failed: %w", err)
	}
	result.Phase = "source_published"
	return nil
}

func VerifyReceipts(ctx context.Context, canonical, archiveRemote string, result Transaction, ports TransactionPorts) error {
	retired, err := ports.RemoteSHA(ctx, canonical, "origin", SourceRef(result))
	if err != nil || retired != result.SourceSHA {
		return fmt.Errorf("retired source receipt changed: %w", err)
	}
	archive, err := ports.RemoteSHA(ctx, canonical, archiveRemote, "refs/heads/"+result.ArchiveRef)
	if err != nil || archive != result.ArchiveSHA {
		return fmt.Errorf("private archive receipt changed: %w", err)
	}
	return nil
}

func DeleteOriginal(ctx context.Context, result *Transaction, afterPhase func(string) error, ports TransactionPorts) error {
	ref := "refs/heads/" + result.Branch
	current, err := ports.RemoteSHA(ctx, result.Canonical, "origin", ref)
	if err != nil {
		return err
	}
	if result.OriginalRemoteSHA != "" && result.DeleteIntentSHA != result.OriginalRemoteSHA {
		return fmt.Errorf("original remote deletion has no durable exact-SHA intent")
	}
	proofRef := DeletionProofRef(*result)
	proof, err := ports.RemoteSHA(ctx, result.Canonical, "origin", proofRef)
	if err != nil {
		return err
	}
	switch {
	case result.OriginalRemoteSHA == "" && current == "" && proof == "":
		// A source branch that never existed remotely needs no delete proof.
	case current == result.OriginalRemoteSHA && current != "" && proof == "":
		if err := ports.DeleteAndTag(ctx, *result, ref, proofRef); err != nil {
			return fmt.Errorf("delete original remote branch with exact lease and atomic proof: %w", err)
		}
	case current == "" && result.OriginalRemoteSHA != "" && proof == result.SourceSHA:
		// A retry can prove that WB's atomic delete created this exact marker.
	default:
		return fmt.Errorf("original remote branch or deletion proof changed before exact-lease deletion")
	}
	verified, err := ports.RemoteSHA(ctx, result.Canonical, "origin", ref)
	if err != nil || verified != "" {
		return fmt.Errorf("original branch deletion verification failed: %w", err)
	}
	if result.OriginalRemoteSHA != "" {
		verifiedProof, err := ports.RemoteSHA(ctx, result.Canonical, "origin", proofRef)
		if err != nil || verifiedProof != result.SourceSHA {
			return fmt.Errorf("atomic original deletion proof verification failed: %w", err)
		}
	}
	if current != "" && afterPhase != nil {
		if err := afterPhase("original_delete_pushed"); err != nil {
			return err
		}
	}
	result.Phase = "original_deleted"
	return nil
}
