package worktrees

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/repopath"
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
	member sessionpark.Worktree
	// resolvedWorktreeDir is member's checkout resolved by identity, not by
	// its recorded (possibly stale) WorktreeDir: the same path as
	// member.WorktreeDir when that still exists, or the current location the
	// relocation-receipt journal for member.WorkLogReference records
	// otherwise. Every subsequent use of this member's checkout path -- the
	// Work Log journal, the successor's continuation context -- uses this,
	// never the raw recorded member.WorktreeDir.
	resolvedWorktreeDir string
	guard               GuardResult
	worktree            *cleanupWorktreeHandle
	directory           *os.File
	unlock              func()
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

// ResolvedWorktreeDirs maps each member's recorded (park-time) WorktreeDir to
// its currently resolved checkout path -- identical when the recorded path
// still resolves directly, and the relocation-receipt-resolved path
// otherwise. Callers building the successor's continuation context use this
// so it names the CURRENT paths, never a stale recorded one.
//
// A nil receiver (a zero-member local resume, or a test stub that never
// built custody) returns nil rather than panicking: callers that always
// call this before building the continuation context must not crash on the
// no-custody path.
func (custody *ParkedLocalCustody) ResolvedWorktreeDirs() map[string]string {
	if custody == nil {
		return nil
	}
	out := make(map[string]string, len(custody.members))
	for _, prepared := range custody.members {
		out[prepared.member.WorktreeDir] = prepared.resolvedWorktreeDir
	}
	return out
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

// acquire resolves a parked member by identity, not by its recorded absolute
// paths (REQ: resume-resolves-members-by-identity): the canonical clone is
// resolved from member.Repository through the same host-level-first
// placement resolution every other command uses (repopath.Locate), and the
// checkout is resolved from member.WorktreeDir, or, when that path no longer
// exists, through the relocation-receipt journal recorded for member's Work
// Log reference. Only then is the checkout verified: a linked worktree of the
// resolved canonical clone, on the recorded branch, with an origin
// corroborating the recorded repository_remote. This is what lets member
// resolution keep working after a layout migration moves the canonical clone,
// a checkout relocation moves the checkout, or both.
func (custody *ParkedLocalCustody) acquire(ctx context.Context, index int) error {
	prepared := &custody.members[index]
	member := prepared.member
	resolvedCanonicalDir, err := resolveParkedMemberCanonicalDir(custody.projectsRoot, member.Repository)
	if err != nil {
		return fmt.Errorf("resolve canonical clone for %s: %w", member.Repository, err)
	}
	resolvedWorktreeDir, err := resolveParkedMemberWorktreeDir(custody.projectsRoot, member)
	if err != nil {
		return err
	}
	guard, err := Guard(ctx, resolvedWorktreeDir, GuardOptions{ProjectsRoot: custody.projectsRoot, Admission: AdmissionEnforce})
	if err != nil {
		return err
	}
	if guard.Kind != "linked" || guard.Transient || guard.Branch != member.Branch || guard.CanonicalDir != resolvedCanonicalDir {
		return fmt.Errorf("managed worktree identity changed since park: recorded canonical %s worktree %s; resolved canonical %s worktree %s",
			member.CanonicalDir, member.WorktreeDir, resolvedCanonicalDir, resolvedWorktreeDir)
	}
	if member.RepositoryRemote != "" {
		if reason := verifyParkedMemberOriginRemote(ctx, resolvedCanonicalDir, member.RepositoryRemote); reason != "" {
			return fmt.Errorf("managed worktree identity changed since park: %s", reason)
		}
	}
	worktree, err := openAdoptedCleanupWorktree(guard.Path)
	if err != nil {
		return err
	}
	directory, err := openJournalSubdirectory(resolvedWorktreeDir, worklogDirectory, false)
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
	prepared.resolvedWorktreeDir, prepared.guard, prepared.worktree, prepared.directory, prepared.unlock = resolvedWorktreeDir, guard, worktree, directory, unlock
	return nil
}

// resolveParkedMemberCanonicalDir resolves member's canonical clone from its
// repository coordinate through repopath.Locate -- the same host-level-first
// placement resolution every other command uses -- rather than trusting the
// member's recorded, possibly stale canonical_dir.
func resolveParkedMemberCanonicalDir(projectsRoot, repository string) (string, error) {
	org, repo, ok := strings.Cut(repository, "/")
	if !ok || org == "" || repo == "" {
		return "", fmt.Errorf("parked member repository %q is not owner/repository", repository)
	}
	address, err := repopath.Locate(projectsRoot, org, repo)
	if err != nil {
		return "", err
	}
	return address.Path(projectsRoot), nil
}

// resolveParkedMemberWorktreeDir resolves member's checkout: its recorded
// path when that still exists AND still carries member's own Work Log
// reference, or otherwise the current location the relocation-receipt
// journal for its Work Log reference records -- the same chain claims (and
// RecordCloneMoveRelocationIntents) use to resolve a moved checkout, tried
// across every home wbhome.Resolve reports for projectsRoot. A recorded path
// that exists but now holds a different checkout (recycled after a move
// this member's own relocation receipt records) is never trusted merely for
// existing: it falls through to the receipt chain exactly like a path that
// no longer exists at all.
func resolveParkedMemberWorktreeDir(projectsRoot string, member sessionpark.Worktree) (string, error) {
	if _, statErr := os.Lstat(member.WorktreeDir); statErr == nil && parkedMemberOwnsWorktree(member.WorktreeDir, member.WorkLogReference) {
		return member.WorktreeDir, nil
	}
	reference, err := sessionmove.ParseWorkLogReference(member.WorkLogReference)
	if err != nil {
		return "", fmt.Errorf("recorded worktree %s is unusable and its Work Log reference is unusable: %w", member.WorktreeDir, err)
	}
	resolution, err := wbhome.Resolve(projectsRoot)
	if err != nil {
		return "", err
	}
	for _, home := range resolvedClaimHomes(resolution) {
		claim, claimErr := readWorkLogClaimByReference(home, reference)
		if claimErr != nil {
			continue
		}
		chain, chainErr := resolveRelocationChain(home, claim)
		if chainErr != nil || chain.worktree == "" {
			continue
		}
		return chain.worktree, nil
	}
	return "", fmt.Errorf("recorded worktree %s is missing or no longer this member's checkout, and no relocation receipt resolves its Work Log reference %s", member.WorktreeDir, member.WorkLogReference)
}

// corroborateProjectionAcrossHomes corroborates projection against the
// private Work Log claim recorded for worktree, tried across every home
// wbhome.Resolve reports for projectsRoot -- not only the current write
// home. A member's claim may live in a home other than the current write
// home: one parked while its own session's write home was the retired
// legacy $HOME/.wb (or any other home wbhome.Resolve still reads) keeps its
// claim there across a later layout migration or a resume invoked against a
// different projects root. Shared by local and remote parked-session
// resume's member validation, exactly like claimForRelocationAcrossHomes is
// shared by every relocation caller.
func corroborateProjectionAcrossHomes(projectsRoot, worktree string, projection workLogProjection) error {
	resolution, err := wbhome.Resolve(projectsRoot)
	if err != nil {
		return err
	}
	var lastErr error
	for _, home := range resolvedClaimHomes(resolution) {
		if err := corroborateProjectionWithPrivateClaim(home, worktree, projection); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no resolved home could corroborate the parked member's Work Log claim")
	}
	return lastErr
}

// parkedMemberOwnsWorktree reports whether the checkout at path is still the
// one carrying workLogReference -- true when path's own local Work Log
// projection resolves to exactly that effort/run/claim identity. It is false
// (never an error) for any other outcome: no projection, an unreadable one,
// or one naming a different claim, all mean this path is not (or is no
// longer) this member's checkout, so the caller must fall through to the
// relocation-receipt chain rather than trust a recycled path.
func parkedMemberOwnsWorktree(path, workLogReference string) bool {
	projection, err := readWorkLogProjection(path)
	if err != nil {
		return false
	}
	return "worklog:"+projection.EffortID+"/"+projection.RunID+"/"+projection.ClaimID == workLogReference
}

// readWorkLogClaimByReference reads a Work Log claim record directly by its
// effort/run/claim identity, independent of any worktree carrying a live
// projection file -- unlike activeWorkLogClaim, which requires exactly that.
// This is what lets a parked member's claim be found from its Work Log
// reference alone, when its recorded checkout no longer exists.
func readWorkLogClaimByReference(home string, reference sessionmove.WorkLogReference) (workLogClaim, error) {
	runDir, _, err := openWorkLogRun(home, reference.EffortID, reference.RunID, false)
	if err != nil {
		return workLogClaim{}, err
	}
	defer func() { _ = runDir.Close() }()
	claims, err := openPrivateChild(runDir, "claims", false)
	if err != nil {
		return workLogClaim{}, err
	}
	defer func() { _ = claims.Close() }()
	var claim workLogClaim
	if err := readJSONAt(claims, reference.ClaimID+".json", &claim); err != nil {
		return workLogClaim{}, err
	}
	return claim, nil
}

// verifyParkedMemberOriginRemote reports a non-empty reason unless the
// resolved canonical clone's own origin remote identifies the same
// repository as recordedRemote: repopath.Locate resolves a clone by
// owner/repository/host placement alone, never by reading a remote, so this
// is what corroborates that the resolved clone is genuinely the one this
// member was parked against.
func verifyParkedMemberOriginRemote(ctx context.Context, canonicalDir, recordedRemote string) string {
	recorded, err := gitremote.Parse(recordedRemote)
	if err != nil {
		return ""
	}
	raw, err := git(ctx, canonicalDir, "remote", "get-url", "origin")
	if err != nil {
		return fmt.Sprintf("cannot read origin remote of %s: %v", canonicalDir, err)
	}
	current, err := gitremote.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Sprintf("origin remote of %s is unusable: %v", canonicalDir, err)
	}
	if !current.Identity.Equal(recorded.Identity) {
		return fmt.Sprintf("origin remote of %s (%s) does not match the recorded repository_remote (%s)", canonicalDir, raw, recordedRemote)
	}
	return ""
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
		resolvedWorktreeDir := custody.members[index].resolvedWorktreeDir
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
		if _, _, err := appendLocalEventUnderLock(resolvedWorktreeDir, custody.members[index].directory, event); err != nil {
			return fmt.Errorf("attach local parked successor to %s: %w", resolvedWorktreeDir, err)
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
	projection, err := readWorkLogProjection(prepared.resolvedWorktreeDir)
	if err != nil || "worklog:"+projection.EffortID+"/"+projection.RunID+"/"+projection.ClaimID != member.WorkLogReference || projection.Lifecycle != "active" {
		return fmt.Errorf("active Work Log claim changed after park")
	}
	if err := corroborateProjectionAcrossHomes(projectsRoot, prepared.resolvedWorktreeDir, projection); err != nil {
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
