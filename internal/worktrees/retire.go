package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/wbhome"
	"golang.org/x/sys/unix"
)

// RetireOptions selects one WB-managed checkout. Inspector and ArchiveRemote
// are replaceable only so bare-remote integration tests never contact GitHub.
type RetireOptions struct {
	ProjectsRoot     string
	Task             string
	Repository       string
	Message          string
	Apply            bool
	Now              func() time.Time
	Inspect          RetiredArchiveInspector
	ArchiveRemote    string
	OpenPullRequests func(context.Context, string, string, string) (bool, error)
	RemoteOwnership  func(context.Context, string) error
	afterPhase       func(string) error // deterministic interruption point for integration tests
}

// RetireResult is also the durable resume receipt. It contains only identities
// and hashes; prompt and journal bytes are written only to the private repo.
type RetireResult struct {
	Version           int       `json:"version"`
	Task              string    `json:"task"`
	Repository        string    `json:"repository"`
	ArchiveRepository string    `json:"archive_repository"`
	Worktree          string    `json:"worktree"`
	Canonical         string    `json:"canonical"`
	WorktreesRoot     string    `json:"worktrees_root"`
	Local             bool      `json:"local"`
	Branch            string    `json:"branch"`
	OriginalRemoteSHA string    `json:"original_remote_sha,omitempty"`
	DeleteIntentSHA   string    `json:"delete_intent_sha,omitempty"`
	SourceSHA         string    `json:"source_sha"`
	IntentParentSHA   string    `json:"intent_parent_sha,omitempty"`
	IntentTreeSHA     string    `json:"intent_tree_sha,omitempty"`
	IntentMessage     string    `json:"intent_message,omitempty"`
	IntentAt          time.Time `json:"intent_at,omitempty"`
	RetiredRef        string    `json:"retired_ref"`
	ArchiveRef        string    `json:"archive_ref"`
	ArchiveSHA        string    `json:"archive_sha,omitempty"`
	ClaimID           string    `json:"claim_id"`
	EffortID          string    `json:"effort_id"`
	RunID             string    `json:"run_id"`
	Phase             string    `json:"phase"`
	ReportPath        string    `json:"report_path,omitempty"`
}

