package worktrees

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/wbhome"
)

type ParkedLocalSuccessorOptions struct {
	ProjectsRoot string
	Bundle       sessionpark.Bundle
	Successor    session.Record
	AttemptID    string
	AttemptIndex uint64
}

type parkedLocalMember struct {
	member    sessionpark.Worktree
	guard     GuardResult
	worktree  *cleanupWorktreeHandle
	directory *os.File
	unlock    func()
}

type ParkedLocalCustody struct {
	projectsRoot    string
	bundle          sessionpark.Bundle
	members         []parkedLocalMember
	replayAttemptID string
}

// AttachParkedLocalSuccessor locks every member journal in stable path order,
// validates the complete Git/claim/latest-owner barrier, and only then appends
// the same prepared successor to every member. Explicit event IDs make a
// partial I/O failure repairable by the same launcher attempt.
func AttachParkedLocalSuccessor(ctx context.Context, options ParkedLocalSuccessorOptions) error {
	return withParkedLocalResumeCustody(ctx, options.ProjectsRoot, options.Bundle, options.AttemptID, func(custody *ParkedLocalCustody) error {
		return custody.Attach(ctx, options.Successor, options.AttemptID, options.AttemptIndex)
	})
}

// WithParkedLocalResumeCustody holds every exact worktree descriptor and
// journal lock across local aggregate preparation, launcher readiness, member
// attachment, and source finalization. The callback therefore cannot launch
// from a path or custody projection that changed after the all-member barrier.
func WithParkedLocalResumeCustody(ctx context.Context, projectsRoot string, bundle sessionpark.Bundle, proceed func(*ParkedLocalCustody) error) error {
	return withParkedLocalResumeCustody(ctx, projectsRoot, bundle, "", proceed)
}

func WithParkedLocalResumeCustodyForAttempt(ctx context.Context, projectsRoot string, bundle sessionpark.Bundle, replayAttemptID string, proceed func(*ParkedLocalCustody) error) error {
	return withParkedLocalResumeCustody(ctx, projectsRoot, bundle, replayAttemptID, proceed)
}

func withParkedLocalResumeCustody(ctx context.Context, projectsRoot string, bundle sessionpark.Bundle, replayAttemptID string, proceed func(*ParkedLocalCustody) error) error {
	if proceed == nil {
		return fmt.Errorf("local parked-session resume requires a launch callback")
	}
	members := make([]parkedLocalMember, len(bundle.Worktrees))
	for index, member := range bundle.Worktrees {
		members[index].member = member
	}
	sort.Slice(members, func(i, j int) bool { return members[i].member.WorktreeDir < members[j].member.WorktreeDir })
	custody := &ParkedLocalCustody{projectsRoot: projectsRoot, bundle: bundle, members: members, replayAttemptID: replayAttemptID}
	defer custody.close()
	for index := range custody.members {
		if index > 0 && custody.members[index-1].member.WorktreeDir == custody.members[index].member.WorktreeDir {
			return fmt.Errorf("parked local member path is duplicated")
		}
		if err := custody.acquire(ctx, index); err != nil {
			return fmt.Errorf("retain parked local member %s: %w", custody.members[index].member.WorktreeDir, err)
		}
	}
	if err := custody.validate(ctx, replayAttemptID); err != nil {
		return err
	}
	return proceed(custody)
}

func (custody *ParkedLocalCustody) close() {
	for index := len(custody.members) - 1; index >= 0; index-- {
		member := &custody.members[index]
		if member.unlock != nil {
			member.unlock()
		}
		if member.directory != nil {
			_ = member.directory.Close()
		}
		if member.worktree != nil {
			member.worktree.close()
		}
	}
}

func (custody *ParkedLocalCustody) acquire(ctx context.Context, index int) error {
	prepared := &custody.members[index]
	member := prepared.member
	guard, err := Guard(ctx, member.WorktreeDir, GuardOptions{ProjectsRoot: custody.projectsRoot, Admission: AdmissionEnforce})
	if err != nil {
		return err
	}
	if guard.Kind != "linked" || guard.Transient || guard.Branch != member.Branch || guard.CanonicalDir != member.CanonicalDir ||
		guard.WorktreesRoot != member.WorktreesRoot {
		return fmt.Errorf("managed worktree identity changed since park")
	}
	worktree, err := openAdoptedCleanupWorktree(guard.Path)
	if err != nil {
		return err
	}
	directory, err := openJournalSubdirectory(member.WorktreeDir, worklogDirectory, false)
	if err != nil {
		worktree.close()
		return fmt.Errorf("open parked member Work Log journal: %w", err)
	}
	unlock, err := lockLocalWorkLog(directory)
	if err != nil {
		_ = directory.Close()
		worktree.close()
		return err
	}
	prepared.guard, prepared.worktree, prepared.directory, prepared.unlock = guard, worktree, directory, unlock
	return nil
}

func (custody *ParkedLocalCustody) validate(ctx context.Context, replayAttemptIDs ...string) error {
	for index := range custody.members {
		if err := validateParkedLocalMember(ctx, custody.projectsRoot, custody.bundle, replayAttemptIDs, custody.members[index]); err != nil {
			return fmt.Errorf("preflight parked local member %s: %w", custody.members[index].member.WorktreeDir, err)
		}
	}
	return nil
}

