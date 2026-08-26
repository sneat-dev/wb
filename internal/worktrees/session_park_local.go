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
	directory *os.File
	unlock    func()
}

// AttachParkedLocalSuccessor locks every member journal in stable path order,
// validates the complete Git/claim/latest-owner barrier, and only then appends
// the same prepared successor to every member. Explicit event IDs make a
// partial I/O failure repairable by the same launcher attempt.
func AttachParkedLocalSuccessor(ctx context.Context, options ParkedLocalSuccessorOptions) error {
	if options.Successor.PID <= 0 || options.Successor.WBSessionID == "" || options.Successor.StartedAt.IsZero() ||
		options.Successor.PredecessorWBSessionID != options.Bundle.Source.WBSessionID || options.Successor.WBSessionID == options.Bundle.Source.WBSessionID {
		return fmt.Errorf("local parked successor does not descend from the parked source session")
	}
	if options.AttemptID == "" || options.AttemptIndex == 0 {
		return fmt.Errorf("local parked successor requires one stable launcher attempt")
	}
	members := make([]parkedLocalMember, len(options.Bundle.Worktrees))
	for index, member := range options.Bundle.Worktrees {
		members[index].member = member
	}
	sort.Slice(members, func(i, j int) bool { return members[i].member.WorktreeDir < members[j].member.WorktreeDir })
	defer func() {
		for index := len(members) - 1; index >= 0; index-- {
			if members[index].unlock != nil {
				members[index].unlock()
			}
			if members[index].directory != nil {
				_ = members[index].directory.Close()
			}
		}
	}()
	for index := range members {
		directory, err := openJournalSubdirectory(members[index].member.WorktreeDir, worklogDirectory, false)
		if err != nil {
			return fmt.Errorf("open parked member Work Log journal: %w", err)
		}
		unlock, err := lockLocalWorkLog(directory)
		if err != nil {
			_ = directory.Close()
			return err
		}
		members[index].directory, members[index].unlock = directory, unlock
	}
	for index := range members {
		if err := validateParkedLocalMember(ctx, options, members[index]); err != nil {
			return fmt.Errorf("preflight parked local member %s: %w", members[index].member.WorktreeDir, err)
		}
	}
	bundleRaw, err := sessionpark.EncodeBundle(options.Bundle)
	if err != nil {
		return err
	}
	digest := sessionmove.DigestBytes(bundleRaw)
	for index := range members {
		member := members[index].member
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
		if _, _, err := appendLocalEventUnderLock(member.WorktreeDir, members[index].directory, event); err != nil {
			return fmt.Errorf("attach local parked successor to %s: %w", member.WorktreeDir, err)
		}
	}
	return nil
}

func validateParkedLocalMember(ctx context.Context, options ParkedLocalSuccessorOptions, prepared parkedLocalMember) error {
	member := prepared.member
	if member.OwnerEventID == "" || member.WorkLogReference == "" {
		return fmt.Errorf("parked member lacks exact source Work Log custody evidence; park again")
	}
	guard, err := Guard(ctx, member.WorktreeDir, GuardOptions{ProjectsRoot: options.ProjectsRoot, Admission: AdmissionEnforce})
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
	defer worktree.close()
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
	home, err := wbhome.Root(options.ProjectsRoot)
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
	bundleRaw, encodeErr := sessionpark.EncodeBundle(options.Bundle)
	if encodeErr != nil {
		return encodeErr
	}
	replayOwner := externalLocalEventID("park-local-owner", sessionmove.DigestBytes(bundleRaw), options.AttemptID+"-"+member.OwnerEventID)
	if latestOwner != member.OwnerEventID && latestOwner != replayOwner {
		return fmt.Errorf("newer session custody exists after park")
	}
	return nil
}
