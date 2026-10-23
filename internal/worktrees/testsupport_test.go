package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/repopath"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktreebranches"
	"github.com/sneat-dev/wb/internal/worktreeclaims"
	"github.com/sneat-dev/wb/internal/worktreejournal"
	"github.com/sneat-dev/wb/internal/worktreelayout"
)

// Helpers kept for tests only: no production caller remains.

// ExpectedRemoteURL derives the remote URL a canonical clone path under
// projectsRoot corresponds to: <root>/github.com/dal-go/dalgo becomes
// https://github.com/dal-go/dalgo.
//
// It is pure path arithmetic. No WB configuration and no repository remote is
// read, so the answer exists before a clone does and cannot be changed by a
// rewritten origin. A path whose first level is not a literal forge hostname
// has no such remote and is refused rather than guessed.
func ExpectedRemoteURL(projectsRoot, canonicalPath string) (string, error) {
	root, err := absoluteProjectsRoot(projectsRoot)
	if err != nil {
		return "", err
	}
	return repopath.RemoteURLForLocalPath(root, canonicalPath)
}

func NewestChangedFileTime(ctx context.Context, worktree string) time.Time {
	return heartbeatPorts().NewestChangedFileTime(ctx, worktree)
}

// ParkedSessionWorkLogReference returns the exact active Work Log claim only
// when it is owned by source. Session parking uses this at its immutable
// snapshot boundary; it must never adopt another session's latest owner.
func ParkedSessionWorkLogReference(projectsRoot, worktree string, source session.Record) (string, error) {
	_, reference, err := inspectSessionMoveWorkLog(projectsRoot, worktree, source)
	return reference, err
}

func QuarantineManifestDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:]), nil
}

func grantOwnerWriteAt(directory *os.File, path string) error {
	return grantOwnerWriteWithIO(directory, path, nativeResidueRemovalIO())
}

func mergeResultTree(ctx context.Context, repository, ours, theirs string) (string, bool, error) {
	return landingReceiptService().MergeResultTree(ctx, repository, ours, theirs)
}

func openJournalComponent(parentFD int, name string, create bool) (int, error) {
	return worktreejournal.OpenJournalComponent(parentFD, name, create)
}

// resolveCanonicalClone returns the canonical clone address for one repository
// coordinate.
//
// A host-qualified coordinate names its path directly. An unqualified
// {owner}/{name} coordinate resolves to an existing clone, preferring the
// literal host level and falling back to the legacy two-level placement. When
// neither exists the legacy path is predicted, because an unqualified
// coordinate carries no host: a host is knowable only from an existing clone or
// from a clone URL, and inventing one would place a repository on a forge
// nobody named.
func resolveCanonicalClone(projectsRoot string, address repopath.Address) (repopath.Address, error) {
	return worktreelayout.ResolveCanonicalClone(projectsRoot, address)
}

func sessionReceiveRepositoryFromRemote(remote string) (string, error) {
	parsed, err := gitremote.Parse(remote)
	if err != nil {
		return "", err
	}
	return parsed.Identity.Repository, nil
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return filewrite.WriteBytesAtomic(filepath.Dir(path), filepath.Base(path), append(content, '\n'), mode)
}

// ValidEffortPath accepts a dot-separated effort path of unbounded depth. Dots
// carry parentage, so an empty component, a leading or trailing dot, and an
// over-long path are all rejected rather than normalized: a silently repaired
// identity is worse than a refused one.
func ValidEffortPath(value string) bool { return worktreeclaims.ValidEffortPath(value) }

// attachParkedLocalSuccessor runs the same composition `wb session resume`
// uses (cmd/wb/session_park.go): hold local custody for the attempt, then
// attach the prepared successor to every member.
func attachParkedLocalSuccessor(ctx context.Context, options ParkedLocalSuccessorOptions) error {
	return WithParkedLocalResumeCustodyForAttempt(ctx, options.ProjectsRoot, options.Bundle, options.AttemptID, func(custody *ParkedLocalCustody) error {
		return custody.Attach(ctx, options.Successor, options.AttemptID, options.AttemptIndex)
	})
}

// exactBranchPullRequests reads both PR roles through the same inventory
// service wiring the branch sweep uses.
func exactBranchPullRequests(ctx context.Context, worktree, repository, branch string) branchPullRequestEvidence {
	return facadePullRequestEvidence(branchInventoryService().ExactBranchPullRequests(ctx, worktreebranches.Repository{Slug: repository, Path: worktree}, branch))
}

// classifyAbsorbedOrUnique implements the absorbed/unique split. absorbed is
// true when git cherry reports zero unique patches, or when the branch tree
// is identical to the target tree; both are patch-id/content evidence only,
// never a landing receipt. See #req:absorbed-is-report-only.
func classifyAbsorbedOrUnique(ctx context.Context, repositoryPath, targetSHA, branchSHA string) (absorbed bool, evidence string, uniqueCount int, err error) {
	return branchInventoryService().ClassifyAbsorbedOrUnique(ctx, repositoryPath, targetSHA, branchSHA)
}