func Retire(ctx context.Context, options RetireOptions) (RetireResult, error) {
	if options.Now == nil {
		options.Now = time.Now
	}
	if !validSafeSegment(options.Task) {
		return RetireResult{}, fmt.Errorf("invalid retirement task %q", options.Task)
	}
	if options.Repository != "" {
		if _, _, err := splitRepository(options.Repository); err != nil {
			return RetireResult{}, err
		}
	}
	root, err := absoluteProjectsRoot(options.ProjectsRoot)
	if err != nil {
		return RetireResult{}, err
	}
	resolution, err := wbhome.Resolve(root)
	if err != nil {
		return RetireResult{}, err
	}
	inventory, err := ListWithDiagnostics(ctx, ListOptions{ProjectsRoot: root, Task: options.Task, Filter: options.Repository, Workers: 1})
	if err != nil {
		return RetireResult{}, err
	}
	if len(inventory.Diagnostics) != 0 {
		return RetireResult{}, fmt.Errorf("retirement inventory has %d malformed candidate(s)", len(inventory.Diagnostics))
	}
	var selected []ListResult
	for _, entry := range inventory.Results {
		if options.Repository == "" || options.Repository == entry.Repository {
			selected = append(selected, entry)
		}
	}
	if len(selected) != 1 {
		if len(selected) == 0 && options.Apply {
			return retireResumeRemoved(ctx, resolution.Write.Home, options)
		}
		return RetireResult{}, fmt.Errorf("retirement requires exactly one managed repository; found %d", len(selected))
	}
	entry := selected[0]
	if entry.External || entry.Detached || entry.Branch == "" {
		return RetireResult{}, fmt.Errorf("retirement requires a managed attached branch")
	}
	if entry.Locked {
		return RetireResult{}, fmt.Errorf("task %s has a competing lifecycle lock", options.Task)
	}
	if err := retireCheckOwner(entry); err != nil {
		return RetireResult{}, err
	}
	archivePlan, err := PlanRetiredArchivePreflight(ctx, entry.Repository, options.Inspect)
	if err != nil {
		return RetireResult{}, err
	}
	if archivePlan.Outcome != "planned" {
		return RetireResult{}, fmt.Errorf("retirement refused: %s", archivePlan.Refusal)
	}
	if options.RemoteOwnership == nil {
		return RetireResult{}, fmt.Errorf("retirement requires an authoritative remote owner check")
	}
	if err := options.RemoteOwnership(ctx, options.Task); err != nil {
		return RetireResult{}, fmt.Errorf("retirement remote owner preflight: %w", err)
	}
	if options.ArchiveRemote == "" {
		options.ArchiveRemote = "git@github.com:" + archivePlan.ArchiveRepository + ".git"
	}
	if options.OpenPullRequests == nil {
		options.OpenPullRequests = retireOpenPullRequests
	}
	if err := retireCheckPR(ctx, entry, options); err != nil {
		return RetireResult{}, err
	}
	claim, projection, err := retireReadClaim(resolution.Write.Home, entry.WorktreeDir)
	if err != nil {
		return RetireResult{}, fmt.Errorf("retirement requires a corroborated active Work Log: %w", err)
	}
	if claim.Task != entry.Task || claim.Repository != entry.Repository || claim.Branch != entry.Branch || projection.ClaimID != claim.ClaimID {
		return RetireResult{}, fmt.Errorf("Work Log claim does not bind the selected checkout")
	}
	if err := retireCheckIgnored(ctx, entry.WorktreeDir); err != nil {
		return RetireResult{}, err
	}
	remoteSHA, err := retireRemoteSHA(ctx, entry.CanonicalDir, "origin", "refs/heads/"+entry.Branch)
	if err != nil {
		return RetireResult{}, err
	}
	priorPath := retireReportPath(resolution.Write.Home, RetireResult{Task: entry.Task, Repository: entry.Repository})
	prior, priorErr := readRetireReport(priorPath)
	if priorErr != nil && !errors.Is(priorErr, os.ErrNotExist) {
		return RetireResult{}, priorErr
	}
	if errors.Is(priorErr, os.ErrNotExist) {
		if err := retireCheckRemoteAncestor(ctx, entry.CanonicalDir, remoteSHA, entry.HeadSHA); err != nil {
			return RetireResult{}, err
		}
	}
	result := RetireResult{Version: 1, Task: entry.Task, Repository: entry.Repository, ArchiveRepository: archivePlan.ArchiveRepository,
		Worktree: entry.WorktreeDir, Canonical: entry.CanonicalDir, WorktreesRoot: lifecycleTaskLockRoot(resolution.Write.Home, wbhome.Layout{WorktreesRoot: entry.WorktreesRoot, Local: entry.Local}), Local: entry.Local,
		Branch: entry.Branch, OriginalRemoteSHA: remoteSHA, SourceSHA: entry.HeadSHA, ClaimID: claim.ClaimID, EffortID: claim.EffortID, RunID: claim.RunID, Phase: "planned"}
	result.ReportPath = retireReportPath(resolution.Write.Home, result)
	if !options.Apply {
		result.RetiredRef = retiredBranchDestination(options.Now(), entry.Branch, entry.HeadSHA)
		result.ArchiveRef = retireArchiveRef(result)
		return result, nil
	}
	task, err := acquireCleanupTaskAtOrCreate(result.WorktreesRoot, options.Task)
	if err != nil {
		return RetireResult{}, fmt.Errorf("acquire retirement task lock: %w", err)
	}
	defer task.close()
	defer task.lock.release()
	if err := task.validate(); err != nil {
		return RetireResult{}, err
	}
	if err := options.RemoteOwnership(ctx, options.Task); err != nil {
		return RetireResult{}, fmt.Errorf("retirement remote owner recheck: %w", err)
	}
	heldWorktree, err := openCleanupWorktree(task, CleanupResult{ListResult: entry})
	if err != nil {
		return RetireResult{}, err
	}
	defer heldWorktree.close()
	if err := heldWorktree.validate(); err != nil {
		return RetireResult{}, err
	}
	freshOwners, err := ownerViews(entry.WorktreeDir)
	if err != nil {
		return RetireResult{}, err
	}
	entry.Owners = freshOwners
	if err := retireCheckOwner(entry); err != nil {
		return RetireResult{}, err
	}
	// Recheck under the held task lock. The first observation is for dry-run and
	// admission; all destructive steps use the second observation.
	current, err := git(ctx, entry.WorktreeDir, "rev-parse", "HEAD")
	if err != nil || current != entry.HeadSHA {
		return RetireResult{}, fmt.Errorf("checkout HEAD changed before retirement")
	}
	branch, err := git(ctx, entry.WorktreeDir, "branch", "--show-current")
	if err != nil || branch != entry.Branch {
		return RetireResult{}, fmt.Errorf("checkout branch changed before retirement")
	}
	if observed, err := retireRemoteSHA(ctx, entry.CanonicalDir, "origin", "refs/heads/"+entry.Branch); err != nil || observed != remoteSHA {
		return RetireResult{}, fmt.Errorf("original remote branch changed before retirement: %w", err)
	}
	if priorErr != nil {
		if err := retireCheckRemoteAncestor(ctx, entry.CanonicalDir, remoteSHA, current); err != nil {
			return RetireResult{}, err
		}
	}
	if err := retireCheckPR(ctx, entry, options); err != nil {
		return RetireResult{}, err
	}
	if err := retireCheckIgnored(ctx, entry.WorktreeDir); err != nil {
		return RetireResult{}, err
	}
	if priorErr == nil {
		remoteMatchesReceipt := remoteSHA == prior.OriginalRemoteSHA || remoteSHA == "" && (prior.OriginalRemoteSHA == "" || prior.DeleteIntentSHA == prior.OriginalRemoteSHA)
		if prior.Phase == "original_deleted" {
			remoteMatchesReceipt = remoteSHA == ""
		}
		if prior.Task != result.Task || prior.Repository != result.Repository || prior.Worktree != result.Worktree || prior.Canonical != result.Canonical || prior.WorktreesRoot != result.WorktreesRoot || prior.Branch != result.Branch || prior.ArchiveRepository != result.ArchiveRepository || prior.ClaimID != result.ClaimID || prior.EffortID != result.EffortID || prior.RunID != result.RunID || !remoteMatchesReceipt {
			return RetireResult{}, fmt.Errorf("retirement receipt conflicts with current checkout")
		}
		result = prior
		if result.Phase == "commit_intent" {
			if current == result.IntentParentSHA {
				committed, err := retireCommitSource(ctx, entry.CanonicalDir, heldWorktree, result.IntentMessage, func(tree, message string) error {
					if tree != result.IntentTreeSHA || message != result.IntentMessage {
						return fmt.Errorf("staged source changed after retirement commit intent")
					}
					return nil
				})
				if err != nil {
					return result, err
				}
				if !committed {
					return result, fmt.Errorf("retirement commit intent lost its staged changes")
				}
				current, err = git(ctx, entry.WorktreeDir, "rev-parse", "HEAD")
				if err != nil {
					return result, err
				}
			}
			if err := retireValidateIntentCommit(ctx, entry.WorktreeDir, result, current); err != nil {
				return result, err
			}
			if options.afterPhase != nil {
				if err := options.afterPhase("source_committed"); err != nil {
					return result, err
				}
			}
			result.SourceSHA = current
			result.RetiredRef = retiredBranchDestination(result.IntentAt, result.Branch, current)
			result.ArchiveRef = retireArchiveRef(result)
			result.Phase = "committed"
			if err := writeRetireReport(result); err != nil {
				return result, err
			}
		} else if current != result.SourceSHA {
			return RetireResult{}, fmt.Errorf("checkout moved after recorded retirement commit")
		}
	} else {
		if err := heldWorktree.validate(); err != nil {
			return RetireResult{}, err
		}
		committed, err := retireCommitSource(ctx, entry.CanonicalDir, heldWorktree, options.Message, func(tree, message string) error {
			result.IntentParentSHA = entry.HeadSHA
			result.IntentTreeSHA = tree
			result.IntentMessage = message
			result.IntentAt = options.Now().UTC()
			result.Phase = "commit_intent"
			return writeRetireReport(result)
		})
		if err != nil {
			return RetireResult{}, err
		}
		if committed && options.afterPhase != nil {
			if err := options.afterPhase("source_committed"); err != nil {
				return result, err
			}
		}
		result.SourceSHA, err = git(ctx, entry.WorktreeDir, "rev-parse", "HEAD")
		if err != nil {
			return RetireResult{}, err
		}
		if clean, cleanErr := cleanWorktree(ctx, entry.WorktreeDir); cleanErr != nil || !clean {
			return RetireResult{}, fmt.Errorf("source checkout is dirty after retirement commit: %w", cleanErr)
		}
		date := options.Now()
		if committed {
			date = result.IntentAt
		}
		result.RetiredRef = retiredBranchDestination(date, result.Branch, result.SourceSHA)
		result.ArchiveRef = retireArchiveRef(result)
		result.Phase = "committed"
		if err := writeRetireReport(result); err != nil {
			return RetireResult{}, err
		}
	}
	if err := retirePublishSource(ctx, &result); err != nil {
		return result, err
	}
	if err := writeRetireReport(result); err != nil {
		return result, err
	}
	if options.afterPhase != nil {
		if err := options.afterPhase(result.Phase); err != nil {
			return result, err
		}
	}
	if err := sealWorkLogForRecycle(resolution.Write.Home, entry.WorktreeDir, result.SourceSHA, "retired"); err != nil {
		return result, fmt.Errorf("seal retirement Work Log: %w", err)
	}
	if err := retireCheckPrivateArchive(ctx, result, options.Inspect); err != nil {
		return result, err
	}
	if err := retirePublishArchive(ctx, resolution.Write.Home, options.ArchiveRemote, &result); err != nil {
		return result, err
	}
	if err := writeRetireReport(result); err != nil {
		return result, err
	}
	if options.afterPhase != nil {
		if err := options.afterPhase(result.Phase); err != nil {
			return result, err
		}
	}
	if err := retireVerifyReceipts(ctx, entry.CanonicalDir, options.ArchiveRemote, result); err != nil {
		return result, err
	}
	if err := retireCheckPrivateArchive(ctx, result, options.Inspect); err != nil {
		return result, err
	}
	if err := retireCheckPR(ctx, entry, options); err != nil {
		return result, err
	}
	if err := options.RemoteOwnership(ctx, options.Task); err != nil {
		return result, fmt.Errorf("retirement remote owner final recheck: %w", err)
	}
	if err := retireCheckIgnored(ctx, entry.WorktreeDir); err != nil {
		return result, err
	}
	if result.OriginalRemoteSHA != "" && result.DeleteIntentSHA == "" {
		current, err := retireRemoteSHA(ctx, result.Canonical, "origin", "refs/heads/"+result.Branch)
		if err != nil || current != result.OriginalRemoteSHA {
			return result, fmt.Errorf("original remote branch disappeared or moved before deletion intent: %w", err)
		}
		proof, err := retireRemoteSHA(ctx, result.Canonical, "origin", retireDeletionProofRef(result))
		if err != nil || proof != "" {
			return result, fmt.Errorf("retirement deletion proof ref is already present or cannot be inspected: %w", err)
		}
		result.DeleteIntentSHA = result.OriginalRemoteSHA
		if err := writeRetireReport(result); err != nil {
			return result, err
		}
		if options.afterPhase != nil {
			if err := options.afterPhase("original_delete_intent"); err != nil {
				return result, err
			}
		}
	}
	if err := retireDeleteOriginal(ctx, &result, options.afterPhase); err != nil {
		return result, err
	}
	if err := writeRetireReport(result); err != nil {
		return result, err
	}
	if options.afterPhase != nil {
		if err := options.afterPhase(result.Phase); err != nil {
			return result, err
		}
	}
	if err := retireRemoveLocal(ctx, task, entry, &result, options.afterPhase); err != nil {
		return result, err
	}
	if err := writeRetireReport(result); err != nil {
		return result, err
	}
	return result, nil
}

