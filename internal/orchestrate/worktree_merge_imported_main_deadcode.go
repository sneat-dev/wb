package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/quality"
)

const worktreeMergeDeadcodeCommand = "go run ./cmd/wb deadcode"

// WorktreeMergeImportedMainDeadcode binds the one imported main parent to the
// candidate and the exact target whose validation it may supplement.
type WorktreeMergeImportedMainDeadcode struct {
	CandidateSHA  string                     `json:"candidate_sha"`
	TargetSHA     string                     `json:"target_sha"`
	MergeSHA      string                     `json:"merge_sha"`
	ImportedSHA   string                     `json:"imported_sha"`
	OriginMainSHA string                     `json:"origin_main_sha"`
	Validation    quality.VerificationReport `json:"validation"`
}

func worktreeMergeImportedMainDeadcode(ctx context.Context, receipt *WorktreeMergeReceipt, timeout time.Duration, retry int, checkTimeout time.Duration) (*WorktreeMergeImportedMainDeadcode, error) {
	path := receipt.Candidate.Worktree
	mergeSHA, importedSHA, found, err := worktreeMergeImportedMainGraph(ctx, path, receipt.Candidate.SHA, receipt.TargetSHA)
	if err != nil || !found {
		return nil, err
	}
	main, _, err := runCommand(ctx, timeout, retry, path, "git", "ls-remote", "--heads", "origin", "refs/heads/main")
	if err != nil {
		return nil, fmt.Errorf("attest authoritative origin/main: %w", err)
	}
	fields := strings.Fields(main)
	if len(fields) != 2 || fields[1] != "refs/heads/main" {
		return nil, fmt.Errorf("origin/main lookup returned malformed result %q", strings.TrimSpace(main))
	}
	if fields[0] != importedSHA {
		return nil, fmt.Errorf("imported main parent %s does not match authoritative origin/main %s", importedSHA, fields[0])
	}
	validation, err := verifyWorktreeMergeImportedMainDeadcode(ctx, receipt.Repository, path, importedSHA, timeout, retry, checkTimeout)
	if err != nil {
		return nil, err
	}
	return &WorktreeMergeImportedMainDeadcode{CandidateSHA: receipt.Candidate.SHA, TargetSHA: receipt.TargetSHA, MergeSHA: mergeSHA, ImportedSHA: importedSHA, OriginMainSHA: fields[0], Validation: validation}, nil
}

// worktreeMergeImportedMainGraph accepts only a linear candidate suffix above
// one exact merge whose first parent is the receipt target and whose second
// parent is the imported main commit.
func worktreeMergeImportedMainGraph(ctx context.Context, repository, candidate, target string) (mergeSHA, importedSHA string, found bool, err error) {
	current := candidate
	mergeCount := 0
	for steps := 0; steps < 256 && current != target; steps++ {
		out, _, runErr := runCommand(ctx, 0, 0, repository, "git", "rev-list", "--parents", "-n", "1", current)
		if runErr != nil {
			return "", "", false, fmt.Errorf("inspect candidate ancestry at %s: %w", current, runErr)
		}
		parents := strings.Fields(out)
		if len(parents) < 2 || parents[0] != current {
			return "", "", false, fmt.Errorf("candidate first-parent path does not reach receipt target %s", target)
		}
		if len(parents) == 3 {
			mergeCount++
			if mergeCount != 1 || parents[1] != target {
				return "", "", false, errors.New("candidate history has an unexpected merge or imported merge first parent")
			}
			mergeSHA, importedSHA = current, parents[2]
		} else if len(parents) != 2 {
			return "", "", false, errors.New("candidate history has an octopus merge")
		}
		current = parents[1]
	}
	if current != target {
		return "", "", false, errors.New("candidate first-parent path exceeds limit or does not reach receipt target")
	}
	if mergeCount == 0 {
		return "", "", false, nil
	}
	return mergeSHA, importedSHA, true, nil
}

func verifyWorktreeMergeImportedMainDeadcode(ctx context.Context, repository, candidateWorktree, importedSHA string, timeout time.Duration, retry int, checkTimeout time.Duration) (quality.VerificationReport, error) {
	if checkTimeout <= 0 || checkTimeout > 2*time.Minute {
		checkTimeout = 2 * time.Minute
	}
	temporary, err := os.MkdirTemp("", "wb-worktree-merge-main-deadcode-*")
	if err != nil {
		return quality.VerificationReport{}, err
	}
	defer os.RemoveAll(temporary)
	archivePath := filepath.Join(temporary, "main.tar")
	if _, _, err := runCommand(ctx, timeout, retry, candidateWorktree, "git", "archive", "--format=tar", "--output="+archivePath, importedSHA); err != nil {
		return quality.VerificationReport{}, fmt.Errorf("archive imported main %s: %w", importedSHA, err)
	}
	snapshot := filepath.Join(temporary, "tree")
	if err := extractWorktreeMergeArchive(archivePath, snapshot); err != nil {
		return quality.VerificationReport{}, err
	}
	if err := configureWorktreeMergeBaselineRemote(ctx, candidateWorktree, snapshot, timeout, retry); err != nil {
		return quality.VerificationReport{}, err
	}
	runOptions, err := quality.RepositoryRunOptions(snapshot, quality.RunOptions{Timeout: timeout, Retry: retry, CheckTimeout: checkTimeout})
	if err != nil {
		return quality.VerificationReport{}, fmt.Errorf("load imported main quality policy: %w", err)
	}
	configured := false
	for _, command := range runOptions.GoLintCommands {
		if strings.Join(command, " ") == worktreeMergeDeadcodeCommand {
			configured = true
			break
		}
	}
	if !configured {
		return quality.VerificationReport{}, errors.New("exact deadcode command is not configured on imported main")
	}
	runOptions.GoLintCommands = [][]string{{"go", "run", "./cmd/wb", "deadcode"}}
	report := quality.VerifyWithOptions(ctx, repository, snapshot, []quality.Check{quality.CheckLint}, runOptions)
	report.Path, report.Revision, report.WorkspaceClean = "git:"+importedSHA, importedSHA, true
	if !validImportedMainDeadcodeReport(report) {
		return report, errors.New("imported main deadcode validation is incomplete or did not run the exact configured command")
	}
	return report, nil
}

