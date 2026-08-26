package worktrees

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/wbhome"
)

type parkedSessionCaptureMember struct {
	listed    ListResult
	guard     GuardResult
	canonical *canonicalRepository
	worktree  *cleanupWorktreeHandle
	journal   *os.File
	unlock    func()
	snapshot  sessionpark.Worktree

	branch, head, status, fetchRemote, pushRemote, workLogReference, ownerEventID string
}

// CaptureParkedSessionAggregate retains every member's descriptor identity and
// cooperative local Work Log custody lock as one authority. The persistence
// callback runs while that complete authority remains held; cleanup is always
// the reverse of stable acquisition order.
//
// Git has no process-wide lock for ordinary index/worktree writers. WB therefore
// revalidates exact branch, HEAD, status, remotes, descriptors, claim, and owner
// evidence immediately before persistence, while the journal locks prevent
// cooperating WB custody writers from crossing the capture/persistence gap.
func CaptureParkedSessionAggregate(ctx context.Context, projectsRoot string, listed []ListResult, source session.Record, persist func([]sessionpark.Worktree) error) error {
	if persist == nil {
		return fmt.Errorf("parked session aggregate requires a persistence callback")
	}
	if err := validateSourceSession(source); err != nil {
		return err
	}
	members := make([]parkedSessionCaptureMember, len(listed))
	for index := range listed {
		members[index].listed = listed[index]
	}
	sort.SliceStable(members, func(i, j int) bool {
		if members[i].listed.WorktreeDir != members[j].listed.WorktreeDir {
			return members[i].listed.WorktreeDir < members[j].listed.WorktreeDir
		}
		return members[i].listed.Repository < members[j].listed.Repository
	})
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
		if index > 0 && members[index-1].listed.WorktreeDir == members[index].listed.WorktreeDir {
			return fmt.Errorf("parked session worktree path %q is duplicated", members[index].listed.WorktreeDir)
		}
		if err := members[index].acquire(ctx, projectsRoot); err != nil {
			return fmt.Errorf("capture parked worktree %s: %w", members[index].listed.WorktreeDir, err)
		}
	}
	for index := range members {
		if err := members[index].capture(ctx, projectsRoot, source); err != nil {
			return fmt.Errorf("capture parked worktree %s: %w", members[index].listed.WorktreeDir, err)
		}
	}
	for index := range members {
		if err := members[index].validate(ctx, projectsRoot, source); err != nil {
			return fmt.Errorf("revalidate parked worktree %s: %w", members[index].listed.WorktreeDir, err)
		}
	}
	snapshots := make([]sessionpark.Worktree, len(members))
	for index := range members {
		snapshots[index] = members[index].snapshot
	}
	return persist(snapshots)
}

func (member *parkedSessionCaptureMember) acquire(ctx context.Context, projectsRoot string) error {
	guard, err := Guard(ctx, member.listed.WorktreeDir, GuardOptions{ProjectsRoot: projectsRoot, Admission: AdmissionEnforce})
	if err != nil {
		return fmt.Errorf("park requires a managed source worktree: %w", err)
	}
	if guard.Kind != "linked" || guard.Transient {
		return fmt.Errorf("park requires a named linked worktree at %s", member.listed.WorktreeDir)
	}
	if member.listed.CanonicalDir != "" && member.listed.CanonicalDir != guard.CanonicalDir ||
		member.listed.WorktreesRoot != "" && member.listed.WorktreesRoot != guard.WorktreesRoot ||
		member.listed.Branch != "" && member.listed.Branch != guard.Branch {
		return fmt.Errorf("parked worktree inventory changed before capture at %s", member.listed.WorktreeDir)
	}
	member.guard = guard
	member.canonical, err = openCanonicalRepository(guard.CanonicalDir)
	if err != nil {
		return fmt.Errorf("open parked canonical repository: %w", err)
	}
	member.worktree, err = openAdoptedCleanupWorktree(guard.Path)
	if err != nil {
		return fmt.Errorf("hold parked source worktree: %w", err)
	}
	member.journal, err = openJournalSubdirectory(guard.Path, worklogDirectory, false)
	if err != nil {
		return fmt.Errorf("open parked source Work Log journal: %w", err)
	}
	member.unlock, err = lockLocalWorkLog(member.journal)
	return err
}

func (member *parkedSessionCaptureMember) queryWorktree(ctx context.Context, arguments ...string) (string, error) {
	raw, err := runSecureRenameGitBytesWithHeldWorktree(ctx, member.guard.CanonicalDir, member.guard.WorktreesRoot, member.guard.Path, member.worktree.worktree, arguments...)
	return strings.TrimSpace(string(raw)), err
}