func retireCheckOwner(entry ListResult) error {
	identity := CurrentIdentity()
	for _, owner := range entry.Owners {
		if owner.PIDStatus == "active" && (identity.PID <= 0 || owner.PID != identity.PID) {
			return fmt.Errorf("worktree has a competing active claim by PID %d", owner.PID)
		}
	}
	return nil
}

func retireReadClaim(home, worktree string) (workLogClaim, workLogProjection, error) {
	projection, err := readWorkLogProjectionForReadOnlyClaim(worktree)
	if err != nil {
		return workLogClaim{}, projection, err
	}
	if projection.Lifecycle != "active" && projection.Lifecycle != "terminal" {
		return workLogClaim{}, projection, fmt.Errorf("Work Log has unsupported lifecycle %s", projection.Lifecycle)
	}
	if err := corroborateProjectionWithPrivateClaim(home, worktree, projection); err != nil {
		return workLogClaim{}, projection, err
	}
	run, _, err := openWorkLogRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return workLogClaim{}, projection, err
	}
	defer run.Close()
	claims, err := openPrivateChild(run, "claims", false)
	if err != nil {
		return workLogClaim{}, projection, err
	}
	defer claims.Close()
	var claim workLogClaim
	if err := readJSONAt(claims, projection.ClaimID+".json", &claim); err != nil {
		return workLogClaim{}, projection, err
	}
	return claim, projection, nil
}

func retireCheckPrivateArchive(ctx context.Context, result RetireResult, inspect RetiredArchiveInspector) error {
	plan, err := PlanRetiredArchivePreflight(ctx, result.Repository, inspect)
	if err != nil {
		return err
	}
	if plan.Outcome != "planned" || plan.ArchiveRepository != result.ArchiveRepository {
		return fmt.Errorf("retirement archive is no longer the configured private repository")
	}
	return nil
}

func retireCheckRemoteAncestor(ctx context.Context, canonical, remoteSHA, localSHA string) error {
	if remoteSHA == "" || remoteSHA == localSHA {
		return nil
	}
	if _, err := git(ctx, canonical, "merge-base", "--is-ancestor", remoteSHA, localSHA); err != nil {
		return fmt.Errorf("original remote branch moved ahead or diverged from local HEAD")
	}
	return nil
}

