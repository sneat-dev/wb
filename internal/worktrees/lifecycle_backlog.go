package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const lifecycleBacklogVersion = 1

const (
	lifecycleStageSealed              = "sealed"
	lifecycleStageRetiringRemote      = "retiring_remote"
	lifecycleStageRemoteRetired       = "remote_retired"
	lifecycleStageRemovingWorktree    = "removing_worktree"
	lifecycleStageWorktreeRemoved     = "worktree_removed"
	lifecycleStageRemovingLocalBranch = "removing_local_branch"
	lifecycleStageComplete            = "complete"
)

// lifecycleBacklogRecord is the durable, private recovery journal for the
// narrow destructive interval after a Work Log has been sealed. In
// particular, `removing_worktree` is persisted before Git removes the linked
// checkout, so a crash cannot make the remaining exact local branch invisible
// merely because live worktree inventory no longer finds it.
type lifecycleBacklogRecord struct {
	Version       int    `json:"version"`
	ID            string `json:"id"`
	Task          string `json:"task"`
	Repository    string `json:"repository"`
	ProjectsRoot  string `json:"projects_root"`
	CanonicalDir  string `json:"canonical_dir"`
	WorktreesRoot string `json:"worktrees_root"`
	WorktreeDir   string `json:"worktree_dir"`
	Branch        string `json:"branch"`
	Base          string `json:"base"`
	HeadSHA       string `json:"head_sha"`
	// External marks a backlog record for a worktree `wb worktree adopt`
	// registered without relocating it under WorktreesRoot — see
	// ListResult.External. It changes only which shape
	// validateLifecycleBacklog accepts for WorktreeDir.
	External            bool      `json:"external,omitempty"`
	Disposition         string    `json:"disposition"`
	RecoveryKind        string    `json:"recovery_kind,omitempty"`
	WorkLogEffort       string    `json:"work_log_effort_id,omitempty"`
	WorkLogRun          string    `json:"work_log_run_id,omitempty"`
	WorkLogClaim        string    `json:"work_log_claim_id,omitempty"`
	PreserveLocalBranch bool      `json:"preserve_local_branch,omitempty"`
	Failure             string    `json:"failure,omitempty"`
	RemoteHeadSHA       string    `json:"remote_head_sha,omitempty"`
	Stage               string    `json:"stage"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func lifecycleBacklogID(result ListResult, disposition string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"wb-lifecycle-backlog-v1", result.Task, result.Repository, result.Branch,
		result.HeadSHA, result.CanonicalDir, result.WorktreeDir, disposition,
	}, "\x00")))
	return hex.EncodeToString(digest[:])
}

func newLifecycleBacklogRecord(projectsRoot string, result ListResult, disposition string) lifecycleBacklogRecord {
	now := time.Now().UTC()
	return lifecycleBacklogRecord{
		Version: lifecycleBacklogVersion, ID: lifecycleBacklogID(result, disposition),
		Task: result.Task, Repository: result.Repository, ProjectsRoot: filepath.Clean(projectsRoot),
		CanonicalDir: result.CanonicalDir, WorktreesRoot: result.WorktreesRoot, WorktreeDir: result.WorktreeDir,
		Branch: result.Branch, Base: result.Base, HeadSHA: result.HeadSHA, RemoteHeadSHA: result.RemoteHeadSHA,
		External:    result.External,
		Disposition: disposition, Stage: lifecycleStageSealed, CreatedAt: now, UpdatedAt: now,
	}
}

func lifecycleBacklogDirectory(home string) string {
	return filepath.Join(home, "reports", "worktree-cleanup", "backlog")
}

func lifecycleBacklogPath(home, id string) string {
	return filepath.Join(lifecycleBacklogDirectory(home), id+".json")
}

func openLifecycleBacklogDirectory(home string, create bool) (*os.File, error) {
	homeDirectory, err := openAbsoluteDirectoryNoFollow(home, create)
	if err != nil {
		return nil, err
	}
	defer func() { _ = homeDirectory.Close() }()
	current := homeDirectory
	for _, segment := range []string{"reports", "worktree-cleanup", "backlog"} {
		next, openErr := openPrivateChild(current, segment, create)
		if current != homeDirectory {
			_ = current.Close()
		}
		if openErr != nil {
			return nil, openErr
		}
		current = next
	}
	return current, nil
}

func persistLifecycleBacklog(home string, record *lifecycleBacklogRecord, stage string) error {
	if record == nil {
		return fmt.Errorf("lifecycle backlog record is unavailable")
	}
	record.Stage = stage
	record.UpdatedAt = time.Now().UTC()
	if err := validateLifecycleBacklog(*record); err != nil {
		return err
	}
	directory, err := openLifecycleBacklogDirectory(home, true)
	if err != nil {
		return fmt.Errorf("open lifecycle backlog: %w", err)
	}
	defer func() { _ = directory.Close() }()
	if err := writeJSONAtomicAt(directory, record.ID+".json", record, 0o600); err != nil {
		return fmt.Errorf("persist lifecycle backlog %s: %w", record.ID, err)
	}
	return nil
}

func validateLifecycleBacklog(record lifecycleBacklogRecord) error {
	if record.Version != lifecycleBacklogVersion || !validClaimID(record.ID) || !validSafeSegment(record.Task) {
		return fmt.Errorf("invalid lifecycle backlog identity")
	}
	owner, repository, err := splitRepository(record.Repository)
	if err != nil {
		return fmt.Errorf("invalid lifecycle backlog repository: %w", err)
	}
	if !filepath.IsAbs(record.ProjectsRoot) || !filepath.IsAbs(record.CanonicalDir) || !filepath.IsAbs(record.WorktreesRoot) || !filepath.IsAbs(record.WorktreeDir) {
		return fmt.Errorf("lifecycle backlog paths must be absolute")
	}
	if filepath.Clean(record.CanonicalDir) != filepath.Join(filepath.Clean(record.ProjectsRoot), owner, repository) {
		return fmt.Errorf("lifecycle backlog canonical path does not match repository")
	}
	if record.External {
		// An adopted worktree was never relocated under WorktreesRoot — see
		// ListResult.External — so it has no <task>/<owner>/<repository>
		// shape to check here. The one thing recovery can and must still
		// prove from this record alone is that it is not lying about being
		// external: a genuinely external path never lands inside the WB
		// worktrees root it names.
		if pathWithin(record.WorktreesRoot, record.WorktreeDir) {
			return fmt.Errorf("lifecycle backlog marked external but its worktree is nested under the WB worktrees root")
		}
	} else {
		relativeWorktree, err := filepath.Rel(filepath.Clean(record.WorktreesRoot), filepath.Clean(record.WorktreeDir))
		if err != nil {
			return fmt.Errorf("resolve lifecycle backlog worktree: %w", err)
		}
		parts := strings.Split(relativeWorktree, string(filepath.Separator))
		managedPath := len(parts) == 3 && parts[0] == record.Task && validRepositorySegment(parts[1]) && validRepositorySegment(parts[2])
		legacyPath := len(parts) == 2 && parts[0] == record.Task && validRepositorySegment(parts[1])
		// The final on-disk repository segment may legitimately differ from the
		// canonical slug after a historical repository rename. Inventory already
		// corroborated the Git common directory; recovery only needs to prove this
		// private path remained inside the exact managed task hierarchy.
		if !managedPath && !legacyPath {
			return fmt.Errorf("lifecycle backlog worktree path does not match managed layout")
		}
	}
	if !validBranch(context.Background(), record.Branch) {
		return fmt.Errorf("invalid lifecycle backlog branch identity")
	}
	if !validBranch(context.Background(), record.Base) {
		return fmt.Errorf("invalid lifecycle backlog base identity")
	}
	if !isGitObjectID(record.HeadSHA) {
		return fmt.Errorf("invalid lifecycle backlog head identity")
	}
	result := ListResult{Task: record.Task, Repository: record.Repository, CanonicalDir: record.CanonicalDir,
		WorktreeDir: record.WorktreeDir, Branch: record.Branch, HeadSHA: record.HeadSHA}
	if lifecycleBacklogID(result, record.Disposition) != record.ID {
		return fmt.Errorf("lifecycle backlog ID does not match immutable evidence")
	}
	switch record.Disposition {
	case "removed", string(AbortDiscarded):
	default:
		return fmt.Errorf("invalid lifecycle backlog disposition %q", record.Disposition)
	}
	switch record.RecoveryKind {
	case "":
		if record.WorkLogEffort != "" || record.WorkLogRun != "" || record.WorkLogClaim != "" || record.PreserveLocalBranch || record.Failure != "" {
			return fmt.Errorf("ordinary lifecycle backlog contains create-recovery metadata")
		}
	case "create_work_log_failed":
		if record.Disposition != "removed" {
			return fmt.Errorf("create recovery backlog must retire a failed publication")
		}
		if len(record.Failure) > 2000 || strings.ContainsRune(record.Failure, '\x00') {
			return fmt.Errorf("invalid create recovery failure detail")
		}
		if record.WorkLogClaim != "" && (!validSafeSegment(record.WorkLogEffort) || !validSafeSegment(record.WorkLogRun) || !validClaimID(record.WorkLogClaim)) {
			return fmt.Errorf("invalid create recovery Work Log identity")
		}
		if record.WorkLogClaim == "" && (record.WorkLogEffort != "" || record.WorkLogRun != "") {
			return fmt.Errorf("incomplete create recovery Work Log identity")
		}
	default:
		return fmt.Errorf("invalid lifecycle backlog recovery kind %q", record.RecoveryKind)
	}
	switch record.Stage {
	case lifecycleStageSealed, lifecycleStageRetiringRemote, lifecycleStageRemoteRetired,
		lifecycleStageRemovingWorktree, lifecycleStageWorktreeRemoved,
		lifecycleStageRemovingLocalBranch, lifecycleStageComplete:
	default:
		return fmt.Errorf("invalid lifecycle backlog stage %q", record.Stage)
	}
	return nil
}

func loadResumableLifecycleBacklog(ctx context.Context, home, projectsRoot string, worktreesRoots []string, task, filter, disposition string) ([]lifecycleBacklogRecord, error) {
	directory, err := openLifecycleBacklogDirectory(home, false)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read lifecycle cleanup backlog: %w", err)
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("read lifecycle cleanup backlog: %w", err)
	}
	allowedRoots := make(map[string]bool, len(worktreesRoots))
	for _, root := range worktreesRoots {
		allowedRoots[filepath.Clean(root)] = true
	}
	var records []lifecycleBacklogRecord
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var record lifecycleBacklogRecord
		if err := readJSONAt(directory, entry.Name(), &record); err != nil {
			return nil, fmt.Errorf("decode lifecycle backlog %s: %w", entry.Name(), err)
		}
		if err := validateLifecycleBacklog(record); err != nil {
			return nil, fmt.Errorf("validate lifecycle backlog %s: %w", entry.Name(), err)
		}
		if entry.Name() != record.ID+".json" || record.Stage == lifecycleStageComplete || record.Disposition != disposition ||
			filepath.Clean(record.ProjectsRoot) != filepath.Clean(projectsRoot) || !allowedRoots[filepath.Clean(record.WorktreesRoot)] {
			continue
		}
		if task != "" && record.Task != task || !filterMatches(filter, record.Repository) {
			continue
		}
		switch record.Stage {
		case lifecycleStageRetiringRemote, lifecycleStageRemoteRetired,
			lifecycleStageRemovingWorktree, lifecycleStageWorktreeRemoved, lifecycleStageRemovingLocalBranch:
			if _, statErr := os.Lstat(record.WorktreeDir); statErr == nil {
				// A path that still exists is not proof the destructive command
				// never ran. Git removes a worktree's registration even when it
				// fails partway through deleting the tree, so the registration
				// is what separates a refusal from residue. Only a worktree Git
				// still knows about is left to the live inventory, which
				// remains authoritative for it and will recheck it; residue is
				// invisible to that inventory and would otherwise strand here
				// forever.
				registered, registrationErr := worktreeStillRegistered(ctx, record.CanonicalDir, record.WorktreeDir)
				if registrationErr != nil || registered {
					// An unreadable canonical repository is legitimate history
					// (a rename, a relocation), never grounds to fail every
					// other task's cleanup. Leaving the record is exactly
					// today's behavior for it.
					continue
				}
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return nil, fmt.Errorf("inspect lifecycle backlog worktree %s: %w", record.WorktreeDir, statErr)
			}
			records = append(records, record)
		}
	}
	sort.Slice(records, func(left, right int) bool { return records[left].ID < records[right].ID })
	return records, nil
}

// resumeLifecycleBacklog finishes the exact residual checkout and local-ref
// deletion this record's own interrupted run left behind. It refuses if the
// worktree registration or remote branch still exists, or if the local ref
// moved. The private record is therefore a recovery hint, never authority to
// delete a different checkout or branch.
func resumeLifecycleBacklog(ctx context.Context, home string, record *lifecycleBacklogRecord, deleteRemote bool) error {
	if err := validateLifecycleBacklog(*record); err != nil {
		return err
	}
	// This is the one caller allowed to reclaim a lock an interrupted run left
	// behind: the record states exactly which worktree, registration, remote
	// and branch head must already be gone or unchanged, and every one of them
	// is revalidated below before anything is deleted. A live operation in
	// another process is still refused.
	task, err := acquireCleanupTaskAtReclaimingInterrupted(record.WorktreesRoot, record.Task, true)
	if err != nil {
		lockErr := fmt.Errorf("lock lifecycle backlog task %s: %w", record.Task, err)
		if !errors.Is(err, os.ErrNotExist) {
			return lockErr
		}
		// The task namespace itself is gone, so there is nothing left to lock —
		// and, if every subject this record names is also already absent,
		// nothing left to do but close the journal. Completing it then is a
		// private write, not a deletion, which is why it is allowed without the
		// lock that guards deletions. A record that still owes any of them
		// keeps the original error: work needs the lock WB can no longer take.
		return completeVacantLifecycleBacklog(ctx, home, record, lockErr)
	}
	defer func() {
		_ = task.lock.release()
		task.close()
	}()
	canonical, err := openCanonicalRepository(record.CanonicalDir)
	if err != nil {
		return err
	}
	defer canonical.close()
	if err := canonical.validate(); err != nil {
		return err
	}
	if remoteHead, err := remoteBranchHead(ctx, record.CanonicalDir, record.Branch); err != nil {
		return err
	} else if remoteHead != "" && !record.PreserveLocalBranch {
		// A record sealed at retiring_remote was interrupted *during* the remote
		// deletion its own run had already authorized, so the surviving branch
		// is the unfinished step rather than evidence something else owns it.
		// Finishing it is the only way that record ever reaches complete: every
		// other stage refuses a live remote, which is why such a record stayed
		// unresumable — and invisible, because the loader did not admit its
		// stage either.
		//
		// The lease is the whole safety argument. Deleting only when origin
		// still stands exactly where the record observed it proves nothing has
		// landed on the branch since, so a resume can never discard work that
		// arrived after the interruption.
		if record.Stage != lifecycleStageRetiringRemote || !deleteRemote {
			return fmt.Errorf("resume lifecycle backlog %s: origin/%s still exists at %s", record.ID, record.Branch, remoteHead)
		}
		if record.RemoteHeadSHA == "" || remoteHead != record.RemoteHeadSHA {
			return fmt.Errorf("resume lifecycle backlog %s: origin/%s advanced from %s to %s since the interrupted run observed it", record.ID, record.Branch, record.RemoteHeadSHA, remoteHead)
		}
		if err := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "",
			"push", "--force-with-lease=refs/heads/"+record.Branch+":"+record.RemoteHeadSHA,
			"origin", ":refs/heads/"+record.Branch); err != nil {
			return fmt.Errorf("resume exact remote branch retirement %s: %w", record.Branch, err)
		}
		if err := persistLifecycleBacklog(home, record, lifecycleStageRemoteRetired); err != nil {
			return err
		}
	}
	registrations, err := registeredWorktreePathsCanonical(ctx, canonical)
	if err != nil {
		return err
	}
	if registrations[filepath.Clean(record.WorktreeDir)] {
		return fmt.Errorf("resume lifecycle backlog %s: worktree remains registered", record.ID)
	}
	// A path that still exists here is residue: this record's own removal step
	// unregistered the worktree and could not finish deleting it. The record
	// named the exact path, and the registration check above proved Git has
	// already let go of it, so finishing that deletion is what resuming means.
	// Anything still registered was refused and returned above.
	if _, err := os.Lstat(record.WorktreeDir); err == nil {
		worktree, openErr := openCleanupWorktree(task, CleanupResult{ListResult: ListResult{WorktreeDir: record.WorktreeDir, External: record.External}})
		if openErr != nil {
			return fmt.Errorf("resume lifecycle backlog %s: %w", record.ID, openErr)
		}
		if _, removeErr := removeUnregisteredWorktreeResidue(worktree, record.WorktreeDir); removeErr != nil {
			worktree.close()
			return fmt.Errorf("resume lifecycle backlog %s: %w", record.ID, removeErr)
		}
		parentErr := worktree.removeEmptyParent(nil)
		worktree.close()
		if parentErr != nil {
			return fmt.Errorf("resume lifecycle backlog %s: %w", record.ID, parentErr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	exists, err := localBranchExistsCanonical(ctx, canonical, record.Branch)
	if err != nil {
		return err
	}
	if exists && !record.PreserveLocalBranch {
		head, err := gitCanonical(ctx, canonical, "rev-parse", "refs/heads/"+record.Branch)
		if err != nil {
			return err
		}
		if head != record.HeadSHA {
			return fmt.Errorf("resume lifecycle backlog %s: branch moved from %s to %s", record.ID, record.HeadSHA, head)
		}
		if err := persistLifecycleBacklog(home, record, lifecycleStageRemovingLocalBranch); err != nil {
			return err
		}
		if err := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "update-ref", "-d", "refs/heads/"+record.Branch, record.HeadSHA); err != nil {
			return fmt.Errorf("resume exact local branch deletion %s: %w", record.Branch, err)
		}
	}
	if record.RecoveryKind == "create_work_log_failed" && record.WorkLogClaim != "" {
		if err := sealCreateFailureBacklogClaim(home, *record); err != nil {
			return err
		}
	}
	return persistLifecycleBacklog(home, record, lifecycleStageComplete)
}

// completeVacantLifecycleBacklog closes a record whose task namespace no longer
// exists, and only when the record has nothing left to act on: no checkout at
// the recorded path, no registration, no remote source branch, and no local
// ref. Every check is read-only, and the single write is to WB's own private
// journal. Anything still present returns lockErr unchanged, so a record that
// still owes a deletion is never quietly marked done.
func completeVacantLifecycleBacklog(ctx context.Context, home string, record *lifecycleBacklogRecord, lockErr error) error {
	if _, err := os.Lstat(record.WorktreeDir); !errors.Is(err, os.ErrNotExist) {
		return lockErr
	}
	canonical, err := openCanonicalRepository(record.CanonicalDir)
	if err != nil {
		return lockErr
	}
	defer canonical.close()
	if err := canonical.validate(); err != nil {
		return lockErr
	}
	registrations, err := registeredWorktreePathsCanonical(ctx, canonical)
	if err != nil || registrations[filepath.Clean(record.WorktreeDir)] {
		return lockErr
	}
	if remoteHead, err := remoteBranchHead(ctx, record.CanonicalDir, record.Branch); err != nil || (remoteHead != "" && !record.PreserveLocalBranch) {
		return lockErr
	}
	if exists, err := localBranchExistsCanonical(ctx, canonical, record.Branch); err != nil || (exists && !record.PreserveLocalBranch) {
		return lockErr
	}
	if record.RecoveryKind == "create_work_log_failed" && record.WorkLogClaim != "" {
		if err := sealCreateFailureBacklogClaim(home, *record); err != nil {
			return err
		}
	}
	return persistLifecycleBacklog(home, record, lifecycleStageComplete)
}

func sealCreateFailureBacklogClaim(home string, record lifecycleBacklogRecord) error {
	runDir, _, err := openWorkLogRun(home, record.WorkLogEffort, record.WorkLogRun, false)
	if err != nil {
		return fmt.Errorf("open failed-create Work Log run: %w", err)
	}
	defer func() { _ = runDir.Close() }()
	claims, err := openPrivateChild(runDir, "claims", false)
	if err != nil {
		return err
	}
	var claim workLogClaim
	if err := readJSONAt(claims, record.WorkLogClaim+".json", &claim); err != nil {
		_ = claims.Close()
		return err
	}
	_ = claims.Close()
	if claim.ClaimID != record.WorkLogClaim || claim.EffortID != record.WorkLogEffort || claim.RunID != record.WorkLogRun ||
		claim.Task != record.Task || claim.Repository != record.Repository || claim.Worktree != record.WorktreeDir ||
		claim.Branch != record.Branch || claim.Base != record.Base || claim.Lifecycle != "active" {
		return fmt.Errorf("failed-create Work Log claim does not match durable cleanup receipt")
	}
	if _, err := writeWorkLogTerminal(home, runDir, claim, record.HeadSHA, "create_failed", "", ""); err != nil {
		return fmt.Errorf("seal failed-create Work Log claim: %w", err)
	}
	return nil
}
