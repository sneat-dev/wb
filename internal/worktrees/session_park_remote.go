package worktrees

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
)

// WithParkedRemoteResumeCustody retains every member's exact Git descriptors
// and Work Log journal lock through the source admission and courier callback.
// No callback runs unless the complete bundle is still clean, pushed, and
// owned by the exact parked source evidence.
func WithParkedRemoteResumeCustody(ctx context.Context, projectsRoot string, bundle sessionpark.Bundle, proceed func() error) error {
	if proceed == nil {
		return fmt.Errorf("remote parked-session resume requires a delivery callback")
	}
	if len(bundle.Worktrees) == 0 || len(bundle.Worktrees) > sessionpark.MaxMembers {
		return fmt.Errorf("remote park resume requires between 1 and %d owned worktrees", sessionpark.MaxMembers)
	}
	members := make([]parkedSessionCaptureMember, len(bundle.Worktrees))
	for index, snapshot := range bundle.Worktrees {
		members[index].snapshot = snapshot
		// Resolve this member by identity -- the same resolver acquire()
		// (internal/worktrees/session_park_local.go) uses for local resume,
		// not the recorded, possibly-stale CanonicalDir/WorktreesRoot/
		// WorktreeDir: the canonical clone through repopath, the checkout
		// through its recorded path or the relocation-receipt chain for its
		// Work Log reference. WorktreesRoot is left unset, exactly like
		// local resume, since it is not part of a member's identity.
		resolvedCanonicalDir, err := resolveParkedMemberCanonicalDir(projectsRoot, snapshot.Repository)
		if err != nil {
			return fmt.Errorf("resolve canonical clone for %s: %w", snapshot.Repository, err)
		}
		resolvedWorktreeDir, err := resolveParkedMemberWorktreeDir(projectsRoot, snapshot)
		if err != nil {
			return err
		}
		members[index].resolvedWorktreeDir = resolvedWorktreeDir
		members[index].listed = ListResult{
			Repository: snapshot.Repository, CanonicalDir: resolvedCanonicalDir, WorktreeDir: resolvedWorktreeDir,
			Branch: snapshot.Branch,
		}
	}
	sort.SliceStable(members, func(i, j int) bool { return members[i].snapshot.WorktreeDir < members[j].snapshot.WorktreeDir })
	defer func() {
		for index := len(members) - 1; index >= 0; index-- {
			member := &members[index]
			if member.unlock != nil {
				member.unlock()
			}
			if member.journal != nil {
				_ = member.journal.Close()
			}
			if member.worktree != nil {
				member.worktree.close()
			}
			if member.canonical != nil {
				member.canonical.close()
			}
		}
	}()
	for index := range members {
		if index > 0 && members[index-1].snapshot.WorktreeDir == members[index].snapshot.WorktreeDir {
			return fmt.Errorf("remote parked-session member path is duplicated")
		}
		if err := members[index].acquire(ctx, projectsRoot); err != nil {
			return fmt.Errorf("retain remote parked-session member %s: %w", members[index].snapshot.WorktreeDir, err)
		}
		if remote := members[index].snapshot.RepositoryRemote; remote != "" {
			if reason := verifyParkedMemberOriginRemote(ctx, members[index].guard.CanonicalDir, remote); reason != "" {
				return fmt.Errorf("retain remote parked-session member %s: managed worktree identity changed since park: %s", members[index].snapshot.WorktreeDir, reason)
			}
		}
	}
	for index := range members {
		if err := validateRemoteParkedMember(ctx, projectsRoot, &members[index]); err != nil {
			return fmt.Errorf("remote parked-session member %s: %w", members[index].snapshot.WorktreeDir, err)
		}
	}
	return proceed()
}

func validateRemoteParkedMember(ctx context.Context, projectsRoot string, prepared *parkedSessionCaptureMember) error {
	member := prepared.snapshot
	if member.Dirty || member.Head == "" || member.RemoteHead == "" || member.Head != member.RemoteHead ||
		member.WorkLogReference == "" || member.OwnerEventID == "" || member.RepositoryRemote == "" {
		return fmt.Errorf("parked evidence is not one clean pushed member with exact source custody; clean, push, and park again")
	}
	if err := prepared.canonical.validate(); err != nil {
		return fmt.Errorf("canonical repository descriptor changed: %w", err)
	}
	if err := prepared.worktree.validate(); err != nil {
		return fmt.Errorf("worktree descriptor changed: %w", err)
	}
	branch, branchErr := prepared.queryWorktree(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	head, headErr := prepared.queryWorktree(ctx, "rev-parse", "--verify", "HEAD^{commit}")
	status, statusErr := prepared.queryWorktree(ctx, "status", "--porcelain=v1", "--untracked-files=all")
	if branchErr != nil || headErr != nil || statusErr != nil || branch != member.Branch || head != member.Head || status != "" {
		return fmt.Errorf("branch, HEAD, or clean status changed after park")
	}
	fetch, fetchErr := readCanonicalOriginRemote(ctx, prepared.canonical, false)
	push, pushErr := readCanonicalOriginRemote(ctx, prepared.canonical, true)
	if fetchErr != nil || pushErr != nil || fetch != member.RepositoryRemote {
		return fmt.Errorf("credential-free origin identity changed after park")
	}
	fetchIdentity, fetchErr := gitremote.Parse(fetch)
	pushIdentity, pushErr := gitremote.Parse(push)
	if fetchErr != nil || pushErr != nil || !fetchIdentity.Identity.Equal(pushIdentity.Identity) ||
		fetchIdentity.Identity.Repository != member.Repository {
		return fmt.Errorf("credential-free origin identity changed after park")
	}
	remoteHead, err := parkedRemoteBranchTip(ctx, prepared.canonical, fetch, member.Branch)
	if err != nil || remoteHead != member.Head {
		return fmt.Errorf("remote branch no longer has the exact parked commit")
	}
	projection, err := readWorkLogProjection(prepared.resolvedWorktreeDir)
	if err != nil || projection.Lifecycle != "active" ||
		"worklog:"+projection.EffortID+"/"+projection.RunID+"/"+projection.ClaimID != member.WorkLogReference {
		return fmt.Errorf("active source Work Log claim changed after park")
	}
	if err := corroborateProjectionAcrossHomes(projectsRoot, prepared.resolvedWorktreeDir, projection); err != nil {
		return fmt.Errorf("corroborate source Work Log claim: %w", err)
	}
	if _, err := sessionmove.ParseWorkLogReference(member.WorkLogReference); err != nil {
		return fmt.Errorf("source Work Log reference is invalid")
	}
	events, _, err := readLocalEventsForAppend(prepared.journal)
	if err != nil {
		return err
	}
	latestOwner := ""
	for _, event := range events {
		if event.Type == LocalEventOwner && event.Owner != nil {
			latestOwner = event.ID
		}
	}
	if strings.TrimSpace(latestOwner) == "" || latestOwner != member.OwnerEventID {
		return fmt.Errorf("newer session custody exists after park")
	}
	return nil
}