func retireCheckIgnored(ctx context.Context, worktree string) error {
	output, err := git(ctx, worktree, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	var unexpected []string
	for _, path := range strings.Split(output, "\x00") {
		if path == "" || path == ".worktree.md" || path == ".wb-worklog.json" || strings.HasPrefix(path, ".wb/local/") || strings.HasPrefix(path, ".wb-worklog/") {
			continue
		}
		unexpected = append(unexpected, path)
	}
	if len(unexpected) != 0 {
		sort.Strings(unexpected)
		return fmt.Errorf("ignored worktree files would be lost during retirement: %s", strings.Join(unexpected, ", "))
	}
	return nil
}

func retireValidateIntentCommit(ctx context.Context, worktree string, intent RetireResult, head string) error {
	if head == intent.IntentParentSHA {
		return fmt.Errorf("retirement commit was not created")
	}
	parent, err := git(ctx, worktree, "rev-parse", "HEAD^")
	if err != nil || parent != intent.IntentParentSHA {
		return fmt.Errorf("retirement commit parent does not match durable intent: %w", err)
	}
	tree, err := git(ctx, worktree, "rev-parse", "HEAD^{tree}")
	if err != nil || tree != intent.IntentTreeSHA {
		return fmt.Errorf("retirement commit tree does not match durable intent: %w", err)
	}
	message, err := git(ctx, worktree, "log", "-1", "--format=%B")
	if err != nil || strings.TrimSpace(message) != intent.IntentMessage {
		return fmt.Errorf("retirement commit message does not match durable intent: %w", err)
	}
	if clean, err := cleanWorktree(ctx, worktree); err != nil || !clean {
		return fmt.Errorf("checkout changed after retirement commit: %w", err)
	}
	return nil
}

// A crash between Git's worktree removal and exact local ref deletion leaves
// no inventory row. The held receipt and two freshly observed remote refs are
// enough to finish only that last local step.
func retireResumeRemoved(ctx context.Context, home string, options RetireOptions) (RetireResult, error) {
	directory := filepath.Join(home, "reports", "worktree-retire", options.Task)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return RetireResult{}, fmt.Errorf("no managed checkout or retirement receipt for %s: %w", options.Task, err)
	}
	var reports []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			candidate := filepath.Join(directory, entry.Name())
			if options.Repository != "" && candidate != retireReportPath(home, RetireResult{Task: options.Task, Repository: options.Repository}) {
				continue
			}
			reports = append(reports, candidate)
		}
	}
	if len(reports) != 1 {
		return RetireResult{}, fmt.Errorf("removed retirement requires exactly one receipt; found %d", len(reports))
	}
	result, err := readRetireReport(reports[0])
	if err != nil {
		return RetireResult{}, err
	}
	if options.Repository != "" && result.Repository != options.Repository {
		return RetireResult{}, fmt.Errorf("retirement receipt repository mismatch")
	}
	if result.Task != options.Task || result.ReportPath != reports[0] || result.Phase != "original_deleted" && result.Phase != "complete" {
		return RetireResult{}, fmt.Errorf("retirement receipt does not authorize removed-checkout resume")
	}
	if err := retireValidateRemovedClaim(home, result); err != nil {
		return result, err
	}
	archivePlan, err := PlanRetiredArchivePreflight(ctx, result.Repository, options.Inspect)
	if err != nil || archivePlan.Outcome != "planned" || archivePlan.ArchiveRepository != result.ArchiveRepository {
		return RetireResult{}, fmt.Errorf("private archive preflight no longer holds")
	}
	if options.ArchiveRemote == "" {
		options.ArchiveRemote = "git@github.com:" + result.ArchiveRepository + ".git"
	}
	task, err := acquireCleanupTaskAtOrCreate(result.WorktreesRoot, result.Task)
	if err != nil {
		return result, err
	}
	defer task.close()
	defer task.lock.release()
	if err := task.validate(); err != nil {
		return result, err
	}
	if options.RemoteOwnership == nil {
		return result, fmt.Errorf("retirement requires an authoritative remote owner check")
	}
	if err := options.RemoteOwnership(ctx, options.Task); err != nil {
		return result, fmt.Errorf("retirement remote owner recheck: %w", err)
	}
	if err := retireVerifyReceipts(ctx, result.Canonical, options.ArchiveRemote, result); err != nil {
		return result, err
	}
	if source, err := retireRemoteSHA(ctx, result.Canonical, "origin", "refs/heads/"+result.Branch); err != nil || source != "" {
		return result, fmt.Errorf("original remote branch remains or changed: %w", err)
	}
	if result.OriginalRemoteSHA != "" {
		proof, err := retireRemoteSHA(ctx, result.Canonical, "origin", retireDeletionProofRef(result))
		if err != nil || proof != result.SourceSHA {
			return result, fmt.Errorf("original remote branch deletion proof changed: %w", err)
		}
	}
	if _, err := os.Lstat(result.Worktree); !errors.Is(err, os.ErrNotExist) {
		return result, fmt.Errorf("checkout path still exists or cannot be inspected: %w", err)
	}
	registered, err := git(ctx, result.Canonical, "worktree", "list", "--porcelain")
	if err != nil {
		return result, err
	}
	if strings.Contains(registered, "worktree "+result.Worktree+"\n") {
		return result, fmt.Errorf("checkout is still registered")
	}
	canonical, err := openCanonicalRepository(result.Canonical)
	if err != nil {
		return result, err
	}
	defer canonical.close()
	if exists, err := localBranchExists(ctx, result.Canonical, result.Branch); err != nil {
		return result, err
	} else if exists {
		if sha, err := git(ctx, result.Canonical, "rev-parse", "refs/heads/"+result.Branch); err != nil || sha != result.SourceSHA {
			return result, fmt.Errorf("local source branch changed: %w", err)
		}
		if err := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "update-ref", "-d", "refs/heads/"+result.Branch, result.SourceSHA); err != nil {
			return result, err
		}
	}
	result.Phase = "complete"
	if err := writeRetireReport(result); err != nil {
		return result, err
	}
	return result, nil
}

