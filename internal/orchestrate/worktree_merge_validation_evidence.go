package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/runner"
)

// Validation evidence is fingerprinted per invocation. Candidate identities fail
// closed; baseline cache keys retain distinct unresolved and unreadable markers.
type worktreeMergeFingerprintDependencies struct {
	readFile   func(string) ([]byte, error)
	executable func() (string, error)
	lookPath   func(string) (string, error)
	hashFile   func(string) (string, error)
}

func nativeWorktreeMergeFingerprintDependencies() worktreeMergeFingerprintDependencies {
	return worktreeMergeFingerprintDependencies{os.ReadFile, os.Executable, exec.LookPath, fileSHA256}
}

func (deps worktreeMergeFingerprintDependencies) validatorSHA(name string) (string, bool, error) {
	path, err := deps.lookPath(name)
	if err != nil {
		return "", false, err
	}
	digest, err := deps.hashFile(path)
	return digest, true, err
}

func worktreeMergeValidationIdentity(receipt WorktreeMergeReceipt) (WorktreeMergeValidationIdentity, bool) {
	return worktreeMergeValidationIdentityWithDependencies(receipt, nativeWorktreeMergeFingerprintDependencies())
}

func worktreeMergeValidationIdentityWithDependencies(receipt WorktreeMergeReceipt, deps worktreeMergeFingerprintDependencies) (WorktreeMergeValidationIdentity, bool) {
	policyPath := filepath.Join(receipt.Candidate.Worktree, ".wb", "quality.yaml")
	policy, err := deps.readFile(policyPath)
	if errors.Is(err, os.ErrNotExist) {
		policy = []byte("absent")
	} else if err != nil {
		return WorktreeMergeValidationIdentity{}, false
	}
	policyDigest := sha256.Sum256(policy)
	executable, err := deps.executable()
	if err != nil {
		return WorktreeMergeValidationIdentity{}, false
	}
	executableSHA, err := deps.hashFile(executable)
	if err != nil {
		return WorktreeMergeValidationIdentity{}, false
	}
	sourceSHAs := make([]string, len(receipt.Sources))
	for index, source := range receipt.Sources {
		sourceSHAs[index] = source.SHA
	}
	var validators map[string]string
	for _, result := range receipt.Validation.Results {
		fields := strings.Fields(result.Command)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if result.Language != "go" && result.Language != "node" && result.Language != "specscore" {
			continue
		}
		digest, _, err := deps.validatorSHA(name)
		if err != nil {
			return WorktreeMergeValidationIdentity{}, false
		}
		if validators == nil {
			validators = make(map[string]string)
		}
		validators[name] = digest
	}
	return WorktreeMergeValidationIdentity{
		CandidateSHA: receipt.Candidate.SHA, TargetSHA: receipt.TargetSHA,
		SourceSHAs: sourceSHAs, QualityPolicySHA: hex.EncodeToString(policyDigest[:]),
		WBBuild: buildinfo.Version() + "@" + buildinfo.Revision(), WBExecutableSHA: executableSHA, Validators: validators,
	}, true
}

// These boundaries belong to exact-target materialization and its durable
// cache. Tests can fail a stage locally without changing process-wide state.
type worktreeMergeBaselineDependencies struct {
	fingerprint worktreeMergeFingerprintDependencies
	mkdirTemp   func(string, string) (string, error)
	removeAll   func(string) error
	command     func(context.Context, runner.Runner, time.Duration, int, string, string, ...string) (string, int, error)
	extract     func(string, string) error
	remote      func(context.Context, string, string, time.Duration, int) error
	options     func(string, quality.RunOptions) (quality.RunOptions, error)
	key         func(string, string, string, string, []quality.Check, map[string]string, quality.RunOptions) (quality.ValidationCacheKey, error)
	home        func() (string, error)
	load        func(string, quality.ValidationCacheKey) (quality.VerificationReport, bool, error)
	verify      func(context.Context, string, string, []quality.Check, quality.RunOptions) quality.VerificationReport
	save        func(string, quality.ValidationCacheKey, quality.VerificationReport) error
}