func (custody *ParkedLocalCustody) Attach(ctx context.Context, successor session.Record, attemptID string, attemptIndex uint64) error {
	options := ParkedLocalSuccessorOptions{ProjectsRoot: custody.projectsRoot, Bundle: custody.bundle, Successor: successor, AttemptID: attemptID, AttemptIndex: attemptIndex}
	if options.Successor.PID <= 0 || options.Successor.WBSessionID == "" || options.Successor.StartedAt.IsZero() ||
		options.Successor.PredecessorWBSessionID != options.Bundle.Source.WBSessionID || options.Successor.WBSessionID == options.Bundle.Source.WBSessionID {
		return fmt.Errorf("local parked successor does not descend from the parked source session")
	}
	if options.AttemptID == "" || options.AttemptIndex == 0 {
		return fmt.Errorf("local parked successor requires one stable launcher attempt")
	}
	if err := custody.validate(ctx, custody.replayAttemptID, options.AttemptID); err != nil {
		return err
	}
	bundleRaw, err := sessionpark.EncodeBundle(options.Bundle)
	if err != nil {
		return err
	}
	digest := sessionmove.DigestBytes(bundleRaw)
	for index := range custody.members {
		member := custody.members[index].member
		reference, _ := sessionmove.ParseWorkLogReference(member.WorkLogReference)
		event := LocalWorkLogEvent{
			Version: 1,
			ID:      externalLocalEventID("park-local-owner", digest, options.AttemptID+"-"+member.OwnerEventID),
			Type:    LocalEventOwner,
			At:      options.Successor.StartedAt.UTC(),
			Message: "local parked successor launcher attempt prepared",
			Owner: &OwnerRegistration{
				Agent: options.Successor.Runtime + "/" + options.Successor.WBSessionID, Model: options.Successor.Model,
				Effort: reference.EffortID, PID: options.Successor.PID, WBVersion: buildinfo.Version(),
				Command: "session resume", At: options.Successor.StartedAt.UTC(),
			},
			Extra: map[string]any{
				"parked_session_id": options.Bundle.ParkedSessionID, "source_work_log_reference": member.WorkLogReference,
				"source_owner_event_id": member.OwnerEventID, "successor_wb_session_id": options.Successor.WBSessionID,
				"attempt_id": options.AttemptID, "attempt_index": options.AttemptIndex,
			},
		}
		if _, _, err := appendLocalEventUnderLock(member.WorktreeDir, custody.members[index].directory, event); err != nil {
			return fmt.Errorf("attach local parked successor to %s: %w", member.WorktreeDir, err)
		}
	}
	return nil
}

func validateParkedLocalMember(ctx context.Context, projectsRoot string, bundle sessionpark.Bundle, replayAttemptIDs []string, prepared parkedLocalMember) error {
	member := prepared.member
	if member.OwnerEventID == "" || member.WorkLogReference == "" {
		return fmt.Errorf("parked member lacks exact source Work Log custody evidence; park again")
	}
	guard, worktree := prepared.guard, prepared.worktree
	if worktree == nil {
		return fmt.Errorf("retained worktree descriptor changed since park")
	}
	if err := worktree.validate(); err != nil {
		return fmt.Errorf("retained worktree descriptor changed since park")
	}
	query := func(arguments ...string) (string, error) {
		raw, queryErr := runSecureRenameGitBytesWithHeldWorktree(ctx, guard.CanonicalDir, guard.WorktreesRoot, guard.Path, worktree.worktree, arguments...)
		return strings.TrimSpace(string(raw)), queryErr
	}
	branch, branchErr := query("symbolic-ref", "--quiet", "--short", "HEAD")
	head, headErr := query("rev-parse", "--verify", "HEAD^{commit}")
	if branchErr != nil || headErr != nil || branch != member.Branch || head != member.Head {
		return fmt.Errorf("worktree branch or HEAD changed after park; refusing later-session state")
	}
	projection, err := readWorkLogProjection(member.WorktreeDir)
	if err != nil || "worklog:"+projection.EffortID+"/"+projection.RunID+"/"+projection.ClaimID != member.WorkLogReference || projection.Lifecycle != "active" {
		return fmt.Errorf("active Work Log claim changed after park")
	}
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return err
	}
	if err := corroborateProjectionWithPrivateClaim(home, member.WorktreeDir, projection); err != nil {
		return err
	}
	events, _, err := readLocalEventsForAppend(prepared.directory)
	if err != nil {
		return err
	}
	latestOwner := ""
	for _, event := range events {
		if event.Type == LocalEventOwner && event.Owner != nil {
			latestOwner = event.ID
		}
	}
	bundleRaw, encodeErr := sessionpark.EncodeBundle(bundle)
	if encodeErr != nil {
		return encodeErr
	}
	allowed := latestOwner == member.OwnerEventID
	for _, replayAttemptID := range replayAttemptIDs {
		if replayAttemptID != "" && latestOwner == externalLocalEventID("park-local-owner", sessionmove.DigestBytes(bundleRaw), replayAttemptID+"-"+member.OwnerEventID) {
			allowed = true
		}
	}
	if !allowed {
		return fmt.Errorf("newer session custody exists after park")
	}
	return nil
}