func retireValidateRemovedClaim(home string, result RetireResult) error {
	run, _, err := openWorkLogRun(home, result.EffortID, result.RunID, false)
	if err != nil {
		return err
	}
	defer run.Close()
	claims, err := openPrivateChild(run, "claims", false)
	if err != nil {
		return err
	}
	defer claims.Close()
	var claim workLogClaim
	if err := readJSONAt(claims, result.ClaimID+".json", &claim); err != nil {
		return err
	}
	if claim.Task != result.Task || claim.Repository != result.Repository || claim.Branch != result.Branch || claim.ClaimID != result.ClaimID || claim.EffortID != result.EffortID || claim.RunID != result.RunID || filepath.Clean(claim.Worktree) != filepath.Clean(result.Worktree) {
		return fmt.Errorf("retirement receipt conflicts with immutable Work Log claim")
	}
	terminals, err := openPrivateChild(run, "terminals", false)
	if err != nil {
		return err
	}
	defer terminals.Close()
	var terminal workLogTerminalRecord
	if err := readJSONAt(terminals, result.ClaimID+".json", &terminal); err != nil {
		return err
	}
	if terminal.ClaimID != result.ClaimID || terminal.FinalCommit != result.SourceSHA || terminal.Disposition != "retired" {
		return fmt.Errorf("retirement receipt conflicts with immutable Work Log terminal")
	}
	return nil
}

func retireCheckPR(ctx context.Context, entry ListResult, options RetireOptions) error {
	open, err := options.OpenPullRequests(ctx, entry.WorktreeDir, entry.Repository, entry.Branch)
	if err != nil {
		return fmt.Errorf("inspect open pull requests: %w", err)
	}
	if open {
		return fmt.Errorf("branch %s has an open pull request", entry.Branch)
	}
	return nil
}

func retireOpenPullRequests(ctx context.Context, worktree, repository, branch string) (bool, error) {
	owner, _, ok := strings.Cut(repository, "/")
	if !ok {
		return false, fmt.Errorf("invalid source repository")
	}
	query := url.Values{"head": []string{owner + ":" + branch}, "state": []string{"open"}}.Encode()
	response := githubobserver.Execute(ctx, worktree, "api", "--paginate", "repos/"+repository+"/pulls?"+query)
	if response.Err != nil {
		return false, response.Err
	}
	var pulls []githubPullRequest
	if err := json.Unmarshal(response.Stdout, &pulls); err != nil {
		return false, err
	}
	for _, pull := range pulls {
		if strings.EqualFold(pull.State, "open") {
			return true, nil
		}
	}
	base, err := openPullRequestUsingBranchAsBase(ctx, worktree, repository, branch)
	return base != nil, err
}

func retireRemoteSHA(ctx context.Context, directory, remote, ref string) (string, error) {
	output, err := git(ctx, directory, "ls-remote", remote, ref)
	if err != nil {
		return "", fmt.Errorf("inspect remote ref %s: %w", ref, err)
	}
	if output == "" {
		return "", nil
	}
	fields := strings.Fields(output)
	if len(fields) != 2 || fields[1] != ref || !isGitObjectID(fields[0]) {
		return "", fmt.Errorf("invalid remote ref observation for %s", ref)
	}
	return fields[0], nil
}

func retireCommitSource(ctx context.Context, canonical string, held *cleanupWorktreeHandle, message string, prepared func(tree, message string) error) (bool, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "Retire worktree source changes"
	}
	gitHeld := func(args ...string) ([]byte, error) {
		if err := held.validate(); err != nil {
			return nil, err
		}
		return runSecureRenameGitBytesWithHeldWorktree(ctx, canonical, held.parentPath, held.worktreePath, held.worktree, args...)
	}
	// Check every path that could introduce file bytes before touching the
	// index. --others expands untracked directories to actual files.
	for _, args := range [][]string{{"diff", "--name-only", "-z", "--diff-filter=ACMR"}, {"diff", "--cached", "--name-only", "-z", "--diff-filter=ACMR"}, {"ls-files", "--others", "--exclude-standard", "-z"}} {
		paths, err := gitHeld(args...)
		if err != nil {
			return false, err
		}
		for _, path := range strings.Split(string(paths), "\x00") {
			if path == "" {
				continue
			}
			if retireLooksLikeSecretPath(path) {
				return false, fmt.Errorf("refusing secret-looking source commit path %q", path)
			}
			if args[0] == "ls-files" {
				if err := retireCheckUntrackedPath(held.worktreePath, path); err != nil {
					return false, err
				}
			}
		}
	}
	if _, err := gitHeld("add", "-A"); err != nil {
		return false, err
	}
	_, _ = gitHeld("reset", "-q", "--", ".worktree.md")
	paths, err := gitHeld("diff", "--cached", "--name-only", "-z")
	if err != nil {
		return false, err
	}
	secretPaths, err := gitHeld("diff", "--cached", "--name-only", "-z", "--diff-filter=ACMR")
	if err != nil {
		return false, err
	}
	for _, path := range strings.Split(string(secretPaths), "\x00") {
		if path != "" && retireLooksLikeSecretPath(path) {
			return false, fmt.Errorf("refusing secret-looking source commit path %q", path)
		}
	}
	if len(paths) == 0 {
		return false, nil
	}
	tree, err := gitHeld("write-tree")
	if err != nil {
		return false, err
	}
	if prepared != nil {
		if err := prepared(strings.TrimSpace(string(tree)), message); err != nil {
			return false, err
		}
	}
	if _, err := gitHeld("commit", "-m", message); err != nil {
		return false, fmt.Errorf("commit source with hooks: %w", err)
	}
	return true, nil
}

func retireCheckUntrackedPath(worktree, relative string) error {
	parent, err := openAbsoluteDirectoryNoFollow(filepath.Join(worktree, filepath.Dir(relative)), false)
	if err != nil {
		return err
	}
	defer parent.Close()
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), filepath.Base(relative), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT == unix.S_IFLNK {
		return fmt.Errorf("refusing untracked symlink %q", relative)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("refusing nonregular untracked path %q", relative)
	}
	return nil
}

// Keep this conservative filename rule aligned with `wb pr create --commit-all`.
func retireLooksLikeSecretPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return base == ".env" || strings.HasPrefix(base, ".env.") || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".p12") || strings.HasPrefix(base, "id_rsa") || strings.HasPrefix(base, "id_ed25519") || strings.HasPrefix(base, "id_ecdsa") || base == ".worktree.md"
}