func nativeWorktreeMergeBaselineDependencies() worktreeMergeBaselineDependencies {
	return worktreeMergeBaselineDependencies{
		fingerprint: nativeWorktreeMergeFingerprintDependencies(),
		mkdirTemp:   os.MkdirTemp, removeAll: os.RemoveAll, command: runCommand,
		extract: extractWorktreeMergeArchive, remote: configureWorktreeMergeBaselineRemote,
		options: quality.RepositoryRunOptions, key: quality.NewValidationCacheKey,
		home: os.UserHomeDir, load: quality.LoadValidationCache,
		verify: quality.VerifyWithOptions, save: quality.SaveValidationCache,
	}
}

func fileSHA256(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// verifyWorktreeMergeTarget materializes the exact fetched target revision in
// a temporary archive rather than trusting a mutable canonical checkout. This
// keeps the baseline tied to receipt.TargetSHA even while a candidate is being
// rebased for target drift.
func verifyWorktreeMergeTarget(ctx context.Context, repository, repositoryDir, targetSHA string, timeout time.Duration, retry int, checkTimeout, shardAttemptTimeout time.Duration) (quality.VerificationReport, error) {
	return verifyWorktreeMergeTargetChecks(ctx, repository, repositoryDir, targetSHA, timeout, retry, checkTimeout, shardAttemptTimeout,
		[]quality.Check{quality.CheckLint, quality.CheckTest, quality.CheckBuild, quality.CheckSpec})
}

func verifyWorktreeMergeTargetChecks(ctx context.Context, repository, repositoryDir, targetSHA string, timeout time.Duration, retry int, checkTimeout, shardAttemptTimeout time.Duration, checks []quality.Check) (quality.VerificationReport, error) {
	return verifyWorktreeMergeTargetChecksWithDependencies(ctx, repository, repositoryDir, targetSHA, timeout, retry, checkTimeout, shardAttemptTimeout, checks, nativeWorktreeMergeBaselineDependencies())
}

func verifyWorktreeMergeTargetChecksWithDependencies(ctx context.Context, repository, repositoryDir, targetSHA string, timeout time.Duration, retry int, checkTimeout, shardAttemptTimeout time.Duration, checks []quality.Check, deps worktreeMergeBaselineDependencies) (quality.VerificationReport, error) {
	targetSHA = strings.TrimSpace(targetSHA)
	if targetSHA == "" {
		return quality.VerificationReport{}, errors.New("target SHA is required for validation baseline")
	}
	temporary, err := deps.mkdirTemp("", "wb-worktree-merge-target-*")
	if err != nil {
		return quality.VerificationReport{}, fmt.Errorf("create target validation snapshot: %w", err)
	}
	defer func() { _ = deps.removeAll(temporary) }()
	archivePath := filepath.Join(temporary, "target.tar")
	if _, _, err := deps.command(ctx, defaultRunner, timeout, retry, repositoryDir, "git", "archive", "--format=tar", "--output="+archivePath, targetSHA); err != nil {
		return quality.VerificationReport{}, fmt.Errorf("archive target %s: %w", targetSHA, err)
	}
	snapshot := filepath.Join(temporary, "tree")
	if err := deps.extract(archivePath, snapshot); err != nil {
		return quality.VerificationReport{}, fmt.Errorf("materialize target %s: %w", targetSHA, err)
	}
	// Some repository checks, including SpecScore project-host validation,
	// intentionally inspect the checkout's origin remote. An archive has no
	// .git directory, so recreate only that read-only context from the
	// candidate before comparing failure identities. The snapshot remains an
	// exact target tree: no commits, refs, index, or candidate files are used.
	if err := deps.remote(ctx, repositoryDir, snapshot, timeout, retry); err != nil {
		return quality.VerificationReport{}, err
	}
	runOptions, err := deps.options(snapshot, quality.RunOptions{Timeout: timeout, Retry: retry, CheckTimeout: checkTimeout, ShardAttemptTimeout: shardAttemptTimeout})
	if err != nil {
		return quality.VerificationReport{}, fmt.Errorf("load target quality policy: %w", err)
	}
	cacheKey, err := deps.key(repository, targetSHA, snapshot, buildinfo.Revision(), checks, validationCacheValidatorSHAsWithDependencies(checks, deps.fingerprint), runOptions)
	if err != nil {
		return quality.VerificationReport{}, fmt.Errorf("fingerprint target validation baseline: %w", err)
	}
	cacheRoot, err := deps.home()
	if err != nil {
		return quality.VerificationReport{}, fmt.Errorf("resolve WB validation cache: %w", err)
	}
	cacheDir := quality.ValidationCacheDir(filepath.Join(cacheRoot, ".wb"))
	if len(checks) == 1 && checks[0] == quality.CheckLint {
		fullChecks := []quality.Check{quality.CheckLint, quality.CheckTest, quality.CheckBuild, quality.CheckSpec}
		fullKey, keyErr := deps.key(repository, targetSHA, snapshot, buildinfo.Revision(), fullChecks, validationCacheValidatorSHAsWithDependencies(fullChecks, deps.fingerprint), runOptions)
		if keyErr != nil {
			return quality.VerificationReport{}, fmt.Errorf("fingerprint full target validation baseline: %w", keyErr)
		}
		if cached, ok, cacheErr := deps.load(cacheDir, fullKey); cacheErr != nil {
			return quality.VerificationReport{}, fmt.Errorf("read full target validation baseline cache: %w", cacheErr)
		} else if ok {
			return worktreeMergeLintEvidence(cached), nil
		}
	}
	if cached, ok, cacheErr := deps.load(cacheDir, cacheKey); cacheErr != nil {
		return quality.VerificationReport{}, fmt.Errorf("read target validation baseline cache: %w", cacheErr)
	} else if ok {
		return cached, nil
	}
	report := deps.verify(ctx, repository, snapshot, checks, runOptions)
	// The transient snapshot is intentionally removed before this durable
	// receipt is written. The exact revision remains the useful evidence.
	report.Path = "git:" + targetSHA
	report.Revision = targetSHA
	report.WorkspaceClean = true
	if report.Status != quality.StatusSkipped {
		if cacheErr := deps.save(cacheDir, cacheKey, report); cacheErr != nil {
			return quality.VerificationReport{}, fmt.Errorf("save target validation baseline cache: %w", cacheErr)
		}
	}
	return report, nil
}

func worktreeMergeLintEvidence(report quality.VerificationReport) quality.VerificationReport {
	entries := make([]quality.VerificationEntry, 0, len(report.Results))
	report.Status = quality.StatusSkipped
	for _, entry := range report.Results {
		if entry.Check != quality.CheckLint && entry.Check != "install" && entry.Check != "" {
			continue
		}
		entries = append(entries, entry)
		if entry.Status == quality.StatusFailed {
			report.Status = quality.StatusFailed
		} else if report.Status == quality.StatusSkipped && entry.Status == quality.StatusPassed {
			report.Status = quality.StatusPassed
		}
	}
	report.Results = entries
	return report
}

// validationCacheValidatorSHAs prevents a baseline report from being reused
// after an installed external validator changes. The candidate receipt already
// records validator identities; the baseline cache must carry the same guard.
func validationCacheValidatorSHAs(checks []quality.Check) map[string]string {
	return validationCacheValidatorSHAsWithDependencies(checks, nativeWorktreeMergeFingerprintDependencies())
}

func validationCacheValidatorSHAsWithDependencies(checks []quality.Check, deps worktreeMergeFingerprintDependencies) map[string]string {
	for _, check := range checks {
		if check != quality.CheckSpec {
			continue
		}
		digest, resolved, err := deps.validatorSHA("specscore")
		if !resolved {
			return map[string]string{"specscore": "unresolved"}
		}
		if err != nil {
			return map[string]string{"specscore": "unreadable"}
		}
		return map[string]string{"specscore": digest}
	}
	return nil
}
