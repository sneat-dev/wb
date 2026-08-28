package worktrees

import (
	"context"
	"fmt"
	"strings"

	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionpark"
)

// CaptureParkedSessionWorktree binds one parked member to retained Git
// directories and the source session's exact active Work Log claim. It makes
// no Git mutation and never pushes; the remote query only observes whether the
// captured commit is already the exact branch tip.
func CaptureParkedSessionWorktree(ctx context.Context, projectsRoot string, listed ListResult, source session.Record) (sessionpark.Worktree, error) {
	var snapshot sessionpark.Worktree
	if err := validateSourceSession(source); err != nil {
		return snapshot, err
	}
	guard, err := Guard(ctx, listed.WorktreeDir, GuardOptions{ProjectsRoot: projectsRoot, Admission: AdmissionEnforce})
	if err != nil {
		return snapshot, fmt.Errorf("park requires a managed source worktree: %w", err)
	}
	if guard.Kind != "linked" || guard.Transient {
		return snapshot, fmt.Errorf("park requires a named linked worktree at %s", listed.WorktreeDir)
	}
	if listed.CanonicalDir != "" && listed.CanonicalDir != guard.CanonicalDir ||
		listed.WorktreesRoot != "" && listed.WorktreesRoot != guard.WorktreesRoot ||
		listed.Branch != "" && listed.Branch != guard.Branch {
		return snapshot, fmt.Errorf("parked worktree inventory changed before capture at %s", listed.WorktreeDir)
	}
	canonical, err := openCanonicalRepository(guard.CanonicalDir)
	if err != nil {
		return snapshot, fmt.Errorf("open parked canonical repository: %w", err)
	}
	defer canonical.close()
	worktree, err := openAdoptedCleanupWorktree(guard.Path)
	if err != nil {
		return snapshot, fmt.Errorf("hold parked source worktree: %w", err)
	}
	defer worktree.close()

	queryWorktree := func(arguments ...string) (string, error) {
		raw, queryErr := runSecureRenameGitBytesWithHeldWorktree(
			ctx, guard.CanonicalDir, guard.WorktreesRoot, guard.Path, worktree.worktree, arguments...,
		)
		if queryErr != nil {
			return "", queryErr
		}
		return strings.TrimSpace(string(raw)), nil
	}
	branch, err := queryWorktree("symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || branch == "" || branch != guard.Branch {
		return snapshot, fmt.Errorf("parked source named branch changed during capture")
	}
	head, err := queryWorktree("rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || !isGitObjectID(head) {
		return snapshot, fmt.Errorf("resolve exact parked source commit: %w", err)
	}
	status, err := queryWorktree("status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return snapshot, fmt.Errorf("inspect parked source status: %w", err)
	}
	workLogReference, ownerEventID, err := ParkedSessionWorkLogSnapshot(projectsRoot, guard.Path, source)
	if err != nil {
		return snapshot, err
	}

	fetchValue, err := readCanonicalOriginRemote(ctx, canonical, false)
	if err != nil {
		return snapshot, fmt.Errorf("parked source has no usable origin fetch remote: %w", err)
	}
	pushValue, err := readCanonicalOriginRemote(ctx, canonical, true)
	if err != nil {
		return snapshot, fmt.Errorf("parked source has no usable origin push remote: %w", err)
	}
	fetchRemote, err := gitremote.Parse(fetchValue)
	if err != nil {
		return snapshot, fmt.Errorf("parked source origin fetch remote is unsafe: %w", err)
	}
	pushRemote, err := gitremote.Parse(pushValue)
	if err != nil {
		return snapshot, fmt.Errorf("parked source origin push remote is unsafe: %w", err)
	}
	if !fetchRemote.Identity.Equal(pushRemote.Identity) {
		return snapshot, fmt.Errorf("parked source origin fetch and push remotes identify different repositories")
	}
	if listed.Repository != "" && listed.Repository != fetchRemote.Identity.Repository {
		return snapshot, fmt.Errorf("parked source repository identity changed before capture")
	}
	remoteHead, err := parkedRemoteBranchTip(ctx, canonical, fetchRemote.Raw, branch)
	if err != nil {
		return snapshot, fmt.Errorf("observe parked source remote branch: %w", err)
	}

	if err := canonical.validate(); err != nil {
		return snapshot, fmt.Errorf("parked canonical repository changed during capture: %w", err)
	}
	if err := worktree.validate(); err != nil {
		return snapshot, fmt.Errorf("parked source worktree changed during capture: %w", err)
	}
	branchAfter, branchErr := queryWorktree("symbolic-ref", "--quiet", "--short", "HEAD")
	headAfter, headErr := queryWorktree("rev-parse", "--verify", "HEAD^{commit}")
	statusAfter, statusErr := queryWorktree("status", "--porcelain=v1", "--untracked-files=all")
	fetchAfter, fetchErr := readCanonicalOriginRemote(ctx, canonical, false)
	pushAfter, pushErr := readCanonicalOriginRemote(ctx, canonical, true)
	referenceAfter, ownerAfter, referenceErr := ParkedSessionWorkLogSnapshot(projectsRoot, guard.Path, source)
	if branchErr != nil || headErr != nil || statusErr != nil || fetchErr != nil || pushErr != nil || referenceErr != nil ||
		branchAfter != branch || headAfter != head || statusAfter != status || fetchAfter != fetchValue || pushAfter != pushValue ||
		referenceAfter != workLogReference || ownerAfter != ownerEventID {
		return snapshot, fmt.Errorf("parked source Git or Work Log authority changed during capture")
	}

	snapshot = sessionpark.Worktree{
		Repository: fetchRemote.Identity.Repository, RepositoryRemote: fetchRemote.Raw,
		CanonicalDir: guard.CanonicalDir, WorktreeDir: guard.Path, WorktreesRoot: guard.WorktreesRoot,
		Branch: branch, Head: head, Dirty: status != "", RemoteHead: remoteHead,
		WorkLogReference: workLogReference,
		OwnerEventID:     ownerEventID,
		Status:           fmt.Sprintf("clean=%t head=%s remote=%s", status == "", head, remoteHead),
	}
	return snapshot, nil
}

func parkedRemoteBranchTip(ctx context.Context, canonical *canonicalRepository, remote, branch string) (string, error) {
	raw, err := gitCanonicalBytes(ctx, canonical, "ls-remote", "--heads", "--", remote, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(raw))
	if line == "" {
		return "", nil
	}
	fields := strings.Fields(line)
	if len(fields) != 2 || !isGitObjectID(fields[0]) || fields[1] != "refs/heads/"+branch {
		return "", fmt.Errorf("remote branch response was not one exact branch tip")
	}
	return fields[0], nil
}