func retireArchiveRef(result RetireResult) string {
	_, repository, _ := strings.Cut(result.Repository, "/")
	return "retired/" + repository + "/" + strings.TrimPrefix(result.RetiredRef, "retired/")
}

func retireReportPath(home string, result RetireResult) string {
	owner, repository, _ := strings.Cut(result.Repository, "/")
	return filepath.Join(home, "reports", "worktree-retire", result.Task, owner+"-"+repository+".json")
}

func readRetireReport(path string) (RetireResult, error) {
	var result RetireResult
	parent, err := openAbsoluteDirectoryNoFollow(filepath.Dir(path), false)
	if err != nil {
		return result, err
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return result, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return result, fmt.Errorf("invalid retirement receipt file")
	}
	body, err := io.ReadAll(file)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return result, err
	}
	if result.Version != 1 || !isGitObjectID(result.SourceSHA) {
		return result, fmt.Errorf("invalid retirement receipt")
	}
	if result.DeleteIntentSHA != "" && (result.DeleteIntentSHA != result.OriginalRemoteSHA || !isGitObjectID(result.DeleteIntentSHA)) {
		return result, fmt.Errorf("invalid retirement original-ref deletion intent")
	}
	if (result.Phase == "original_deleted" || result.Phase == "complete") && result.OriginalRemoteSHA != "" && result.DeleteIntentSHA != result.OriginalRemoteSHA {
		return result, fmt.Errorf("retirement deletion receipt has no durable intent")
	}
	if result.Phase == "commit_intent" {
		if result.SourceSHA != result.IntentParentSHA || !isGitObjectID(result.IntentTreeSHA) || result.IntentMessage == "" || result.IntentAt.IsZero() || result.RetiredRef != "" || result.ArchiveRef != "" || result.DeleteIntentSHA != "" {
			return result, fmt.Errorf("invalid retirement commit intent")
		}
		return result, nil
	}
	if !strings.HasPrefix(result.RetiredRef, "retired/") || result.ArchiveRef != retireArchiveRef(result) {
		return result, fmt.Errorf("invalid retirement receipt")
	}
	stem := strings.TrimPrefix(result.RetiredRef, "retired/")
	if len(stem) < 9 {
		return result, fmt.Errorf("invalid retired ref date")
	}
	date, err := time.Parse("20060102", stem[:8])
	if err != nil || result.RetiredRef != retiredBranchDestination(date, result.Branch, result.SourceSHA) {
		return result, fmt.Errorf("retirement receipt ref does not bind branch and commit")
	}
	return result, nil
}

func writeRetireReport(result RetireResult) error {
	if err := os.MkdirAll(filepath.Dir(result.ReportPath), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(result.ReportPath), ".retire-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(body, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), result.ReportPath)
}

func retirePublishSource(ctx context.Context, result *RetireResult) error {
	ref := "refs/heads/" + result.RetiredRef
	current, err := retireRemoteSHA(ctx, result.Canonical, "origin", ref)
	if err != nil {
		return err
	}
	if current != "" && current != result.SourceSHA {
		return fmt.Errorf("retired remote ref has conflicting commit")
	}
	if current == "" {
		canonical, err := openCanonicalRepository(result.Canonical)
		if err != nil {
			return err
		}
		defer canonical.close()
		if err := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "push", "--force-with-lease="+ref+":", "origin", result.SourceSHA+":"+ref); err != nil {
			return fmt.Errorf("publish retired source ref: %w", err)
		}
	}
	verified, err := retireRemoteSHA(ctx, result.Canonical, "origin", ref)
	if err != nil || verified != result.SourceSHA {
		return fmt.Errorf("retired source ref verification failed: %w", err)
	}
	result.Phase = "source_published"
	return nil
}

func retireVerifyReceipts(ctx context.Context, canonical, archiveRemote string, result RetireResult) error {
	retired, err := retireRemoteSHA(ctx, canonical, "origin", "refs/heads/"+result.RetiredRef)
	if err != nil || retired != result.SourceSHA {
		return fmt.Errorf("retired source receipt changed: %w", err)
	}
	archive, err := retireRemoteSHA(ctx, canonical, archiveRemote, "refs/heads/"+result.ArchiveRef)
	if err != nil || archive != result.ArchiveSHA {
		return fmt.Errorf("private archive receipt changed: %w", err)
	}
	return nil
}

func retireDeletionProofRef(result RetireResult) string {
	return "refs/tags/wb-retirement-deleted/" + strings.TrimPrefix(result.RetiredRef, "retired/")
}

func retireDeleteOriginal(ctx context.Context, result *RetireResult, afterPhase func(string) error) error {
	ref := "refs/heads/" + result.Branch
	current, err := retireRemoteSHA(ctx, result.Canonical, "origin", ref)
	if err != nil {
		return err
	}
	if result.OriginalRemoteSHA != "" && result.DeleteIntentSHA != result.OriginalRemoteSHA {
		return fmt.Errorf("original remote deletion has no durable exact-SHA intent")
	}
	proofRef := retireDeletionProofRef(*result)
	proof, err := retireRemoteSHA(ctx, result.Canonical, "origin", proofRef)
	if err != nil {
		return err
	}
	switch {
	case result.OriginalRemoteSHA == "" && current == "" && proof == "":
		// A source branch that never existed remotely needs no delete proof.
	case current == result.OriginalRemoteSHA && current != "" && proof == "":
		canonical, err := openCanonicalRepository(result.Canonical)
		if err != nil {
			return err
		}
		defer canonical.close()
		if err := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "push", "--atomic", "--force-with-lease="+ref+":"+current, "--force-with-lease="+proofRef+":", "origin", ":"+ref, result.SourceSHA+":"+proofRef); err != nil {
			return fmt.Errorf("delete original remote branch with exact lease and atomic proof: %w", err)
		}
	case current == "" && result.OriginalRemoteSHA != "" && proof == result.SourceSHA:
		// A retry can prove that WB's atomic delete created this exact marker.
	default:
		return fmt.Errorf("original remote branch or deletion proof changed before exact-lease deletion")
	}
	verified, err := retireRemoteSHA(ctx, result.Canonical, "origin", ref)
	if err != nil || verified != "" {
		return fmt.Errorf("original branch deletion verification failed: %w", err)
	}
	if result.OriginalRemoteSHA != "" {
		verifiedProof, err := retireRemoteSHA(ctx, result.Canonical, "origin", proofRef)
		if err != nil || verifiedProof != result.SourceSHA {
			return fmt.Errorf("atomic original deletion proof verification failed: %w", err)
		}
	}
	if current != "" && afterPhase != nil {
		if err := afterPhase("original_delete_pushed"); err != nil {
			return err
		}
	}
	result.Phase = "original_deleted"
	return nil
}

