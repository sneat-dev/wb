package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/wbhome"
	"golang.org/x/sys/unix"
)

// ListOptions selects WB-managed task worktrees and optional GitHub PR state.
type ListOptions struct {
	ProjectsRoot string
	Task         string
	Base         string
	// Filter narrows the inventory to candidates whose owner/repository slug
	// (or, for a candidate that cannot be identified that cleanly, whatever
	// raw path-derived identity is available) contains this substring — the
	// same "only repos whose org/name contains this substring" semantics as
	// the root --filter flag elsewhere in WB. An empty Filter matches
	// everything, exactly like today. Filtering happens before a candidate's
	// diagnostic or result is retained, so a candidate outside the selection
	// can neither appear in the report nor influence it.
	Filter string
	GitHub bool
}

// PullRequest is the GitHub evidence used to decide whether a branch is safe
// to clean up. HeadSHA must match the current branch tip.
type PullRequest struct {
	Number  int        `json:"number"`
	URL     string     `json:"url"`
	State   string     `json:"state"`
	Base    string     `json:"base"`
	HeadSHA string     `json:"head_sha"`
	Merged  *time.Time `json:"merged_at,omitempty"`
}

// ListResult describes one linked checkout below the WB task hierarchy.
type ListResult struct {
	Task               string       `json:"task"`
	Repository         string       `json:"repository"`
	CanonicalDir       string       `json:"canonical_dir"`
	WorktreeDir        string       `json:"worktree_dir"`
	WorktreesRoot      string       `json:"worktrees_root"`
	Branch             string       `json:"branch"`
	Base               string       `json:"base"`
	HeadSHA            string       `json:"head_sha"`
	RemoteHeadSHA      string       `json:"remote_head_sha,omitempty"`
	RemoteTargetSHA    string       `json:"remote_target_sha,omitempty"`
	IntegratedAtOrigin bool         `json:"integrated_at_origin"`
	Clean              bool         `json:"clean"`
	LocallyMerged      bool         `json:"locally_merged"`
	Locked             bool         `json:"locked"`
	LastCommit         time.Time    `json:"last_commit"`
	OpenPullRequest    *PullRequest `json:"open_pull_request,omitempty"`
	MergedPullRequest  *PullRequest `json:"merged_pull_request,omitempty"`
}

// ListDiagnostic describes a malformed task-layout candidate that was skipped
// without hiding valid sibling worktrees. It is intentionally separate from
// ListResult so cleanup can never mistake an unvalidated path for a safe
// linked checkout. WorktreesRoot is carried alongside Task so a diagnostic
// can be matched back to the exact coordinated task it belongs to even when
// more than one resolver-recognized layout is being read at once (see
// wbhome.Resolve) — Task name alone is not always unique across layouts.
type ListDiagnostic struct {
	Task          string `json:"task,omitempty"`
	WorktreesRoot string `json:"worktrees_root,omitempty"`
	Path          string `json:"path"`
	Message       string `json:"message"`
}