func validImportedMainDeadcodeReport(report quality.VerificationReport) bool {
	count := 0
	for _, entry := range report.Results {
		if entry.Language == "go" && entry.Check == quality.CheckLint && entry.Command == worktreeMergeDeadcodeCommand {
			count++
			if entry.Status == quality.StatusPassed {
				continue
			}
			if entry.Status != quality.StatusFailed || !entry.Deadcode.Valid() {
				return false
			}
		}
	}
	return count == 1
}

func importedMainDeadcodeIdentities(report quality.VerificationReport, check quality.Check, command, module string) (map[string]bool, bool) {
	return worktreeMergeDeadcodeIdentitySet(report.Results, "go", check, command, module)
}

func recheckWorktreeMergeImportedMainDeadcode(ctx context.Context, receipt WorktreeMergeReceipt, timeout time.Duration, retry int, checkTimeout time.Duration) error {
	evidence := receipt.ImportedMainDeadcode
	if evidence == nil {
		return nil
	}
	if evidence.CandidateSHA != receipt.Candidate.SHA || evidence.TargetSHA != receipt.TargetSHA || evidence.OriginMainSHA != evidence.ImportedSHA || evidence.Validation.Revision != evidence.ImportedSHA || !validImportedMainDeadcodeReport(evidence.Validation) {
		return errors.New("imported main deadcode evidence does not bind the exact candidate, target and imported main revision")
	}
	ioTimeout := timeout
	if ioTimeout <= 0 || ioTimeout > 30*time.Second {
		ioTimeout = 30 * time.Second
	}
	remoteCtx, cancel := context.WithTimeout(ctx, ioTimeout)
	defer cancel()
	merge, imported, found, err := worktreeMergeImportedMainGraph(remoteCtx, receipt.Candidate.Worktree, receipt.Candidate.SHA, receipt.TargetSHA)
	if err != nil {
		return err
	}
	if !found || merge != evidence.MergeSHA || imported != evidence.ImportedSHA {
		return errors.New("candidate merge ancestry no longer matches imported main deadcode evidence")
	}
	if receiptCheckTimeout, _ := receiptWorktreeMergeValidationTimeouts(receipt); receiptCheckTimeout > 0 {
		checkTimeout = receiptCheckTimeout
	}
	if err := revalidateImportedMainDeadcodeEvidence(ctx, receipt, evidence, timeout, retry, checkTimeout); err != nil {
		return err
	}
	remote, _, err := runCommand(remoteCtx, ioTimeout, retry, receipt.Candidate.Worktree, "git", "ls-remote", "--heads", "origin", "refs/heads/main")
	if err != nil {
		return fmt.Errorf("recheck authoritative origin/main: %w", err)
	}
	fields := strings.Fields(remote)
	if len(fields) != 2 || fields[0] != evidence.OriginMainSHA || fields[1] != "refs/heads/main" {
		return errors.New("authoritative origin/main changed since imported parent validation")
	}
	return nil
}

func revalidateImportedMainDeadcodeEvidence(ctx context.Context, receipt WorktreeMergeReceipt, evidence *WorktreeMergeImportedMainDeadcode, timeout time.Duration, retry int, checkTimeout time.Duration) error {
	validation, err := verifyWorktreeMergeImportedMainDeadcode(ctx, receipt.Repository, receipt.Candidate.Worktree, evidence.ImportedSHA, timeout, retry, checkTimeout)
	if err != nil {
		return err
	}
	if !sameImportedMainDeadcodeEvidence(evidence.Validation, validation) {
		return errors.New("imported main deadcode validation changed since candidate validation")
	}
	return nil
}

func sameImportedMainDeadcodeEvidence(previous, current quality.VerificationReport) bool {
	find := func(report quality.VerificationReport) (quality.VerificationEntry, bool) {
		var match quality.VerificationEntry
		count := 0
		for _, entry := range report.Results {
			if entry.Language == "go" && entry.Check == quality.CheckLint && entry.Command == worktreeMergeDeadcodeCommand {
				match, count = entry, count+1
			}
		}
		return match, count == 1
	}
	old, oldOK := find(previous)
	now, nowOK := find(current)
	if !oldOK || !nowOK || old.Module != now.Module || old.Status != now.Status {
		return false
	}
	if old.Status == quality.StatusPassed {
		return old.Deadcode == nil && now.Deadcode == nil
	}
	if old.Status != quality.StatusFailed || !old.Deadcode.Valid() || !now.Deadcode.Valid() || old.Deadcode.Count != now.Deadcode.Count {
		return false
	}
	identities := make(map[string]bool, len(old.Deadcode.Identities))
	for _, identity := range old.Deadcode.Identities {
		identities[identity] = true
	}
	for _, identity := range now.Deadcode.Identities {
		if !identities[identity] {
			return false
		}
		delete(identities, identity)
	}
	return len(identities) == 0
}