func (member *parkedSessionCaptureMember) capture(ctx context.Context, projectsRoot string, source session.Record) error {
	var err error
	member.branch, err = member.queryWorktree(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || member.branch == "" || member.branch != member.guard.Branch {
		return fmt.Errorf("parked source named branch changed during capture")
	}
	member.head, err = member.queryWorktree(ctx, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || !isGitObjectID(member.head) {
		return fmt.Errorf("resolve exact parked source commit: %w", err)
	}
	member.status, err = member.queryWorktree(ctx, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("inspect parked source status: %w", err)
	}
	member.workLogReference, member.ownerEventID, err = parkedSessionWorkLogSnapshotUnderLock(projectsRoot, member.guard.Path, source, member.journal)
	if err != nil {
		return err
	}
	member.fetchRemote, err = readCanonicalOriginRemote(ctx, member.canonical, false)
	if err != nil {
		return fmt.Errorf("parked source has no usable origin fetch remote: %w", err)
	}
	member.pushRemote, err = readCanonicalOriginRemote(ctx, member.canonical, true)
	if err != nil {
		return fmt.Errorf("parked source has no usable origin push remote: %w", err)
	}
	fetch, err := gitremote.Parse(member.fetchRemote)
	if err != nil {
		return fmt.Errorf("parked source origin fetch remote is unsafe: %w", err)
	}
	push, err := gitremote.Parse(member.pushRemote)
	if err != nil {
		return fmt.Errorf("parked source origin push remote is unsafe: %w", err)
	}
	if !fetch.Identity.Equal(push.Identity) {
		return fmt.Errorf("parked source origin fetch and push remotes identify different repositories")
	}
	if member.listed.Repository != "" && member.listed.Repository != fetch.Identity.Repository {
		return fmt.Errorf("parked source repository identity changed before capture")
	}
	remoteHead, err := parkedRemoteBranchTip(ctx, member.canonical, fetch.Raw, member.branch)
	if err != nil {
		return fmt.Errorf("observe parked source remote branch: %w", err)
	}
	member.snapshot = sessionpark.Worktree{
		Repository: fetch.Identity.Repository, RepositoryRemote: fetch.Raw,
		CanonicalDir: member.guard.CanonicalDir, WorktreeDir: member.guard.Path, WorktreesRoot: member.guard.WorktreesRoot,
		Branch: member.branch, Head: member.head, Dirty: member.status != "", RemoteHead: remoteHead,
		WorkLogReference: member.workLogReference, OwnerEventID: member.ownerEventID,
		Status: fmt.Sprintf("clean=%t head=%s remote=%s", member.status == "", member.head, remoteHead),
	}
	return nil
}

func (member *parkedSessionCaptureMember) validate(ctx context.Context, projectsRoot string, source session.Record) error {
	if err := member.canonical.validate(); err != nil {
		return fmt.Errorf("parked canonical repository changed during capture: %w", err)
	}
	if err := member.worktree.validate(); err != nil {
		return fmt.Errorf("parked source worktree changed during capture: %w", err)
	}
	branch, branchErr := member.queryWorktree(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	head, headErr := member.queryWorktree(ctx, "rev-parse", "--verify", "HEAD^{commit}")
	status, statusErr := member.queryWorktree(ctx, "status", "--porcelain=v1", "--untracked-files=all")
	fetch, fetchErr := readCanonicalOriginRemote(ctx, member.canonical, false)
	push, pushErr := readCanonicalOriginRemote(ctx, member.canonical, true)
	reference, owner, workLogErr := parkedSessionWorkLogSnapshotUnderLock(projectsRoot, member.guard.Path, source, member.journal)
	if branchErr != nil || headErr != nil || statusErr != nil || fetchErr != nil || pushErr != nil || workLogErr != nil ||
		branch != member.branch || head != member.head || status != member.status || fetch != member.fetchRemote || push != member.pushRemote ||
		reference != member.workLogReference || owner != member.ownerEventID {
		return fmt.Errorf("parked source Git, remote, Work Log, or owner evidence changed during aggregate capture")
	}
	return nil
}

func parkedSessionWorkLogSnapshotUnderLock(projectsRoot, worktree string, source session.Record, journal *os.File) (string, string, error) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return "", "", err
	}
	projection, err := readWorkLogProjection(worktree)
	if err != nil || projection.Lifecycle != "active" {
		return "", "", fmt.Errorf("session park requires an active managed Work Log")
	}
	if err := corroborateProjectionWithPrivateClaim(home, worktree, projection); err != nil {
		return "", "", fmt.Errorf("corroborate managed Work Log: %w", err)
	}
	events, _, err := readLocalEventsForAppend(journal)
	if err != nil {
		return "", "", fmt.Errorf("inspect exact parked owner event: %w", err)
	}
	ownerID := ""
	for _, event := range events {
		if event.Type != LocalEventOwner || event.Owner == nil {
			continue
		}
		if event.Owner.PID == source.PID && !event.Owner.At.Before(source.StartedAt) && ownerPIDStatus(event.Owner.PID) == "active" {
			ownerID = event.ID
		} else {
			ownerID = ""
		}
	}
	if ownerID == "" {
		return "", "", fmt.Errorf("registered source session %s is not the exact latest parked owner", source.WBSessionID)
	}
	return fmt.Sprintf("worklog:%s/%s/%s", projection.EffortID, projection.RunID, projection.ClaimID), ownerID, nil
}