// LifecycleArtifact is WB-owned control-plane state, never a user worktree
// candidate. Active secure stages are transient under the task lock. Retired
// stages are identity-bound quarantine evidence: a later create may reclaim
// one only when it is still the same empty directory. Inventory reports the
// classification but cleanup must never reinterpret or delete it as a legacy
// dot-prefixed repository checkout.
type LifecycleArtifact struct {
	Task          string `json:"task"`
	WorktreesRoot string `json:"worktrees_root"`
	Path          string `json:"path"`
	Kind          string `json:"kind"`
	State         string `json:"state"`
	Disposition   string `json:"disposition"`
	Eligible      bool   `json:"eligible"`
	Applied       bool   `json:"applied"`
	ArchivePath   string `json:"archive_path,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// ListOutcome preserves the valid local inventory while exposing every
// deterministic malformed-candidate diagnostic encountered during scanning.
type ListOutcome struct {
	SchemaVersion int                 `json:"schema_version"`
	Results       []ListResult        `json:"results"`
	Diagnostics   []ListDiagnostic    `json:"diagnostics,omitempty"`
	Artifacts     []LifecycleArtifact `json:"artifacts,omitempty"`
}

// CleanupOptions controls planning and removal of merged WB tasks.
type CleanupOptions struct {
	ProjectsRoot string
	Task         string
	Base         string
	// Filter narrows both which candidates are validated and which are acted
	// on to those whose owner/repository slug contains this substring — see
	// ListOptions.Filter. An empty Filter matches everything, preserving
	// today's behavior exactly.
	Filter       string
	AllMerged    bool
	Apply        bool
	DeleteRemote bool
	OlderThan    time.Duration
	ReportDir    string
	Now          func() time.Time
	// beforeCleanupLocks is a test-only seam before cleanup opens and locks
	// task directories. It exercises substituted task hierarchy rejection.
	beforeCleanupLocks func()
	// beforeCleanupWorktreeRemoval is a test-only seam after reinspection and
	// before Git removes a worktree. It proves the held descriptor identity is
	// reauthorized immediately before destructive removal.
	beforeCleanupWorktreeRemoval func(worktree string)
	// afterCleanupWorktreeRemoval simulates a crash/failure after Git removed
	// the checkout but before the exact local branch deletion. The durable
	// lifecycle backlog must make the next identical cleanup resumable.
	afterCleanupWorktreeRemoval func(worktree string) error
	// beforeCleanupNetworkBranchOperation is a test-only seam after cleanup's
	// final pre-network authorization. It proves a substituted worktree blocks
	// the optional remote-branch deletion as well as local removal.
	beforeCleanupNetworkBranchOperation func(worktree string)
	// afterCleanupGitAuthorization is a test-only seam after cleanup retains
	// its canonical/worktree descriptors and immediately before a Git child
	// consumes them. It proves late lexical substitutions cannot redirect Git.
	afterCleanupGitAuthorization func(operation string)
	// afterCleanupParentAuthorization is a test-only seam after cleanup has
	// retained and authorized an empty owner directory, immediately before it
	// is atomically retired. It proves a successor cannot be unlinked by a
	// stale verify-then-remove sequence.
	afterCleanupParentAuthorization func(parent string)
}

// CleanupResult records one repository's cleanup decision and outcome.
type CleanupResult struct {
	ListResult
	Eligible      bool   `json:"eligible"`
	Applied       bool   `json:"applied"`
	RemoteDeleted bool   `json:"remote_deleted"`
	WorktreeGone  bool   `json:"worktree_gone"`
	BranchDeleted bool   `json:"branch_deleted"`
	BacklogID     string `json:"backlog_id,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// CleanupOutcome contains the decisions plus the durable audit report written
// before any destructive apply.
//
// Diagnostics never abort a run. A malformed candidate inside the selection
// (see CleanupOptions.Filter) is skipped and reported here as a warning, and
// blocks eligibility only for its own coordinated task — the same
// all-or-nothing unit blockUnsafeTasks already applies to an unclean, locked,
// or unmerged sibling. Every other task in the run proceeds normally.
type CleanupOutcome struct {
	Results     []CleanupResult     `json:"results"`
	ReportPath  string              `json:"report_path,omitempty"`
	Diagnostics []ListDiagnostic    `json:"diagnostics,omitempty"`
	Artifacts   []LifecycleArtifact `json:"artifacts,omitempty"`
}

type cleanupReport struct {
	GeneratedAt  time.Time           `json:"generated_at"`
	Phase        string              `json:"phase"`
	Task         string              `json:"task,omitempty"`
	Filter       string              `json:"filter,omitempty"`
	AllMerged    bool                `json:"all_merged"`
	Apply        bool                `json:"apply"`
	DeleteRemote bool                `json:"delete_remote"`
	OlderThan    string              `json:"older_than"`
	Results      []CleanupResult     `json:"results"`
	Diagnostics  []ListDiagnostic    `json:"diagnostics,omitempty"`
	Artifacts    []LifecycleArtifact `json:"artifacts,omitempty"`
}

type cleanupTaskHandle struct {
	worktreesPath string
	taskPath      string
	worktrees     *os.File
	task          *os.File
	lock          operationLock
}

type cleanupLifecycleArtifactHandle struct {
	index     int
	name      string
	directory *os.File
}

func prepareCleanupLifecycleArtifacts(
	home string,
	task *cleanupTaskHandle,
	taskKey string,
	artifacts []LifecycleArtifact,
) (*os.File, string, []cleanupLifecycleArtifactHandle, error) {
	handles := make([]cleanupLifecycleArtifactHandle, 0)
	for index := range artifacts {
		artifact := artifacts[index]
		if !artifact.Eligible || cleanupTaskKey(artifact.WorktreesRoot, artifact.Task) != taskKey {
			continue
		}
		name := filepath.Base(artifact.Path)
		if _, _, recognized := lifecycleArtifactName(name); !recognized {
			closeCleanupLifecycleArtifacts(handles)
			return nil, "", nil, fmt.Errorf("cleanup artifact %s lost its reserved WB identity", artifact.Path)
		}
		fd, err := unix.Openat(int(task.task.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if err != nil {
			closeCleanupLifecycleArtifacts(handles)
			return nil, "", nil, fmt.Errorf("open cleanup lifecycle artifact %s: %w", artifact.Path, err)
		}
		directory := os.NewFile(uintptr(fd), "wb-cleanup-lifecycle-artifact")
		if directory == nil {
			_ = unix.Close(fd)
			closeCleanupLifecycleArtifacts(handles)
			return nil, "", nil, fmt.Errorf("wrap cleanup lifecycle artifact %s", artifact.Path)
		}
		if !directoryStillMatches(artifact.Path, directory) {
			_ = directory.Close()
			closeCleanupLifecycleArtifacts(handles)
			return nil, "", nil, fmt.Errorf("cleanup lifecycle artifact path changed: %s", artifact.Path)
		}
		empty, err := directoryEmpty(directory)
		if err != nil || !empty {
			_ = directory.Close()
			closeCleanupLifecycleArtifacts(handles)
			if err != nil {
				return nil, "", nil, fmt.Errorf("reinspect cleanup lifecycle artifact %s: %w", artifact.Path, err)
			}
			return nil, "", nil, fmt.Errorf("cleanup lifecycle artifact %s became non-empty; retained as explicit cleanup backlog", artifact.Path)
		}
		handles = append(handles, cleanupLifecycleArtifactHandle{index: index, name: name, directory: directory})
	}
	if len(handles) == 0 {
		return nil, "", nil, nil
	}
	archiveID := lifecycleBacklogID(ListResult{
		Task: task.taskPath, WorktreesRoot: task.worktreesPath, WorktreeDir: task.taskPath,
	}, "stage-archive")
	archivePath := filepath.Join(home, "reports", "worktree-cleanup", "stage-archive",
		filepath.Base(task.taskPath)+"-"+archiveID[:16])
	archive, err := openAbsoluteDirectoryNoFollow(archivePath, true)
	if err != nil {
		closeCleanupLifecycleArtifacts(handles)
		return nil, "", nil, fmt.Errorf("open cleanup lifecycle artifact archive: %w", err)
	}
	return archive, archivePath, handles, nil
}

func archiveCleanupLifecycleArtifacts(
	task *cleanupTaskHandle,
	archive *os.File,
	archivePath string,
	handles []cleanupLifecycleArtifactHandle,
	artifacts []LifecycleArtifact,
) error {
	for _, handle := range handles {
		artifact := &artifacts[handle.index]
		if err := task.validate(); err != nil {
			return err
		}
		empty, err := directoryEmpty(handle.directory)
		if err != nil || !empty {
			if err != nil {
				return fmt.Errorf("reinspect cleanup lifecycle artifact %s at retirement boundary: %w", artifact.Path, err)
			}
			return fmt.Errorf("cleanup lifecycle artifact %s became non-empty at retirement boundary; retained as explicit cleanup backlog", artifact.Path)
		}
		moved, err := moveExpectedDirectoryNoReplace(task.task, handle.name, archive, handle.name, handle.directory, nil)
		if err != nil {
			if moved != nil {
				_ = moved.Close()
			}
			return fmt.Errorf("descriptor-safely archive cleanup lifecycle artifact %s: %w", artifact.Path, err)
		}
		_ = moved.Close()
		artifact.State = "archived"
		artifact.Disposition = "archived_empty_stage"
		artifact.Applied = true
		artifact.ArchivePath = filepath.Join(archivePath, handle.name)
		artifact.Reason = "recognized empty WB-owned stage archived outside the active task"
	}
	return nil
}

func closeCleanupLifecycleArtifacts(handles []cleanupLifecycleArtifactHandle) {
	for _, handle := range handles {
		if handle.directory != nil {
			_ = handle.directory.Close()
		}
	}
}

func (handle *cleanupTaskHandle) validate() error {
	if !directoryStillMatches(handle.worktreesPath, handle.worktrees) {
		return fmt.Errorf("cleanup worktrees root path changed: %s", handle.worktreesPath)
	}
	if !directoryStillMatches(handle.taskPath, handle.task) {
		return fmt.Errorf("cleanup task path changed: %s", handle.taskPath)
	}
	return nil
}

func (handle *cleanupTaskHandle) close() {
	if handle.task != nil {
		_ = handle.task.Close()
	}
	if handle.worktrees != nil {
		_ = handle.worktrees.Close()
	}
}

type cleanupWorktreeHandle struct {
	task         *cleanupTaskHandle
	parentPath   string
	parentName   string
	parent       *os.File
	closeParent  bool
	worktreePath string
	worktree     *os.File
}

func (handle *cleanupWorktreeHandle) validate() error {
	if err := handle.task.validate(); err != nil {
		return err
	}
	if !directoryStillMatches(handle.parentPath, handle.parent) {
		return fmt.Errorf("cleanup worktree parent path changed: %s", handle.parentPath)
	}
	if !directoryStillMatches(handle.worktreePath, handle.worktree) {
		return fmt.Errorf("cleanup worktree path changed: %s", handle.worktreePath)
	}
	return nil
}

func (handle *cleanupWorktreeHandle) removeEmptyParent(afterAuthorization func(string)) error {
	if !handle.closeParent {
		return nil // Legacy <task>/<repository> layout has no owner directory.
	}
	if err := handle.task.validate(); err != nil {
		return err
	}
	if !directoryStillMatches(handle.parentPath, handle.parent) {
		return fmt.Errorf("cleanup worktree parent path changed before removal: %s", handle.parentPath)
	}
	if afterAuthorization != nil {
		afterAuthorization(handle.parentPath)
	}
	// Keep an empty owner directory in place. It is a harmless, reusable
	// destination for the next worktree and avoids both verify-then-unlink and
	// an ever-growing owner-retirement namespace. Reauthorize once more after
	// the test seam so a replacement is surfaced, never removed or retired.
	if !directoryStillMatches(handle.parentPath, handle.parent) {
		return fmt.Errorf("cleanup worktree parent path changed before retention: %s", handle.parentPath)
	}
	return nil
}

func (handle *cleanupWorktreeHandle) close() {
	if handle.worktree != nil {
		_ = handle.worktree.Close()
	}
	if handle.closeParent && handle.parent != nil {
		_ = handle.parent.Close()
	}
}

type githubPullRequest struct {
	Number      int        `json:"number"`
	URL         string     `json:"url"`
	State       string     `json:"state"`
	BaseRefName string     `json:"baseRefName"`
	HeadRefOID  string     `json:"headRefOid"`
	MergedAt    *time.Time `json:"mergedAt"`
}

// List inspects real Git worktrees. It stays local unless GitHub is requested.
// Callers that present diagnostics should use ListWithDiagnostics.
func List(ctx context.Context, options ListOptions) ([]ListResult, error) {
	outcome, err := ListWithDiagnostics(ctx, options)
	if err != nil {
		return nil, err
	}
	return outcome.Results, nil
}

// ListWithDiagnostics inventories every resolver-recognized layout. It never
// descends below a Git root, which prevents ordinary repository directories
// such as .claude, .github, source, and generated trees from being re-read as
// task-level repositories.
func ListWithDiagnostics(ctx context.Context, options ListOptions) (ListOutcome, error) {
	projectsRoot, task, base, filter, err := normalizeListOptions(options)
	if err != nil {
		return ListOutcome{}, err
	}
	resolution, err := wbhome.Resolve(projectsRoot)
	if err != nil {
		return ListOutcome{}, err
	}
	outcome := ListOutcome{SchemaVersion: 1}
	for _, layout := range resolution.Read {
		results, diagnostics, artifacts, listErr := listLayout(ctx, projectsRoot, layout, task, base, filter, options.GitHub)
		if listErr != nil {
			return ListOutcome{}, listErr
		}
		outcome.Results = append(outcome.Results, results...)
		outcome.Diagnostics = append(outcome.Diagnostics, diagnostics...)
		outcome.Artifacts = append(outcome.Artifacts, artifacts...)
	}
	sort.Slice(outcome.Results, func(i, j int) bool {
		if outcome.Results[i].Task == outcome.Results[j].Task {
			if outcome.Results[i].Repository == outcome.Results[j].Repository {
				return outcome.Results[i].WorktreeDir < outcome.Results[j].WorktreeDir
			}
			return outcome.Results[i].Repository < outcome.Results[j].Repository
		}
		return outcome.Results[i].Task < outcome.Results[j].Task
	})
	sort.Slice(outcome.Diagnostics, func(i, j int) bool {
		if outcome.Diagnostics[i].Task == outcome.Diagnostics[j].Task {
			return outcome.Diagnostics[i].Path < outcome.Diagnostics[j].Path
		}
		return outcome.Diagnostics[i].Task < outcome.Diagnostics[j].Task
	})
	sort.Slice(outcome.Artifacts, func(i, j int) bool { return outcome.Artifacts[i].Path < outcome.Artifacts[j].Path })
	return outcome, nil
}

func listLayout(
	ctx context.Context,
	projectsRoot string,
	layout wbhome.Layout,
	task, base, filter string,
	withGitHub bool,
) ([]ListResult, []ListDiagnostic, []LifecycleArtifact, error) {
	taskEntries, err := os.ReadDir(layout.WorktreesRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read worktree tasks under %s: %w", layout.WorktreesRoot, err)
	}
	results := make([]ListResult, 0)
	diagnostics := make([]ListDiagnostic, 0)
	artifacts := make([]LifecycleArtifact, 0)
	for _, taskEntry := range taskEntries {
		if !taskEntry.IsDir() || strings.HasPrefix(taskEntry.Name(), ".") || (task != "" && taskEntry.Name() != task) {
			continue
		}
		if !validSafeSegment(taskEntry.Name()) {
			// A malformed task directory name carries no repository identity to
			// weigh against --filter, and the exact-match task argument already
			// scopes which task directories are even looked at above. Report it
			// unconditionally rather than guess at scope.
			diagnostics = append(diagnostics, listDiagnostic(layout.WorktreesRoot, taskEntry.Name(), filepath.Join(layout.WorktreesRoot, taskEntry.Name()), "invalid task directory name"))
			continue
		}
		taskRoot := filepath.Join(layout.WorktreesRoot, taskEntry.Name())
		_, lockErr := os.Stat(filepath.Join(taskRoot, ".lock"))
		locked := lockErr == nil
		if lockErr != nil && !errors.Is(lockErr, os.ErrNotExist) {
			return nil, nil, nil, fmt.Errorf("inspect task lock %s: %w", taskRoot, lockErr)
		}
		entries, readErr := os.ReadDir(taskRoot)
		if readErr != nil {
			return nil, nil, nil, fmt.Errorf("read task %s: %w", taskEntry.Name(), readErr)
		}
		for _, entry := range entries {
			candidate := filepath.Join(taskRoot, entry.Name())
			if artifact, internal := inspectLifecycleArtifact(layout.WorktreesRoot, taskEntry.Name(), candidate, entry); internal {
				artifacts = append(artifacts, artifact)
				continue
			}
			if !entry.IsDir() {
				continue
			}
			if isGitRoot(ctx, candidate) {
				result, inspectErr := inspectLifecycleWorktree(ctx, projectsRoot, layout, taskEntry.Name(), candidate, base, withGitHub, locked)
				if inspectErr != nil {
					if filterMatches(filter, inspectErrorFilterCandidates("", candidate, entry.Name(), inspectErr)...) {
						diagnostics = append(diagnostics, listDiagnosticForInspectError(layout.WorktreesRoot, taskEntry.Name(), candidate, "", inspectErr))
					}
					continue
				}
				if !filterMatches(filter, result.Repository) {
					continue
				}
				results = append(results, result)
				// A repository boundary is terminal. Never inspect its source or
				// tool directories as candidate repositories.
				continue
			}
			// Metadata directories are not candidate owners or legacy repository
			// names, but a valid registered worktree itself may intentionally
			// start with a dot. Detect the Git boundary before ignoring ordinary
			// dot directories.
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			if !validSafeSegment(entry.Name()) {
				if filterMatches(filter, candidate, entry.Name()) {
					diagnostics = append(diagnostics, listDiagnostic(layout.WorktreesRoot, taskEntry.Name(), candidate, "invalid owner or legacy repository directory name"))
				}
				continue
			}
			nested, nestedErr := os.ReadDir(candidate)
			if nestedErr != nil {
				if filterMatches(filter, candidate, entry.Name()) {
					diagnostics = append(diagnostics, listDiagnostic(layout.WorktreesRoot, taskEntry.Name(), candidate, fmt.Sprintf("read candidate directory: %v", nestedErr)))
				}
				continue
			}
			for _, repositoryEntry := range nested {
				if !repositoryEntry.IsDir() {
					continue
				}
				repositoryPath := filepath.Join(candidate, repositoryEntry.Name())
				slug := entry.Name() + "/" + repositoryEntry.Name()
				if isGitRoot(ctx, repositoryPath) {
					result, inspectErr := inspectLifecycleWorktree(ctx, projectsRoot, layout, taskEntry.Name(), repositoryPath, base, withGitHub, locked)
					if inspectErr != nil {
						if filterMatches(filter, inspectErrorFilterCandidates(entry.Name(), repositoryPath, slug, inspectErr)...) {
							diagnostics = append(diagnostics, listDiagnosticForInspectError(layout.WorktreesRoot, taskEntry.Name(), repositoryPath, entry.Name(), inspectErr))
						}
						continue
					}
					if !filterMatches(filter, result.Repository) {
						continue
					}
					results = append(results, result)
					continue
				}
				if strings.HasPrefix(repositoryEntry.Name(), ".") {
					continue
				}
				if !filterMatches(filter, repositoryPath, slug) {
					continue
				}
				if !validSafeSegment(repositoryEntry.Name()) {
					diagnostics = append(diagnostics, listDiagnostic(layout.WorktreesRoot, taskEntry.Name(), repositoryPath, "invalid repository directory name"))
					continue
				}
				diagnostics = append(diagnostics, listDiagnostic(layout.WorktreesRoot, taskEntry.Name(), repositoryPath, "candidate is not a Git worktree root"))
			}
		}
	}
	return results, diagnostics, artifacts, nil
}

func inspectLifecycleArtifact(worktreesRoot, task, path string, entry os.DirEntry) (LifecycleArtifact, bool) {
	name := entry.Name()
	kind, state, recognized := lifecycleArtifactName(name)
	if !recognized {
		return LifecycleArtifact{}, false
	}
	artifact := LifecycleArtifact{Task: task, WorktreesRoot: worktreesRoot, Path: path,
		Kind: kind, State: state, Disposition: "cleanup_backlog"}
	if (state == "staging" && !isWorktreeStagingDirectory(name)) ||
		(state == "quarantined" && !isRetiredWorktreeStagingDirectory(name)) {
		artifact.Reason = "reserved WB stage name has no collision-resistant identity suffix"
		return artifact, true
	}
	if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
		artifact.Reason = "reserved WB stage entry is not a no-follow directory"
		return artifact, true
	}
	directory, err := openAbsoluteDirectoryNoFollow(path, false)
	if err != nil {
		artifact.Reason = "cannot open reserved WB stage without following links: " + err.Error()
		return artifact, true
	}
	empty, emptyErr := directoryEmpty(directory)
	_ = directory.Close()
	if emptyErr != nil {
		artifact.Reason = "cannot inspect reserved WB stage contents: " + emptyErr.Error()
		return artifact, true
	}
	if !empty {
		artifact.Reason = "reserved WB stage is non-empty and requires audited recovery before task cleanup"
		return artifact, true
	}
	artifact.Eligible = true
	artifact.Disposition = "archive_empty_stage"
	artifact.Reason = "recognized empty WB-owned stage will be descriptor-safely archived on apply"
	return artifact, true
}

func lifecycleArtifactName(name string) (kind, state string, recognized bool) {
	switch {
	case strings.HasPrefix(name, ".wb-stage-"):
		return "secure_worktree_stage", "staging", true
	case strings.HasPrefix(name, ".wb-retired-stage-"):
		return "secure_worktree_stage", "quarantined", true
	default:
		return "", "", false
	}
}

func listDiagnostic(worktreesRoot, task, path, message string) ListDiagnostic {
	return ListDiagnostic{Task: task, WorktreesRoot: worktreesRoot, Path: path, Message: message}
}

// listDiagnosticForInspectError builds the diagnostic for a candidate that
// failed inspectLifecycleWorktree. A RepositoryRenameMismatchError gets a
// richer message — the mismatch's own path repository and canonical
// repository already give the reader "expected repo" and "actual repo"; this
// adds the path and the likely cause so the warning is actionable without
// reading source. owner is the on-disk owner directory name when known (the
// <task>/<owner>/<repository> layout); it is empty for the legacy
// <task>/<repository> layout, which never produces this mismatch type.
func listDiagnosticForInspectError(worktreesRoot, task, candidate, owner string, err error) ListDiagnostic {
	var mismatch *RepositoryRenameMismatchError
	if errors.As(err, &mismatch) {
		return listDiagnostic(worktreesRoot, task, candidate, fmt.Sprintf(
			"%s (likely cause: the canonical repository was renamed from %q to %q after this worktree was created; this is ordinary history, not corruption — wb does not reconcile it automatically, so re-register it with `wb worktree create` under the new name or remove it by hand once you have confirmed its branch is safe to lose)",
			mismatch.Error(), mismatch.PathRepository, mismatch.CanonicalRepository,
		))
	}
	return listDiagnostic(worktreesRoot, task, candidate, err.Error())
}

// inspectErrorFilterCandidates lists every identity string worth weighing
// against --filter for a candidate that failed inspectLifecycleWorktree: the
// full path and the raw on-disk slug, plus — for a repository rename
// mismatch specifically — the canonical (current) repository name too, so a
// filter naming either the old or the new identity reaches the diagnostic.
// owner is the on-disk owner directory name when known; pass "" for the
// legacy <task>/<repository> layout, which never produces this mismatch.
func inspectErrorFilterCandidates(owner, path, slug string, err error) []string {
	candidates := []string{path, slug}
	var mismatch *RepositoryRenameMismatchError
	if owner != "" && errors.As(err, &mismatch) {
		candidates = append(candidates, owner+"/"+mismatch.CanonicalRepository)
	}
	return candidates
}

// filterMatches reports whether at least one candidate identity string
// contains filter as a substring. An empty filter always matches, so an
// unfiltered call sees exactly today's behavior. Candidates may be a full
// path, a bare repository name, or an "owner/repository" slug — whatever
// identity is available at the point of the check; a malformed candidate
// often cannot offer more than that.
func filterMatches(filter string, candidates ...string) bool {
	if filter == "" {
		return true
	}
	for _, candidate := range candidates {
		if candidate != "" && strings.Contains(candidate, filter) {
			return true
		}
	}
	return false
}

func isGitRoot(ctx context.Context, path string) bool {
	root, err := git(ctx, path, "rev-parse", "--show-toplevel")
	return err == nil && filepath.Clean(root) == filepath.Clean(path)
}

// Cleanup plans or applies cleanup for one task or every safely merged task.
// A coordinated task is all-or-nothing: one unsafe repository blocks all of
// its worktrees.
func Cleanup(ctx context.Context, options CleanupOptions) (CleanupOutcome, error) {
	normalized, err := normalizeCleanupOptions(options)
	if err != nil {
		return CleanupOutcome{}, err
	}
	resolution, err := wbhome.Resolve(normalized.ProjectsRoot)
	if err != nil {
		return CleanupOutcome{}, err
	}
	// The report is a mutation too. Probe the platform capability before
	// creating its default directory or making any other apply-time change.
	if normalized.Apply {
		if err := requireGitFilesystemCapability(); err != nil {
			return CleanupOutcome{}, err
		}
	}
	now := normalized.Now()
	if normalized.ReportDir == "" && normalized.Apply {
		normalized.ReportDir = DefaultCleanupReportDir(resolution.Write.Home, now)
	}
	listed, err := ListWithDiagnostics(ctx, ListOptions{
		ProjectsRoot: normalized.ProjectsRoot,
		Task:         normalized.Task,
		Base:         normalized.Base,
		Filter:       normalized.Filter,
		GitHub:       true,
	})
	if err != nil {
		return CleanupOutcome{}, err
	}
	recognizedWorktreesRoots := make([]string, 0, len(resolution.Read))
	for _, layout := range resolution.Read {
		recognizedWorktreesRoots = append(recognizedWorktreesRoots, layout.WorktreesRoot)
	}
	backlog, err := loadResumableLifecycleBacklog(resolution.Write.Home, normalized.ProjectsRoot, recognizedWorktreesRoots, normalized.Task, normalized.Filter, "removed")
	if err != nil {
		return CleanupOutcome{}, err
	}
	// A malformed candidate never aborts the run — see blockDiagnosedTasks. It
	// is legitimate history (for example a renamed canonical repository, see
	// RepositoryRenameMismatchError), not evidence that anyone's work is at
	// risk, and one unreadable entry anywhere in the fleet must not deadlock
	// cleanup everywhere else. --filter (and the exact-match task argument
	// above) already scoped listed.Diagnostics to the current selection, so
	// every diagnostic here is one the caller asked to see.
	if normalized.Task != "" && len(listed.Results) == 0 && len(listed.Diagnostics) == 0 && len(listed.Artifacts) == 0 && len(backlog) == 0 {
		return CleanupOutcome{}, fmt.Errorf("WB worktree task %q was not found", normalized.Task)
	}

	results := make([]CleanupResult, len(listed.Results))
	for index, entry := range listed.Results {
		eligible, reason := cleanupEligibility(entry, normalized.OlderThan, now)
		results[index] = CleanupResult{ListResult: entry, Eligible: eligible, Reason: reason}
	}
	for _, record := range backlog {
		results = append(results, CleanupResult{ListResult: ListResult{
			Task: record.Task, Repository: record.Repository, CanonicalDir: record.CanonicalDir,
			WorktreeDir: record.WorktreeDir, WorktreesRoot: record.WorktreesRoot,
			Branch: record.Branch, Base: record.Base, HeadSHA: record.HeadSHA,
		}, Eligible: true, WorktreeGone: true, BacklogID: record.ID,
			Reason: "durable cleanup backlog awaiting exact local branch retirement"})
	}
	blockDiagnosedTasks(results, listed.Diagnostics)
	blockArtifactTasks(results, listed.Artifacts)
	blockUnsafeTasks(results)
	outcome := CleanupOutcome{Results: results, Diagnostics: listed.Diagnostics, Artifacts: listed.Artifacts}
	// A cleanup plan is read-only even when a caller supplies ReportDir. Audit
	// artifacts are created only for an apply attempt, after the platform
	// capability preflight has succeeded.
	if normalized.Apply && normalized.ReportDir != "" {
		outcome.ReportPath, err = writeCleanupReport(normalized, now, "planned", outcome.Results, outcome.Diagnostics, outcome.Artifacts)
		if err != nil {
			return outcome, err
		}
	}
	if !normalized.Apply {
		return outcome, nil
	}

	fail := func(cleanupErr error) (CleanupOutcome, error) {
		if normalized.ReportDir != "" {
			if _, reportErr := writeCleanupReport(normalized, now, "failed", outcome.Results, outcome.Diagnostics, outcome.Artifacts); reportErr != nil {
				return outcome, fmt.Errorf("%w; write failed cleanup report: %v", cleanupErr, reportErr)
			}
		}
		return outcome, cleanupErr
	}
	for backlogIndex := range backlog {
		if err := resumeLifecycleBacklog(ctx, resolution.Write.Home, &backlog[backlogIndex]); err != nil {
			return fail(err)
		}
		for resultIndex := range outcome.Results {
			if outcome.Results[resultIndex].BacklogID == backlog[backlogIndex].ID {
				outcome.Results[resultIndex].Applied = true
				outcome.Results[resultIndex].BranchDeleted = true
				outcome.Results[resultIndex].Reason = "resumed durable cleanup backlog"
			}
		}
	}
	// Hold the same per-task lock used by worktree creation across that task's
	// complete recheck-and-remove sequence. Complete one task, close all of its
	// retained descriptors, then move to the next: an --all-merged run must not
	// retain every task's lock and file descriptors for its entire duration.
	if normalized.beforeCleanupLocks != nil {
		normalized.beforeCleanupLocks()
	}
	for _, selection := range cleanupTaskSelections(outcome) {
		taskKey := cleanupTaskKey(selection.WorktreesRoot, selection.Task)
		if !cleanupTaskCanApply(outcome, taskKey) {
			continue
		}
		task, err := acquireCleanupTaskAt(selection.WorktreesRoot, selection.Task)
		if err != nil {
			return fail(err)
		}
		cleanupOutcome, cleanupErr := func() (CleanupOutcome, error) {
			defer func() {
				task.lock.release()
				task.close()
			}()
			// Corroborate every repository, exact remote target SHA, and private
			// Work Log before the first terminal write or Git deletion. The task
			// lock prevents another WB lifecycle operation from racing this phase;
			// every destructive step still repeats its local/network recheck below.
			for index := range outcome.Results {
				if !outcome.Results[index].Eligible || outcome.Results[index].BacklogID != "" || cleanupTaskKey(outcome.Results[index].WorktreesRoot, outcome.Results[index].Task) != taskKey {
					continue
				}
				refreshed, preflightErr := preflightCleanupRepository(ctx, normalized, now, task, outcome.Results[index], resolution.Write.Home)
				if preflightErr != nil {
					return fail(preflightErr)
				}
				outcome.Results[index].ListResult = refreshed
			}
			artifactArchive, artifactArchivePath, artifactHandles, artifactErr := prepareCleanupLifecycleArtifacts(
				resolution.Write.Home, task, taskKey, outcome.Artifacts,
			)
			if artifactErr != nil {
				return fail(artifactErr)
			}
			if artifactArchive != nil {
				defer func() { _ = artifactArchive.Close() }()
			}
			defer closeCleanupLifecycleArtifacts(artifactHandles)
			for index := range outcome.Results {
				if !outcome.Results[index].Eligible || outcome.Results[index].BacklogID != "" || cleanupTaskKey(outcome.Results[index].WorktreesRoot, outcome.Results[index].Task) != taskKey {
					continue
				}
				worktree, err := openCleanupWorktree(task, outcome.Results[index])
				if err != nil {
					return fail(err)
				}
				if err := worktree.validate(); err != nil {
					worktree.close()
					return fail(err)
				}
				refreshed, err := inspectLifecycleWorktree(
					ctx,
					normalized.ProjectsRoot,
					wbhome.Layout{WorktreesRoot: outcome.Results[index].WorktreesRoot},
					outcome.Results[index].Task,
					outcome.Results[index].WorktreeDir,
					normalized.Base,
					true,
					false, // The task is locked by this cleanup operation.
				)
				if err != nil {
					worktree.close()
					return fail(err)
				}
				if err := worktree.validate(); err != nil {
					worktree.close()
					return fail(err)
				}
				eligible, reason := cleanupEligibility(refreshed, normalized.OlderThan, now)
				if !eligible {
					worktree.close()
					return fail(fmt.Errorf("cleanup safety changed for %s: %s", refreshed.Repository, reason))
				}
				if refreshed.HeadSHA != outcome.Results[index].HeadSHA {
					worktree.close()
					return fail(fmt.Errorf("cleanup safety changed for %s: branch head moved", refreshed.Repository))
				}
				outcome.Results[index].ListResult = refreshed
				canonical, err := openCanonicalRepository(refreshed.CanonicalDir)
				if err != nil {
					worktree.close()
					return fail(fmt.Errorf("open cleanup canonical repository %s: %w", refreshed.CanonicalDir, err))
				}
				canonicalClosed := false
				closeCanonical := func() {
					if canonicalClosed {
						return
					}
					canonicalClosed = true
					canonical.close()
				}
				if err := canonical.validate(); err != nil {
					closeCanonical()
					worktree.close()
					return fail(fmt.Errorf("cleanup canonical repository changed before Git operations: %w", err))
				}
				if normalized.beforeCleanupWorktreeRemoval != nil {
					normalized.beforeCleanupWorktreeRemoval(refreshed.WorktreeDir)
				}
				// Git's worktree-remove command requires the registered lexical path
				// (it rejects descriptor aliases such as /dev/fd/N). Reauthorize that
				// spelling against the retained task/owner/worktree descriptors at the
				// last possible point; any substitution conservatively aborts before Git
				// can remove a checkout or its registration.
				if err := worktree.validate(); err != nil {
					closeCanonical()
					worktree.close()
					return fail(err)
				}
				// Archive the recoverable run record while every Git asset still
				// exists. Remote branch deletion is destructive too, so it must never
				// precede the durable terminal/outbox record.
				if err := sealWorkLogForRecycle(resolution.Write.Home, refreshed.WorktreeDir, refreshed.HeadSHA, "removed"); err != nil {
					closeCanonical()
					worktree.close()
					return fail(fmt.Errorf("seal work log before removing %s: %w", refreshed.WorktreeDir, err))
				}
				backlogRecord := newLifecycleBacklogRecord(normalized.ProjectsRoot, refreshed, "removed")
				if err := persistLifecycleBacklog(resolution.Write.Home, &backlogRecord, lifecycleStageSealed); err != nil {
					closeCanonical()
					worktree.close()
					return fail(err)
				}
				outcome.Results[index].BacklogID = backlogRecord.ID
				if normalized.DeleteRemote && refreshed.RemoteHeadSHA != "" {
					if err := persistLifecycleBacklog(resolution.Write.Home, &backlogRecord, lifecycleStageRetiringRemote); err != nil {
						closeCanonical()
						worktree.close()
						return fail(err)
					}
					if err := worktree.validate(); err != nil {
						closeCanonical()
						worktree.close()
						return fail(err)
					}
					if normalized.beforeCleanupNetworkBranchOperation != nil {
						normalized.beforeCleanupNetworkBranchOperation(refreshed.WorktreeDir)
					}
					if err := worktree.validate(); err != nil {
						closeCanonical()
						worktree.close()
						return fail(err)
					}
					if normalized.afterCleanupGitAuthorization != nil {
						normalized.afterCleanupGitAuthorization("delete remote branch")
					}
					if err := runSecureCleanupGitHelper(ctx, canonical, worktree.parent, worktree.worktree, worktree.parentPath, refreshed.WorktreeDir, "push", "--force-with-lease=refs/heads/"+refreshed.Branch+":"+refreshed.HeadSHA, "origin", ":refs/heads/"+refreshed.Branch); err != nil {
						closeCanonical()
						worktree.close()
						return fail(fmt.Errorf("delete remote branch %s at %s: %w", refreshed.Branch, refreshed.HeadSHA, err))
					}
					outcome.Results[index].RemoteDeleted = true
					if err := persistLifecycleBacklog(resolution.Write.Home, &backlogRecord, lifecycleStageRemoteRetired); err != nil {
						closeCanonical()
						worktree.close()
						return fail(err)
					}
				}
				if err := worktree.validate(); err != nil {
					closeCanonical()
					worktree.close()
					return fail(err)
				}
				if normalized.afterCleanupGitAuthorization != nil {
					normalized.afterCleanupGitAuthorization("remove worktree")
				}
				if err := persistLifecycleBacklog(resolution.Write.Home, &backlogRecord, lifecycleStageRemovingWorktree); err != nil {
					closeCanonical()
					worktree.close()
					return fail(err)
				}
				if err := runSecureCleanupGitHelper(ctx, canonical, worktree.parent, worktree.worktree, worktree.parentPath, refreshed.WorktreeDir, "worktree", "remove", refreshed.WorktreeDir); err != nil {
					closeCanonical()
					worktree.close()
					return fail(fmt.Errorf("remove worktree %s: %w", refreshed.WorktreeDir, err))
				}
				outcome.Results[index].WorktreeGone = true
				if err := persistLifecycleBacklog(resolution.Write.Home, &backlogRecord, lifecycleStageWorktreeRemoved); err != nil {
					closeCanonical()
					worktree.close()
					return fail(err)
				}
				if normalized.afterCleanupWorktreeRemoval != nil {
					if err := normalized.afterCleanupWorktreeRemoval(refreshed.WorktreeDir); err != nil {
						closeCanonical()
						worktree.close()
						return fail(fmt.Errorf("after worktree removal for %s: %w", refreshed.Repository, err))
					}
				}
				if err := task.validate(); err != nil {
					closeCanonical()
					worktree.close()
					return fail(err)
				}
				if normalized.afterCleanupGitAuthorization != nil {
					normalized.afterCleanupGitAuthorization("delete local branch")
				}
				if err := persistLifecycleBacklog(resolution.Write.Home, &backlogRecord, lifecycleStageRemovingLocalBranch); err != nil {
					closeCanonical()
					worktree.close()
					return fail(err)
				}
				if err := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "update-ref", "-d", "refs/heads/"+refreshed.Branch, refreshed.HeadSHA); err != nil {
					closeCanonical()
					worktree.close()
					return fail(fmt.Errorf("delete local branch %s at %s: %w", refreshed.Branch, refreshed.HeadSHA, err))
				}
				outcome.Results[index].BranchDeleted = true
				if err := worktree.removeEmptyParent(normalized.afterCleanupParentAuthorization); err != nil {
					closeCanonical()
					worktree.close()
					return fail(err)
				}
				if err := persistLifecycleBacklog(resolution.Write.Home, &backlogRecord, lifecycleStageComplete); err != nil {
					closeCanonical()
					worktree.close()
					return fail(err)
				}
				worktree.close()
				closeCanonical()
				outcome.Results[index].Applied = true
			}
			if err := archiveCleanupLifecycleArtifacts(task, artifactArchive, artifactArchivePath, artifactHandles, outcome.Artifacts); err != nil {
				return fail(err)
			}
			return outcome, nil
		}()
		if cleanupErr != nil {
			return cleanupOutcome, cleanupErr
		}
	}
	// Keep the now-empty task root while its descriptor lock is live. Removing
	// it after releasing that lock creates an ABA window where a concurrent
	// create can make a new, unreachable task directory at the same pathname.
	// Future creation reuses this harmless empty root under its normal lock.
	if normalized.ReportDir != "" {
		outcome.ReportPath, err = writeCleanupReport(normalized, now, "applied", outcome.Results, outcome.Diagnostics, outcome.Artifacts)
		if err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

type cleanupTaskSelection struct {
	WorktreesRoot string
	Task          string
}

func cleanupTaskSelections(outcome CleanupOutcome) []cleanupTaskSelection {
	byKey := make(map[string]cleanupTaskSelection)
	for _, result := range outcome.Results {
		if result.BacklogID != "" {
			continue
		}
		key := cleanupTaskKey(result.WorktreesRoot, result.Task)
		byKey[key] = cleanupTaskSelection{WorktreesRoot: result.WorktreesRoot, Task: result.Task}
	}
	for _, artifact := range outcome.Artifacts {
		key := cleanupTaskKey(artifact.WorktreesRoot, artifact.Task)
		byKey[key] = cleanupTaskSelection{WorktreesRoot: artifact.WorktreesRoot, Task: artifact.Task}
	}
	selections := make([]cleanupTaskSelection, 0, len(byKey))
	for _, selection := range byKey {
		selections = append(selections, selection)
	}
	sort.Slice(selections, func(i, j int) bool {
		return cleanupTaskKey(selections[i].WorktreesRoot, selections[i].Task) <
			cleanupTaskKey(selections[j].WorktreesRoot, selections[j].Task)
	})
	return selections
}

func cleanupTaskCanApply(outcome CleanupOutcome, taskKey string) bool {
	hasPending := false
	for _, result := range outcome.Results {
		if result.BacklogID != "" || cleanupTaskKey(result.WorktreesRoot, result.Task) != taskKey {
			continue
		}
		if !result.Eligible {
			return false
		}
		if !result.Applied {
			hasPending = true
		}
	}
	for _, artifact := range outcome.Artifacts {
		if cleanupTaskKey(artifact.WorktreesRoot, artifact.Task) != taskKey {
			continue
		}
		if !artifact.Eligible {
			return false
		}
		if !artifact.Applied {
			hasPending = true
		}
	}
	return hasPending
}

func normalizeListOptions(options ListOptions) (projectsRoot, task, base, filter string, err error) {
	projectsRoot, err = absoluteProjectsRoot(options.ProjectsRoot)
	if err != nil {
		return "", "", "", "", err
	}
	task = strings.TrimSpace(options.Task)
	if task != "" && !validSafeSegment(task) {
		return "", "", "", "", fmt.Errorf("task %q must be one safe path segment", task)
	}
	base = strings.TrimSpace(options.Base)
	if base == "" {
		base = "main"
	}
	if !validBranch(context.Background(), base) {
		return "", "", "", "", fmt.Errorf("invalid base branch %q", base)
	}
	filter = strings.TrimSpace(options.Filter)
	return projectsRoot, task, base, filter, nil
}

func normalizeCleanupOptions(options CleanupOptions) (CleanupOptions, error) {
	projectsRoot, task, base, filter, err := normalizeListOptions(ListOptions{
		ProjectsRoot: options.ProjectsRoot,
		Task:         options.Task,
		Base:         options.Base,
		Filter:       options.Filter,
	})
	if err != nil {
		return CleanupOptions{}, err
	}
	options.ProjectsRoot = projectsRoot
	options.Task = task
	options.Base = base
	options.Filter = filter
	if options.Task == "" && !options.AllMerged {
		return CleanupOptions{}, fmt.Errorf("supply one task or use --all-merged")
	}
	if options.Task != "" && options.AllMerged {
		return CleanupOptions{}, fmt.Errorf("task and --all-merged cannot be combined")
	}
	if options.OlderThan < 0 {
		return CleanupOptions{}, fmt.Errorf("--older-than cannot be negative")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ReportDir != "" {
		options.ReportDir, err = filepath.Abs(options.ReportDir)
		if err != nil {
			return CleanupOptions{}, fmt.Errorf("resolve cleanup report directory: %w", err)
		}
		options.ReportDir = filepath.Clean(options.ReportDir)
	}
	return options, nil
}

// DefaultCleanupReportDir returns the durable audit directory for one apply,
// below the already-resolved WB home directory (see wbhome.Root).
func DefaultCleanupReportDir(home string, now time.Time) string {
	return filepath.Join(
		home,
		"reports",
		"worktree-cleanup",
		now.UTC().Format("20060102T150405.000000000Z"),
	)
}

func validSafeSegment(value string) bool {
	return safeSegment.MatchString(value) && value != "." && value != ".."
}

func validRepositorySegment(value string) bool {
	return safeRepositorySegment.MatchString(value) && value != "." && value != ".."
}

func inspectLifecycleWorktree(
	ctx context.Context,
	projectsRoot string,
	layout wbhome.Layout,
	task, worktree, base string,
	withGitHub, locked bool,
) (ListResult, error) {
	root, err := git(ctx, worktree, "rev-parse", "--show-toplevel")
	if err != nil {
		return ListResult{}, fmt.Errorf("inspect %s: %w", worktree, err)
	}
	if filepath.Clean(root) != filepath.Clean(worktree) {
		return ListResult{}, fmt.Errorf("WB worktree %s has Git root %s", worktree, root)
	}
	location, err := locateManagedWorktree(ctx, projectsRoot, worktree, []wbhome.Layout{layout})
	if err != nil {
		return ListResult{}, err
	}
	if location.Task != task {
		return ListResult{}, fmt.Errorf("WB worktree %s belongs to task %q, not %q", worktree, location.Task, task)
	}
	slug := location.Owner + "/" + location.Repository
	canonical := filepath.Join(projectsRoot, location.Owner, location.Repository)
	_, commonDir, err := gitDirectories(ctx, worktree)
	if err != nil {
		return ListResult{}, err
	}
	expectedCommonDir := filepath.Join(canonical, ".git")
	if resolved, resolveErr := filepath.EvalSymlinks(expectedCommonDir); resolveErr == nil {
		expectedCommonDir = resolved
	}
	if filepath.Clean(commonDir) != filepath.Clean(expectedCommonDir) {
		return ListResult{}, fmt.Errorf("WB worktree %s belongs to %s, not %s", worktree, commonDir, canonical)
	}
	branch, err := git(ctx, worktree, "branch", "--show-current")
	if err != nil {
		return ListResult{}, err
	}
	if branch == "" || branch == base {
		return ListResult{}, fmt.Errorf("WB worktree %s is not on a feature branch", worktree)
	}
	head, err := git(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return ListResult{}, err
	}
	clean, err := cleanWorktree(ctx, worktree)
	if err != nil {
		return ListResult{}, err
	}
	locallyMerged, err := isAncestor(ctx, canonical, head, "origin/"+base)
	if err != nil {
		return ListResult{}, err
	}
	lastCommitValue, err := git(ctx, worktree, "show", "-s", "--format=%cI", "HEAD")
	if err != nil {
		return ListResult{}, err
	}
	lastCommit, err := time.Parse(time.RFC3339, lastCommitValue)
	if err != nil {
		return ListResult{}, fmt.Errorf("parse last commit time for %s: %w", slug, err)
	}
	result := ListResult{
		Task: task, Repository: slug, CanonicalDir: canonical, WorktreeDir: worktree,
		WorktreesRoot: layout.WorktreesRoot,
		Branch:        branch, Base: base, HeadSHA: head,
		Clean: clean, LocallyMerged: locallyMerged, Locked: locked, LastCommit: lastCommit,
	}
	if withGitHub {
		result.RemoteTargetSHA, err = fetchRemoteTargetHead(ctx, canonical, base)
		if err != nil {
			return ListResult{}, err
		}
		result.IntegratedAtOrigin, err = isAncestor(ctx, canonical, head, result.RemoteTargetSHA)
		if err != nil {
			return ListResult{}, err
		}
		// LocallyMerged historically described the remote-tracking ref. Once an
		// exact fetched target is available, report the stronger observation.
		result.LocallyMerged = result.IntegratedAtOrigin
		result.RemoteHeadSHA, err = remoteBranchHead(ctx, canonical, branch)
		if err != nil {
			return ListResult{}, err
		}
		pullRequests, err := githubPullRequests(ctx, worktree, slug, branch)
		if err != nil {
			return ListResult{}, err
		}
		result.OpenPullRequest, result.MergedPullRequest = matchingPullRequests(pullRequests, base, head)
	}
	return result, nil
}

func isAncestor(ctx context.Context, repository, ancestor, descendant string) (bool, error) {
	command := exec.CommandContext(ctx, "git", "-C", repository, "merge-base", "--is-ancestor", ancestor, descendant)
	command.Env = console.Env()
	err := command.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check whether %s is merged into %s: %w", ancestor, descendant, err)
}

func remoteBranchHead(ctx context.Context, repository, branch string) (string, error) {
	output, err := git(ctx, repository, "ls-remote", "--heads", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return "", nil
	}
	if len(fields) != 2 {
		return "", fmt.Errorf("unexpected remote branch response for %s: %q", branch, output)
	}
	return fields[0], nil
}

// fetchRemoteTargetHead obtains the exact origin target object used for the
// integration decision. FETCH_HEAD is deliberately used instead of trusting
// a possibly stale origin/<base> tracking ref. Cleanup repeats this immediately
// before deletion, so a force-pushed target cannot reuse old evidence.
func fetchRemoteTargetHead(ctx context.Context, repository, branch string) (string, error) {
	if _, err := git(ctx, repository, "fetch", "--no-tags", "origin", "refs/heads/"+branch); err != nil {
		return "", fmt.Errorf("fetch exact origin/%s target: %w", branch, err)
	}
	head, err := git(ctx, repository, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve fetched origin/%s target: %w", branch, err)
	}
	if !isGitObjectID(head) {
		return "", fmt.Errorf("origin/%s returned invalid target SHA %q", branch, head)
	}
	return head, nil
}

func githubPullRequests(ctx context.Context, worktree, repository, branch string) ([]githubPullRequest, error) {
	command := exec.CommandContext(
		ctx,
		"gh", "pr", "list",
		"--repo", repository,
		"--head", branch,
		"--state", "all",
		"--limit", "100",
		"--json", "number,url,state,mergedAt,headRefOid,baseRefName",
	)
	command.Dir = worktree
	command.Env = console.Env()
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("query pull requests for %s:%s: %w: %s", repository, branch, err, strings.TrimSpace(string(output)))
	}
	var pullRequests []githubPullRequest
	if err := json.Unmarshal(output, &pullRequests); err != nil {
		return nil, fmt.Errorf("decode pull requests for %s:%s: %w", repository, branch, err)
	}
	return pullRequests, nil
}

func matchingPullRequests(pullRequests []githubPullRequest, base, head string) (open, merged *PullRequest) {
	for _, candidate := range pullRequests {
		pullRequest := &PullRequest{
			Number: candidate.Number, URL: candidate.URL, State: candidate.State,
			Base: candidate.BaseRefName, HeadSHA: candidate.HeadRefOID, Merged: candidate.MergedAt,
		}
		if strings.EqualFold(candidate.State, "OPEN") {
			if open == nil || candidate.Number > open.Number {
				open = pullRequest
			}
			continue
		}
		if !strings.EqualFold(candidate.State, "MERGED") ||
			candidate.BaseRefName != base ||
			candidate.HeadRefOID != head ||
			candidate.MergedAt == nil {
			continue
		}
		if merged == nil || candidate.MergedAt.After(*merged.Merged) {
			merged = pullRequest
		}
	}
	return open, merged
}

func cleanupEligibility(entry ListResult, olderThan time.Duration, now time.Time) (bool, string) {
	switch {
	case entry.Locked:
		return false, "task is locked by an active or interrupted operation"
	case !entry.Clean:
		return false, "worktree has local changes"
	case entry.OpenPullRequest != nil:
		return false, "branch still has an open pull request: " + entry.OpenPullRequest.URL
	case entry.MergedPullRequest == nil && !entry.IntegratedAtOrigin:
		return false, "current branch head is not integrated into the exact origin target (awaiting push)"
	case entry.RemoteHeadSHA != "" && entry.RemoteHeadSHA != entry.HeadSHA:
		return false, "remote branch advanced after the merged pull request"
	case entry.MergedPullRequest != nil && olderThan > 0 && entry.MergedPullRequest.Merged.Add(olderThan).After(now):
		return false, "merged pull request is newer than the cleanup safety window"
	default:
		return true, ""
	}
}

// blockDiagnosedTasks blocks eligibility only for the coordinated task a
// malformed candidate belongs to — the same all-or-nothing unit
// blockUnsafeTasks already applies to an unclean, locked, or unmerged
// sibling. A malformed candidate itself never becomes a CleanupResult (it
// isn't a validated ListResult), so without this it would silently sit
// outside the coordination that is supposed to cover its whole task; every
// other task, and every other candidate within the current --filter
// selection, is unaffected.
func blockDiagnosedTasks(results []CleanupResult, diagnostics []ListDiagnostic) {
	if len(diagnostics) == 0 {
		return
	}
	reasonByTask := map[string]string{}
	for _, diagnostic := range diagnostics {
		key := cleanupTaskKey(diagnostic.WorktreesRoot, diagnostic.Task)
		if reasonByTask[key] == "" {
			reasonByTask[key] = diagnostic.Path + ": " + diagnostic.Message
		}
	}
	for index := range results {
		key := cleanupTaskKey(results[index].WorktreesRoot, results[index].Task)
		if reason, blocked := reasonByTask[key]; blocked && results[index].Eligible {
			results[index].Eligible = false
			results[index].Reason = "coordinated task blocked by malformed candidate " + reason
		}
	}
}

func blockArtifactTasks(results []CleanupResult, artifacts []LifecycleArtifact) {
	reasonByTask := make(map[string]string)
	for _, artifact := range artifacts {
		if artifact.Eligible {
			continue
		}
		key := cleanupTaskKey(artifact.WorktreesRoot, artifact.Task)
		if reasonByTask[key] == "" {
			reasonByTask[key] = artifact.Path + ": " + artifact.Reason
		}
	}
	for index := range results {
		key := cleanupTaskKey(results[index].WorktreesRoot, results[index].Task)
		if reason := reasonByTask[key]; reason != "" && results[index].Eligible {
			results[index].Eligible = false
			results[index].Reason = "coordinated task blocked by WB lifecycle artifact cleanup backlog " + reason
		}
	}
}

func blockUnsafeTasks(results []CleanupResult) {
	reasonByTask := map[string]string{}
	for _, result := range results {
		if !result.Eligible && reasonByTask[result.Task] == "" {
			reasonByTask[result.Task] = result.Repository + ": " + result.Reason
		}
	}
	for index := range results {
		if results[index].Eligible && reasonByTask[results[index].Task] != "" {
			results[index].Eligible = false
			results[index].Reason = "coordinated task blocked by " + reasonByTask[results[index].Task]
		}
	}
}

func cleanupTaskKey(worktreesRoot, task string) string {
	return filepath.Clean(worktreesRoot) + "\x00" + task
}

func preflightCleanupRepository(
	ctx context.Context,
	options CleanupOptions,
	now time.Time,
	task *cleanupTaskHandle,
	entry CleanupResult,
	home string,
) (ListResult, error) {
	worktree, err := openCleanupWorktree(task, entry)
	if err != nil {
		return ListResult{}, err
	}
	defer worktree.close()
	if err := worktree.validate(); err != nil {
		return ListResult{}, err
	}
	refreshed, err := inspectLifecycleWorktree(
		ctx,
		options.ProjectsRoot,
		wbhome.Layout{WorktreesRoot: entry.WorktreesRoot},
		entry.Task,
		entry.WorktreeDir,
		options.Base,
		true,
		false,
	)
	if err != nil {
		return ListResult{}, fmt.Errorf("preflight cleanup %s: %w", entry.Repository, err)
	}
	if err := worktree.validate(); err != nil {
		return ListResult{}, err
	}
	if eligible, reason := cleanupEligibility(refreshed, options.OlderThan, now); !eligible {
		return ListResult{}, fmt.Errorf("cleanup safety changed for %s: %s", refreshed.Repository, reason)
	}
	if refreshed.HeadSHA != entry.HeadSHA {
		return ListResult{}, fmt.Errorf("cleanup safety changed for %s: branch head moved", refreshed.Repository)
	}
	canonical, err := openCanonicalRepository(refreshed.CanonicalDir)
	if err != nil {
		return ListResult{}, fmt.Errorf("open cleanup canonical repository %s: %w", refreshed.CanonicalDir, err)
	}
	defer canonical.close()
	if err := canonical.validate(); err != nil {
		return ListResult{}, fmt.Errorf("cleanup canonical repository changed during preflight: %w", err)
	}
	if err := preflightWorkLogSeal(home, refreshed.WorktreeDir, refreshed.HeadSHA); err != nil {
		return ListResult{}, fmt.Errorf("preflight Work Log for %s: %w", refreshed.Repository, err)
	}
	return refreshed, nil
}

func acquireCleanupTaskAt(worktreesRoot, taskName string) (*cleanupTaskHandle, error) {
	worktrees, err := openAbsoluteDirectoryNoFollow(worktreesRoot, false)
	if err != nil {
		return nil, fmt.Errorf("open cleanup worktrees root %s: %w", worktreesRoot, err)
	}
	taskFD, err := unix.Openat(int(worktrees.Fd()), taskName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = worktrees.Close()
		return nil, fmt.Errorf("open cleanup task %s without following links: %w", taskName, err)
	}
	task := os.NewFile(uintptr(taskFD), "wb-cleanup-task")
	if task == nil {
		_ = unix.Close(taskFD)
		_ = worktrees.Close()
		return nil, fmt.Errorf("wrap cleanup task %s", taskName)
	}
	handle := &cleanupTaskHandle{
		worktreesPath: worktreesRoot,
		taskPath:      filepath.Join(worktreesRoot, taskName),
		worktrees:     worktrees,
		task:          task,
	}
	if err := handle.validate(); err != nil {
		handle.close()
		return nil, err
	}
	lock, err := acquireLockAt(task)
	if err != nil {
		handle.close()
		return nil, fmt.Errorf("lock cleanup task %s: %w", taskName, err)
	}
	handle.lock = lock
	return handle, nil
}

// SecureCleanupGitHelperArgument selects the private WB child process that
// runs cleanup Git commands from retained canonical and worktree descriptors.
const SecureCleanupGitHelperArgument = "--wb-internal-cleanup-git"

func runSecureCleanupGitHelper(ctx context.Context, canonical *canonicalRepository, worktreeParent, worktreeDirectory *os.File, worktreeParentPath, worktreePath string, gitArgs ...string) error {
	if canonical == nil || canonical.root == nil || canonical.common == nil {
		return fmt.Errorf("cleanup canonical repository descriptor is unavailable")
	}
	if err := canonical.authorizeForGit(); err != nil {
		return fmt.Errorf("canonical repository path changed before Git operation: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate WB cleanup Git helper: %w", err)
	}
	gitExecutable, err := trustedGitExecutable()
	if err != nil {
		return err
	}
	extraFiles := []*os.File{canonical.root, canonical.common}
	remoteDirectory, remotePath, err := localOriginDirectoryForSecurePush(ctx, canonical, gitArgs)
	if err != nil {
		return err
	}
	remoteFD := -1
	if remoteDirectory != nil {
		defer func() { _ = remoteDirectory.Close() }()
		remoteFD = 3 + len(extraFiles)
		extraFiles = append(extraFiles, remoteDirectory)
	}
	arguments := append([]string{
		SecureCleanupGitHelperArgument, canonical.path, worktreePath, worktreeParentPath,
		gitExecutable, remotePath, strconv.Itoa(remoteFD),
	}, gitArgs...)
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Env = console.Env()
	if worktreeDirectory != nil {
		if worktreeParent == nil || worktreeParentPath == "" {
			return fmt.Errorf("cleanup worktree parent descriptor is unavailable")
		}
		// Worktree descriptors must precede the optional local remote so their
		// child FD numbers remain 5 and 6. Rebuild the ordered list and adjust
		// the advertised remote FD when both are present.
		extraFiles = []*os.File{canonical.root, canonical.common, worktreeParent, worktreeDirectory}
		if remoteDirectory != nil {
			remoteFD = 3 + len(extraFiles)
			extraFiles = append(extraFiles, remoteDirectory)
			arguments[6] = strconv.Itoa(remoteFD)
			command.Args[7] = strconv.Itoa(remoteFD)
		}
	}
	command.ExtraFiles = extraFiles
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run descriptor-anchored cleanup Git: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// localOriginDirectoryForSecurePush authorizes an exact local file remote as
// an additional descriptor-bound write root. Network/SSH remotes need no
// local filesystem root. This keeps integration tests and legitimate local
// bare-repository workflows inside the same sandbox boundary instead of
// weakening remote deletion to an unanchored Git process.
func localOriginDirectoryForSecurePush(ctx context.Context, canonical *canonicalRepository, gitArgs []string) (*os.File, string, error) {
	if len(gitArgs) == 0 || gitArgs[0] != "push" {
		return nil, "", nil
	}
	remoteURL, err := gitCanonical(ctx, canonical, "remote", "get-url", "--push", "origin")
	if err != nil {
		return nil, "", fmt.Errorf("resolve origin push URL: %w", err)
	}
	remoteURL = strings.TrimSpace(remoteURL)
	localPath := remoteURL
	if strings.HasPrefix(localPath, "file://") {
		localPath = strings.TrimPrefix(localPath, "file://")
		localPath = strings.TrimPrefix(localPath, "localhost")
	} else if strings.Contains(localPath, "://") || (!filepath.IsAbs(localPath) && strings.Contains(localPath, ":")) {
		return nil, "", nil
	}
	if !filepath.IsAbs(localPath) {
		localPath = filepath.Join(canonical.path, localPath)
	}
	localPath, err = filepath.EvalSymlinks(filepath.Clean(localPath))
	if err != nil {
		return nil, "", fmt.Errorf("resolve local origin directory %s: %w", remoteURL, err)
	}
	directory, err := openAbsoluteDirectoryNoFollow(localPath, false)
	if err != nil {
		return nil, "", fmt.Errorf("open local origin directory %s: %w", localPath, err)
	}
	return directory, localPath, nil
}

// RunSecureCleanupGitHelper is the child half of descriptor-anchored cleanup
// Git operations. FD 3 is the canonical repository, FD 4 is its held `.git`
// directory, FD 5 is the held worktree parent, and FD 6 is the target
// worktree. Both canonical descriptors and the optional parent/worktree pair
// are reauthorized immediately before Git executes.
func RunSecureCleanupGitHelper(args []string) int {
	if len(args) < 7 {
		_, _ = fmt.Fprintln(os.Stderr, "wb secure cleanup helper: missing worktree path or Git command")
		return 1
	}
	canonical := os.NewFile(uintptr(3), "wb-cleanup-canonical")
	common := os.NewFile(uintptr(4), "wb-cleanup-canonical-git")
	if canonical == nil || common == nil {
		if canonical != nil {
			_ = canonical.Close()
		}
		if common != nil {
			_ = common.Close()
		}
		_, _ = fmt.Fprintln(os.Stderr, "wb secure cleanup helper: inherited canonical repository is unavailable")
		return 1
	}
	defer func() { _ = canonical.Close() }()
	defer func() { _ = common.Close() }()
	if err := unix.Fchdir(int(canonical.Fd())); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure cleanup helper: enter inherited canonical repository: %v\n", err)
		return 1
	}
	if !directoryStillMatches(args[0], canonical) || !directoryEntryStillMatches(canonical, ".git", common) {
		_, _ = fmt.Fprintln(os.Stderr, "wb secure cleanup helper: canonical repository path changed before Git operation")
		return 1
	}
	if err := unix.Fchdir(int(common.Fd())); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure cleanup helper: enter inherited canonical Git directory: %v\n", err)
		return 1
	}
	var parent *os.File
	if args[1] != "" {
		parent = os.NewFile(uintptr(5), "wb-cleanup-worktree-parent")
		worktree := os.NewFile(uintptr(6), "wb-cleanup-worktree")
		if parent == nil || worktree == nil {
			if parent != nil {
				_ = parent.Close()
			}
			if worktree != nil {
				_ = worktree.Close()
			}
			_, _ = fmt.Fprintln(os.Stderr, "wb secure cleanup helper: inherited worktree is unavailable")
			return 1
		}
		defer func() { _ = parent.Close() }()
		defer func() { _ = worktree.Close() }()
		if !directoryStillMatches(args[2], parent) || !directoryStillMatches(args[1], worktree) {
			_, _ = fmt.Fprintln(os.Stderr, "wb secure cleanup helper: worktree path changed before Git operation")
			return 1
		}
	}
	writeRoots := []gitFilesystemCapabilityRoot{{path: args[0], directory: canonical}}
	if args[1] != "" {
		writeRoots = append(writeRoots, gitFilesystemCapabilityRoot{path: args[2], directory: parent})
	}
	if args[4] != "" {
		remoteFD, parseErr := strconv.Atoi(args[5])
		if parseErr != nil || remoteFD < 5 {
			_, _ = fmt.Fprintln(os.Stderr, "wb secure cleanup helper: invalid local remote descriptor")
			return 1
		}
		remote := os.NewFile(uintptr(remoteFD), "wb-cleanup-local-remote")
		if remote == nil {
			_, _ = fmt.Fprintln(os.Stderr, "wb secure cleanup helper: inherited local remote is unavailable")
			return 1
		}
		defer func() { _ = remote.Close() }()
		if !directoryStillMatches(args[4], remote) {
			_, _ = fmt.Fprintln(os.Stderr, "wb secure cleanup helper: local remote path changed before Git operation")
			return 1
		}
		writeRoots = append(writeRoots, gitFilesystemCapabilityRoot{path: args[4], directory: remote})
	}
	capability, err := newGitFilesystemCapability(writeRoots...)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure cleanup helper: %v\n", err)
		return 1
	}
	return runGitWithFilesystemCapability(capability, args[3], args[6:], gitEnvironmentWithHeldGitDirAndWorkTree(filepath.Join(args[0], ".git"), args[0]))
}

func openCleanupWorktree(task *cleanupTaskHandle, result CleanupResult) (*cleanupWorktreeHandle, error) {
	if err := task.validate(); err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(task.taskPath, result.WorktreeDir)
	if err != nil {
		return nil, fmt.Errorf("resolve cleanup worktree relative path: %w", err)
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) == 0 || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return nil, fmt.Errorf("cleanup worktree %s is outside held task %s", result.WorktreeDir, task.taskPath)
	}
	handle := &cleanupWorktreeHandle{task: task, worktreePath: result.WorktreeDir}
	var repository string
	switch len(parts) {
	case 1:
		if !validRepositorySegment(parts[0]) {
			return nil, fmt.Errorf("invalid cleanup repository directory %q", parts[0])
		}
		repository = parts[0]
		handle.parent = task.task
		handle.parentPath = task.taskPath
	case 2:
		if !validSafeSegment(parts[0]) || !validRepositorySegment(parts[1]) {
			return nil, fmt.Errorf("invalid cleanup worktree hierarchy %s", relative)
		}
		parentFD, err := unix.Openat(int(task.task.Fd()), parts[0], unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if err != nil {
			return nil, fmt.Errorf("open cleanup worktree parent %s without following links: %w", parts[0], err)
		}
		parent := os.NewFile(uintptr(parentFD), "wb-cleanup-worktree-parent")
		if parent == nil {
			_ = unix.Close(parentFD)
			return nil, fmt.Errorf("wrap cleanup worktree parent %s", parts[0])
		}
		handle.parent = parent
		handle.parentPath = filepath.Join(task.taskPath, parts[0])
		handle.parentName = parts[0]
		handle.closeParent = true
		repository = parts[1]
	default:
		return nil, fmt.Errorf("cleanup worktree %s has unsupported hierarchy", result.WorktreeDir)
	}
	worktreeFD, err := unix.Openat(int(handle.parent.Fd()), repository, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		handle.close()
		return nil, fmt.Errorf("open cleanup worktree %s without following links: %w", result.WorktreeDir, err)
	}
	handle.worktree = os.NewFile(uintptr(worktreeFD), "wb-cleanup-worktree")
	if handle.worktree == nil {
		_ = unix.Close(worktreeFD)
		handle.close()
		return nil, fmt.Errorf("wrap cleanup worktree %s", result.WorktreeDir)
	}
	if err := handle.validate(); err != nil {
		handle.close()
		return nil, err
	}
	return handle, nil
}

func writeCleanupReport(
	options CleanupOptions,
	generatedAt time.Time,
	phase string,
	results []CleanupResult,
	diagnostics []ListDiagnostic,
	artifacts []LifecycleArtifact,
) (string, error) {
	if err := os.MkdirAll(options.ReportDir, 0o755); err != nil {
		return "", fmt.Errorf("create cleanup report directory: %w", err)
	}
	report := cleanupReport{
		GeneratedAt:  generatedAt,
		Phase:        phase,
		Task:         options.Task,
		Filter:       options.Filter,
		AllMerged:    options.AllMerged,
		Apply:        options.Apply,
		DeleteRemote: options.DeleteRemote,
		OlderThan:    options.OlderThan.String(),
		Results:      results,
		Diagnostics:  diagnostics,
		Artifacts:    artifacts,
	}
	content, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode cleanup report: %w", err)
	}
	content = append(content, '\n')
	path := filepath.Join(options.ReportDir, "cleanup.json")
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, content, 0o644); err != nil {
		return "", fmt.Errorf("write cleanup report: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return "", fmt.Errorf("activate cleanup report: %w", err)
	}
	return path, nil
}