func classifyBranch(ctx context.Context, repository discover.Repo, sweep branchSweepOptions, ref branchRef, scope string, targetSHA, canonicalHEAD string, inUse map[string]string, checkedOut map[string]bool, pullRequestCache map[string][]githubPullRequest) BranchEntry {
	return branchInventoryService().ClassifyBranch(ctx, branchInventoryRepository(repository), sweep.branchInventorySweep(), ref, scope, targetSHA, canonicalHEAD, inUse, checkedOut, pullRequestCache)
}

// classifyLandingReceipt tries to prove, on evidence, that a non-ancestor
// branch's work is in the target: GitHub's commit-to-pull-request index must
// name a merged pull request into the exact base whose merge commit is
// contained in the fetched target, and the local three-way proof must show the
// branch adds nothing to the landing commit or the target. Each failing check
// names itself, and every failure leaves the branch in its patch-evidence
// disposition — a branch never becomes eligible because a check could not be
// run. See #req:receipted-requires-a-proved-landing and
// #req:receipted-is-opt-in-and-fails-closed.
func classifyLandingReceipt(ctx context.Context, repository discover.Repo, ref branchRef, base, targetSHA string, cache map[string][]githubPullRequest) (*PullRequest, string) {
	return branchInventoryService().ClassifyLandingReceipt(ctx, branchInventoryRepository(repository), ref, base, targetSHA, cache)
}

func decorateBranchCommit(ctx context.Context, repositoryPath string, entry *BranchEntry) {
	branchInventoryService().DecorateBranchCommit(ctx, repositoryPath, entry)
}

// decorateBranchCommits reads metadata for one repository's selected refs in
// one Git process. Retired list output has the same author/title fields as the
// normal disposition path without turning a fleet count into one process per
// ref.
func decorateBranchCommits(ctx context.Context, repositoryPath string, entries []BranchEntry) {
	branchInventoryService().DecorateBranchCommits(ctx, repositoryPath, entries)
}

func listLocalRefs(ctx context.Context, repositoryPath string) ([]branchRef, string) {
	return branchInventoryService().ListLocalRefs(ctx, repositoryPath)
}

func listRemoteRefs(ctx context.Context, repositoryPath string) ([]branchRef, string) {
	return branchInventoryService().ListRemoteRefs(ctx, repositoryPath)
}

func listRetiredRemoteRefs(ctx context.Context, repositoryPath string) ([]branchRef, string) {
	return branchInventoryService().ListRetiredRemoteRefs(ctx, repositoryPath)
}

// listRetiredTags keeps remote tag inspection separate from local tags. It
// uses ls-remote for remote scope so branch inventory never writes fetched
// tags into the caller's local tag namespace.
func listRetiredTags(ctx context.Context, repositoryPath string, remote, metadata bool) ([]branchRef, string) {
	return branchInventoryService().ListRetiredTags(ctx, repositoryPath, remote, metadata)
}

func listRefs(ctx context.Context, repositoryPath, refPrefix, namePrefix string) ([]branchRef, string) {
	return branchInventoryService().ListRefs(ctx, repositoryPath, refPrefix, namePrefix)
}

func protectedEvidence(branch, base, canonicalHEAD string) string {
	return worktreebranches.ProtectedEvidence(branch, base, canonicalHEAD)
}

// inspectRepositoryBranches classifies every branch in one repository. A
// fetch failure for the exact target yields unreadable for the whole
// repository rather than aborting the sweep.
func inspectRepositoryBranches(ctx context.Context, repository discover.Repo, sweep branchSweepOptions, inUse map[string]string) ([]BranchEntry, string) {
	return branchInventoryService().InspectRepositoryBranches(ctx, branchInventoryRepository(repository), sweep.branchInventorySweep(), inUse)
}

func inspectRepositoryBranchesWithHeartbeat(
	ctx context.Context,
	repository discover.Repo,
	sweep branchSweepOptions,
	inUse map[string]string,
	index, total int,
	interval time.Duration,
	inspect branchRepositoryInspection,
) ([]BranchEntry, string) {
	adapt := func(ctx context.Context, repo worktreebranches.Repository, _ worktreebranches.InventorySweep, inUse map[string]string) ([]BranchEntry, string) {
		return inspect(ctx, repository, sweep, inUse)
	}
	return branchInventoryService().InspectRepositoryBranchesWithHeartbeat(ctx, branchInventoryRepository(repository), sweep.branchInventorySweep(), inUse, index, total, interval, adapt)
}

func selectBranchRepositories(repositories []discover.Repo, repositorySlug, org string) ([]discover.Repo, error) {
	leaf := make([]worktreebranches.Repository, 0, len(repositories))
	for _, repository := range repositories {
		leaf = append(leaf, branchInventoryRepository(repository))
	}
	selected, err := worktreebranches.SelectBranchRepositories(leaf, repositorySlug, org)
	if err != nil {
		return nil, err
	}
	bySlug := make(map[string]discover.Repo, len(repositories))
	for _, repository := range repositories {
		bySlug[repository.Slug()] = repository
	}
	result := make([]discover.Repo, 0, len(selected))
	for _, repository := range selected {
		result = append(result, bySlug[repository.Slug])
	}
	return result, nil
}

type branchRepositoryInspection func(context.Context, discover.Repo, branchSweepOptions, map[string]string) ([]BranchEntry, string)