func retireRemoveLocal(ctx context.Context, task *cleanupTaskHandle, entry ListResult, result *RetireResult, afterPhase func(string) error) error {
	worktree, err := openCleanupWorktree(task, CleanupResult{ListResult: entry})
	if err != nil {
		return err
	}
	defer worktree.close()
	if err := worktree.validate(); err != nil {
		return err
	}
	canonical, err := openCanonicalRepository(result.Canonical)
	if err != nil {
		return err
	}
	defer canonical.close()
	if head, err := git(ctx, result.Worktree, "rev-parse", "HEAD"); err != nil || head != result.SourceSHA {
		return fmt.Errorf("checkout moved before removal")
	}
	if clean, err := cleanWorktree(ctx, result.Worktree); err != nil || !clean {
		return fmt.Errorf("checkout changed before removal: %w", err)
	}
	if err := runSecureCleanupGitHelper(ctx, canonical, worktree.parent, worktree.worktree, worktree.parentPath, result.Worktree, "worktree", "remove", result.Worktree); err != nil {
		return err
	}
	if afterPhase != nil {
		if err := afterPhase("worktree_removed"); err != nil {
			return err
		}
	}
	if err := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "update-ref", "-d", "refs/heads/"+result.Branch, result.SourceSHA); err != nil {
		return err
	}
	if err := worktree.removeEmptyParent(nil, nil); err != nil {
		return err
	}
	result.Phase = "complete"
	return nil
}

// retireCaptureFile opens every parent without following symlinks. A journal
// symlink is refused, never copied by following its target.
func retireCaptureFile(source, destination string) (string, error) {
	parent, err := openAbsoluteDirectoryNoFollow(filepath.Dir(source), false)
	if err != nil {
		return "", err
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), filepath.Base(source), unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	input := os.NewFile(uintptr(fd), source)
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("retirement archive refuses nonregular file %s", source)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return "", err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(output, hash), input)
	closeErr := output.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func retireCaptureTree(source, destination string, include func(string) bool, hashes map[string]string, prefix string) error {
	root, err := openAbsoluteDirectoryNoFollow(source, false)
	if err != nil {
		return err
	}
	defer root.Close()
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("retirement archive refuses symlink %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !include(filepath.ToSlash(relative)) {
			return nil
		}
		archivePath := filepath.ToSlash(filepath.Join(prefix, relative))
		digest, err := retireCaptureFile(path, filepath.Join(destination, relative))
		if err != nil {
			return err
		}
		hashes[archivePath] = digest
		return nil
	})
}

type retireArchiveManifest struct {
	Version    int               `json:"version"`
	Repository string            `json:"repository"`
	Branch     string            `json:"branch"`
	SourceSHA  string            `json:"source_sha"`
	RetiredRef string            `json:"retired_ref"`
	ClaimID    string            `json:"claim_id"`
	Files      map[string]string `json:"files"`
}

