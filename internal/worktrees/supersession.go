package worktrees

import (
	"context"
	"os"
	"strings"

	"github.com/sneat-dev/wb/internal/worktreebranches"
)

// Public receipt DTOs keep their facade identity while branch policy owns
// parsing, validation, and deterministic audit rendering.
type SupersessionReceipt = worktreebranches.SupersessionReceipt
type SupersessionDependencyDelta = worktreebranches.SupersessionDependencyDelta
type SupersessionReplacement = worktreebranches.SupersessionReplacement
type SupersessionResidual = worktreebranches.SupersessionResidual
type SupersessionApproval = worktreebranches.SupersessionApproval

func supersessionEntry(entry ListResult) worktreebranches.SupersessionEntry {
	return worktreebranches.SupersessionEntry{
		Task: entry.Task, Repository: entry.Repository, Branch: entry.Branch,
		Base: entry.Base, HeadSHA: entry.HeadSHA, RemoteTargetSHA: entry.RemoteTargetSHA,
		CanonicalDir: entry.CanonicalDir, WorktreeDir: entry.WorktreeDir,
		OpenPullRequest: entry.OpenPullRequest,
	}
}

func supersessionService() worktreebranches.SupersessionService {
	return worktreebranches.SupersessionService{Ports: worktreebranches.SupersessionPorts{
		Git:         git,
		IsAncestor:  isAncestor,
		ReadReceipt: os.ReadFile,
		ReadCampaignMarker: func(worktree string) (bool, error) {
			manifest, err := ReadManifest(worktree)
			if err != nil {
				return false, err
			}
			return manifest.DependencyCampaign, nil
		},
	}}
}

func supersessionReceiptForEntry(ctx context.Context, path string, entry ListResult) (*SupersessionReceipt, string) {
	return supersessionService().SupersessionReceiptForEntry(ctx, path, supersessionEntry(entry))
}

// The facade alone updates lifecycle state and retains the receipt for the
// terminal claim transaction; the branch leaf only verifies its evidence.
func applySupersessionReceipt(ctx context.Context, path string, entry *ListResult) {
	if strings.TrimSpace(path) == "" {
		return
	}
	receipt, rejection := supersessionReceiptForEntry(ctx, path, *entry)
	if rejection != "" {
		entry.SupersessionRejection = rejection
		entry.SupersededAtOrigin = false
		return
	}
	entry.SupersededAtOrigin = true
	entry.SupersessionReceipt = path
	entry.SupersessionReviewer = receipt.Approval.Actor
	entry.SupersessionReceiptID = receipt.Approval.ReceiptID
	entry.SupersessionRejection = ""
	entry.supersessionReceipt = receipt
}

func validateDependencyDeltas(ctx context.Context, receipt SupersessionReceipt, entry ListResult) string {
	return supersessionService().ValidateDependencyDeltasReason(ctx, receipt, supersessionEntry(entry))
}
func dependencyDeltasForValidation(receipt SupersessionReceipt, entry ListResult) ([]SupersessionDependencyDelta, string) {
	return worktreebranches.DependencyDeltasForValidation(receipt, supersessionEntry(entry))
}
func ValidateDependencyDeltas(ctx context.Context, receipt SupersessionReceipt, entry ListResult) error {
	return supersessionService().ValidateDependencyDeltas(ctx, receipt, supersessionEntry(entry))
}
func validateAuthoritativeSourcePullRequest(receipt SupersessionReceipt, entry ListResult) string {
	return worktreebranches.ValidateAuthoritativeSourcePullRequest(receipt, supersessionEntry(entry))
}
func dependencyCampaignWorktree(ctx context.Context, entry ListResult) bool {
	return supersessionService().DependencyCampaignWorktree(ctx, supersessionEntry(entry))
}
func isDependencyManifestOrImporter(file string) bool {
	return worktreebranches.IsDependencyManifestOrImporter(file)
}
func dependencyLockfile(ctx context.Context, canonical, target string, delta SupersessionDependencyDelta) (string, bool, error) {
	return supersessionService().DependencyLockfile(ctx, canonical, target, delta)
}
func selectorNamesExactPackage(selector, packageName string) bool {
	return worktreebranches.SelectorNamesExactPackage(selector, packageName)
}
func lockfileEntryContainsVersion(ecosystem, lockfilePath, contents, selector, version string) bool {
	return worktreebranches.LockfileEntryContainsVersion(ecosystem, lockfilePath, contents, selector, version)
}
func selectorPackageFromLockfileSelector(selector string) string {
	return worktreebranches.SelectorPackageFromLockfileSelector(selector)
}
func parseLockfileSelector(ecosystem, lockfilePath, selector, packageName string) ([]string, bool) {
	return worktreebranches.ParseLockfileSelector(ecosystem, lockfilePath, selector, packageName)
}
func dependencyVersionSatisfies(ecosystem, candidate, requested string) bool {
	return worktreebranches.DependencyVersionSatisfies(ecosystem, candidate, requested)
}
func normalizeDependencyVersion(value string) string {
	return worktreebranches.NormalizeDependencyVersion(value)
}
func npmRangeAlternativeSatisfies(candidate, requested string) bool {
	return worktreebranches.NpmRangeAlternativeSatisfies(candidate, requested)
}
func npmComparatorSatisfies(candidate, constraint string) bool {
	return worktreebranches.NpmComparatorSatisfies(candidate, constraint)
}
func mustAtoi(value string) int {
	return worktreebranches.MustAtoi(value)
}
func validateDependencyManifest(delta SupersessionDependencyDelta, contents []byte, expectedVersion string, exact bool) string {
	return worktreebranches.ValidateDependencyManifest(delta, contents, expectedVersion, exact)
}
func dependencyManifestValue(delta SupersessionDependencyDelta, contents []byte) (string, bool, error) {
	return worktreebranches.DependencyManifestValue(delta, contents)
}