func retirePublishArchive(ctx context.Context, home, remote string, result *RetireResult) error {
	claim, _, err := retireReadClaim(home, result.Worktree)
	if err != nil {
		return err
	}
	if claim.ClaimID != result.ClaimID || claim.Repository != result.Repository || claim.Branch != result.Branch {
		return fmt.Errorf("retirement archive claim identity changed")
	}
	working, err := os.MkdirTemp("", "wb-retirement-archive-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(working)
	if _, err := git(ctx, working, "init", "--initial-branch=main"); err != nil {
		return err
	}
	if _, err := git(ctx, working, "config", "user.name", "WB Retirement"); err != nil {
		return err
	}
	if _, err := git(ctx, working, "config", "user.email", "wb-retirement@localhost"); err != nil {
		return err
	}
	hashes := map[string]string{}
	worktreeMeta := filepath.Join(result.Worktree, ".worktree.md")
	if digest, err := retireCaptureFile(worktreeMeta, filepath.Join(working, "worktree", ".worktree.md")); err != nil {
		return fmt.Errorf("capture worktree metadata: %w", err)
	} else {
		hashes["worktree/.worktree.md"] = digest
	}
	local := filepath.Join(result.Worktree, ".wb", "local")
	if err := retireCaptureTree(local, filepath.Join(working, "worktree", ".wb", "local"), func(string) bool { return true }, hashes, "worktree/.wb/local"); err != nil {
		return fmt.Errorf("capture local Work Log: %w", err)
	}
	projection := filepath.Join(result.Worktree, ".wb-worklog", "recovery.json")
	if digest, err := retireCaptureFile(projection, filepath.Join(working, "worktree", ".wb-worklog", "recovery.json")); err != nil {
		return fmt.Errorf("capture Work Log projection: %w", err)
	} else {
		hashes["worktree/.wb-worklog/recovery.json"] = digest
	}
	run := filepath.Join(home, "worklogs", result.EffortID, "runs", result.RunID)
	reportName, err := finalizeReportFileName(result.Task, result.Repository)
	if err != nil {
		return err
	}
	includeRun := func(path string) bool {
		if path == "run.json" || path == "original-prompt.json" || strings.HasPrefix(path, "original-prompt.") {
			return true
		}
		parts := strings.Split(path, "/")
		if len(parts) < 2 {
			return true // run-scoped index, prompt, or migration record
		}
		if parts[0] == "claims" || parts[0] == "terminals" || parts[0] == "cleanups" {
			return parts[1] == result.ClaimID+".json"
		}
		if parts[0] == "corrections" || parts[0] == "dirty-discard" {
			return parts[1] == result.ClaimID
		}
		if parts[0] == "reports" {
			return parts[1] == reportName
		}
		return false
	}
	if err := retireCaptureTree(run, filepath.Join(working, "worklog", "run"), includeRun, hashes, "worklog/run"); err != nil {
		return fmt.Errorf("capture private Work Log run: %w", err)
	}
	for _, mandatory := range []string{"worklog/run/run.json", "worklog/run/claims/" + result.ClaimID + ".json", "worklog/run/terminals/" + result.ClaimID + ".json"} {
		if hashes[mandatory] == "" {
			return fmt.Errorf("missing mandatory Work Log file %s", mandatory)
		}
	}
	if claim.PromptArchive != "" {
		if filepath.Base(claim.PromptArchive) != claim.PromptArchive || hashes["worklog/run/"+claim.PromptArchive] != claim.PromptDigest {
			return fmt.Errorf("private prompt archive does not match immutable claim digest")
		}
	}
	terminal, err := readWorkLogTerminalRecordReadOnly(home, result.Worktree)
	if err != nil {
		return err
	}
	if terminal == nil || terminal.ClaimID != result.ClaimID || terminal.FinalCommit != result.SourceSHA {
		return fmt.Errorf("retirement terminal does not bind exact source commit")
	}
	if terminal.FinalizeReport != nil && terminal.FinalizeReport.ReportPath != "" {
		if filepath.Clean(terminal.FinalizeReport.ReportPath) != filepath.Join(run, "reports", reportName) || hashes["worklog/run/reports/"+reportName] == "" {
			return fmt.Errorf("missing referenced private Work Log report")
		}
	}
	outbox := filepath.Join(home, "worklogs", result.EffortID, "outbox")
	if err := retireCaptureTree(outbox, filepath.Join(working, "worklog", "outbox"), func(path string) bool { return strings.HasPrefix(path, result.RunID+"-"+result.ClaimID+"-") }, hashes, "worklog/outbox"); err != nil {
		return fmt.Errorf("capture Work Log outbox: %w", err)
	}
	manifest := retireArchiveManifest{Version: 1, Repository: result.Repository, Branch: result.Branch, SourceSHA: result.SourceSHA, RetiredRef: result.RetiredRef, ClaimID: result.ClaimID, Files: hashes}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(working, "retirement.json"), append(body, '\n'), 0o600); err != nil {
		return err
	}
	ref := "refs/heads/" + result.ArchiveRef
	existing, err := retireRemoteSHA(ctx, result.Canonical, remote, ref)
	if err != nil {
		return err
	}
	if existing != "" {
		if err := retireVerifyArchive(ctx, working, remote, ref, existing, manifest); err != nil {
			return err
		}
		result.ArchiveSHA, result.Phase = existing, "archive_published"
		return nil
	}
	if _, err := git(ctx, working, "add", "-A"); err != nil {
		return err
	}
	if _, err := git(ctx, working, "commit", "-m", "Retire "+result.Repository+" "+result.Branch+" at "+result.SourceSHA); err != nil {
		return err
	}
	sha, err := git(ctx, working, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if _, err := git(ctx, working, "push", "--force-with-lease="+ref+":", remote, "HEAD:"+ref); err != nil {
		return fmt.Errorf("publish private Work Log archive: %w", err)
	}
	observed, err := retireRemoteSHA(ctx, result.Canonical, remote, ref)
	if err != nil || observed != sha {
		return fmt.Errorf("private archive ref verification failed: %w", err)
	}
	if err := retireVerifyArchive(ctx, working, remote, ref, sha, manifest); err != nil {
		return err
	}
	result.ArchiveSHA, result.Phase = sha, "archive_published"
	return nil
}

func retireVerifyArchive(ctx context.Context, working, remote, ref, expectedSHA string, expected retireArchiveManifest) error {
	if _, err := git(ctx, working, "fetch", "--no-tags", remote, ref); err != nil {
		return err
	}
	sha, err := git(ctx, working, "rev-parse", "FETCH_HEAD")
	if err != nil || sha != expectedSHA {
		return fmt.Errorf("private archive commit changed: %w", err)
	}
	sizeText, err := git(ctx, working, "cat-file", "-s", "FETCH_HEAD:retirement.json")
	if err != nil {
		return err
	}
	size, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil || size > 16<<20 {
		return fmt.Errorf("private archive manifest exceeds verification limit")
	}
	body, err := retireGitBytes(ctx, working, "show", "FETCH_HEAD:retirement.json")
	if err != nil {
		return err
	}
	var actual retireArchiveManifest
	if err := json.Unmarshal(body, &actual); err != nil {
		return err
	}
	if actual.Version != expected.Version || actual.Repository != expected.Repository || actual.Branch != expected.Branch || actual.SourceSHA != expected.SourceSHA || actual.RetiredRef != expected.RetiredRef || actual.ClaimID != expected.ClaimID || len(actual.Files) != len(expected.Files) {
		return fmt.Errorf("private archive manifest identity mismatch")
	}
	paths := make([]string, 0, len(expected.Files))
	for path, hash := range expected.Files {
		if actual.Files[path] != hash {
			return fmt.Errorf("private archive manifest file mismatch %s", path)
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	tree, err := git(ctx, working, "ls-tree", "-r", "-z", "--name-only", "FETCH_HEAD")
	if err != nil {
		return err
	}
	actualPaths := strings.Split(tree, "\x00")
	var listed []string
	for _, path := range actualPaths {
		if path != "" {
			listed = append(listed, path)
		}
	}
	if len(listed) != len(paths)+1 {
		return fmt.Errorf("private archive has unlisted files")
	}
	for _, path := range listed {
		if path != "retirement.json" && expected.Files[path] == "" {
			return fmt.Errorf("private archive has unlisted file %s", path)
		}
	}
	for _, path := range paths {
		value, err := retireGitObjectSHA(ctx, working, "FETCH_HEAD:"+path)
		if err != nil {
			return fmt.Errorf("private archive file missing %s: %w", path, err)
		}
		if value != expected.Files[path] {
			return fmt.Errorf("private archive file digest mismatch %s", path)
		}
	}
	return nil
}

func retireGitObjectSHA(ctx context.Context, directory, object string) (string, error) {
	command := exec.CommandContext(ctx, "git", "-C", directory, "show", object)
	pipe, err := command.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := command.Start(); err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, pipe)
	waitErr := command.Wait()
	if copyErr != nil {
		return "", copyErr
	}
	if waitErr != nil {
		return "", waitErr
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func retireGitBytes(ctx context.Context, directory string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, args...)...)
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("read archive Git object: %w", err)
	}
	return output, nil
}
