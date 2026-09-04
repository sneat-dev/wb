package worktrees

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/wbhome"
	"golang.org/x/sys/unix"
)

// ListOptions selects WB-managed task worktrees and optional GitHub PR state.
type ListOptions struct {
	ProjectsRoot string
	Task         string
	// Tasks is an exact set of task names. Task remains for compatibility with
	// callers that select one task; callers must not set both.
	Tasks []string
	// Base is the fallback target branch for candidates without an immutable
	// recorded target. A candidate's manifest/Work Log claim wins over this
	// fallback, so one task may safely contain worktrees stacked on different
	// targets. An omitted Base resolves to main.
	Base string
	// Filter narrows the inventory to candidates whose owner/repository slug
	// (or, for a candidate that cannot be identified that cleanly, whatever
	// raw path-derived identity is available) contains this substring — the
	// same "only repos whose org/name contains this substring" semantics as
	// the root --filter flag elsewhere in WB. An empty Filter matches
	// everything, exactly like today. Filtering happens before a candidate's
	// diagnostic or result is retained, so a candidate outside the selection
	// can neither appear in the report nor influence it.
	Filter string
	// OwnerState limits results by current owner PID liveness: active or orphaned.
	OwnerState string
	GitHub     bool
	// AbsorbedBy points at the merged pull request or exact landing commit
	// that carried a candidate's work into the target inside a differently
	// named integration branch. It selects which receipt to verify and never
	// substitutes for one: every containment proof still runs, so a wrong or
	// dishonest pointer can only fail closed. See absorbedLandingReceipt.
	AbsorbedBy string
	// Progress, when set, is called as the walk reaches and finishes each
	// candidate. The inventory is one long blocking call — with GitHub set it
	// fetches from the network once per candidate, serially — so without this
	// hook a fleet-wide run is indistinguishable from a hang for as long as it
	// takes. It is observation only: nothing about the walk depends on it, and
	// a nil Progress costs nothing.
	Progress func(ListProgress)
	// Workers caps concurrent candidate inspections, mirroring wb sync's
	// --workers. Zero means the default; one makes the walk serial again,
	// which is the behaviour to fall back to if a repository ever proves
	// unsafe to inspect alongside its siblings.
	Workers int
	// IncludeDetached keeps a checkout whose HEAD is detached in the inventory
	// instead of reporting it as a malformed candidate. A pull-request review
	// checkout is detached by construction, and dropping it from the inventory
	// is why nothing in WB could ever retire one: the measured sweep showed 50
	// inventory rows for 60 checkouts. Callers that mutate a branch — cleanup,
	// merge — leave this false, so a detached checkout stays outside their
	// reach exactly as before; `wb worktree list` and `wb worktree gc` set it.
	IncludeDetached bool
	// TTL, when positive, marks a checkout older than this as expired. It is
	// pure reporting: nothing acts on it, and its purpose is that "this task
	// has been finished for six days" is visible before the disk fills.
	TTL time.Duration
	// Activity asks the inventory to record LastActivityAt. Only a verb that
	// decides whether a checkout may be removed needs it.
	Activity bool
	// ResidueEvidence asks the inventory to look for a landed ancestor when the
	// head itself is not integrated. It costs up to ResidueDepth extra reads of
	// GitHub's commit index per candidate, so it is opt-in: a fleet-wide sweep
	// over dozens of unlanded worktrees must not pay for evidence nobody asked
	// for. Verbs that classify — worktree gc — set it; verbs that merely list
	// do not.
	ResidueEvidence bool
	// ResidueDepth bounds how far back from HEAD that walk goes. Zero uses
	// DefaultResidueDepth.
	ResidueDepth int
	// Now is the clock used for age and TTL. Tests inject it; production
	// leaves it nil for time.Now.
	Now func() time.Time
}

// DefaultInspectWorkers is the default cap on concurrent candidate
// inspections, matching wb sync's default worker count.
const DefaultInspectWorkers = 8

// ListProgress is one step of the inventory walk, reported to
// ListOptions.Progress.
type ListProgress struct {
	// Index counts candidates inspected in this run, 1-based, across every
	// layout. The total is not known in advance: it takes reading each task
	// directory to learn how many repositories are under it, which is most of
	// the walk itself.
	Index      int
	Task       string
	Repository string // empty until inspection identifies it
	Path       string
	// Done distinguishes reaching a candidate from finishing it. Both are
	// reported because the gap between them is the part that takes the time,
	// and a candidate that never reports Done is the one that is stuck.
	Done    bool
	Elapsed time.Duration
	// Network is true when this candidate's inspection contacts origin, which
	// is what makes the walk slow.
	Network bool
}

// pendingInspect is one candidate the filesystem walk found, queued for the
// inspection phase. The walk itself is local and fast; inspection is what
// contacts origin, so only inspection is worth parallelising.
type pendingInspect struct {
	task      string
	path      string
	ownerName string // current-layout owner segment, empty for legacy
	slug      string
	locked    bool
	commonDir string // canonical .git this worktree belongs to
	// external is true when path was resolved from a `wb worktree adopt`
	// registration entry rather than being itself nested under the WB
	// worktrees root. See listLayout's adoption-pointer branch.
	external bool
}

// cloneLocks serialises git work per canonical clone.
//
// This is the one way the worktree inventory differs from wb sync's worker
// pool: sync's unit of work is a repository, so no two workers ever touch the
// same clone. Here many worktrees share one — twenty sneat-go tasks all fetch
// ~/projects/sneat-co/sneat-go — and concurrent fetches into a single clone
// contend on git's ref and index locks. Parallel across clones, serial within
// one, keyed on the resolved common .git directory so a renamed or oddly
// nested checkout still lands in the right lane.
type cloneLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newCloneLocks() *cloneLocks { return &cloneLocks{locks: map[string]*sync.Mutex{}} }

func (c *cloneLocks) get(key string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	lock, ok := c.locks[key]
	if !ok {
		lock = &sync.Mutex{}
		c.locks[key] = lock
	}
	return lock
}

// gitCommonDir resolves the canonical .git directory a worktree belongs to.
// It is a local call with no network, so the walk can afford it per candidate
// in order to shard the inspection phase safely. An unreadable answer yields
// the path itself, which is conservative: that candidate simply gets its own
// lane rather than sharing one it might have needed.
func gitCommonDir(ctx context.Context, worktreePath string) string {
	out, err := git(ctx, worktreePath, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return worktreePath
	}
	if resolved := strings.TrimSpace(out); resolved != "" {
		return resolved
	}
	return worktreePath
}

// listProgressReporter carries the shared candidate counter across layouts so
// the index a caller sees is continuous over the whole run, not per layout.
//
// Every worker reports through one reporter, so the counter and the callback
// are both guarded. Serialising the callback rather than only the counter is
// deliberate: it means a caller's Progress function is never entered twice at
// once and can be written as ordinary single-threaded rendering code. The lock
// is held only for the callback, never across an inspection.
type listProgressReporter struct {
	mu     sync.Mutex
	report func(ListProgress)
	index  int
}

// progressToken ties a finish back to the start that issued it. The index
// cannot be re-read at finish time — by then other workers have moved the
// counter on, and the candidate would be reported under a stranger's number.
type progressToken struct {
	index   int
	started time.Time
}

func (r *listProgressReporter) start(task, path string, network bool) progressToken {
	if r == nil || r.report == nil {
		return progressToken{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.index++
	r.report(ListProgress{Index: r.index, Task: task, Path: path, Network: network})
	return progressToken{index: r.index, started: time.Now()}
}

func (r *listProgressReporter) finish(token progressToken, task, repository, path string, network bool) {
	if r == nil || r.report == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.report(ListProgress{
		Index: token.index, Task: task, Repository: repository, Path: path,
		Done: true, Elapsed: time.Since(token.started), Network: network,
	})
}

// PullRequest is the GitHub evidence used to decide whether a branch is safe
// to clean up. HeadSHA must match the current branch tip.
type PullRequest struct {
	Number     int        `json:"number"`
	URL        string     `json:"url"`
	Repository string     `json:"repository,omitempty"`
	State      string     `json:"state"`
	Base       string     `json:"base"`
	HeadSHA    string     `json:"head_sha"`
	MergeSHA   string     `json:"merge_sha,omitempty"`
	Merged     *time.Time `json:"merged_at,omitempty"`
}

// ListResult describes one linked checkout below the WB task hierarchy.
type ListResult struct {
	Task                 string `json:"task"`
	Repository           string `json:"repository"`
	CanonicalDir         string `json:"canonical_dir"`
	WorktreeDir          string `json:"worktree_dir"`
	WorktreesRoot        string `json:"worktrees_root"`
	Branch               string `json:"branch"`
	Base                 string `json:"base"`
	HeadSHA              string `json:"head_sha"`
	RemoteHeadSHA        string `json:"remote_head_sha,omitempty"`
	RemoteTargetSHA      string `json:"remote_target_sha,omitempty"`
	IntegratedAtOrigin   bool   `json:"integrated_at_origin"`
	RebaseMergedAtOrigin bool   `json:"rebase_merged_at_origin,omitempty"`
	AbsorbedAtOrigin     bool   `json:"absorbed_at_origin,omitempty"`
	AbsorbedBySHA        string `json:"absorbed_by_sha,omitempty"`
	// SupersededAtOrigin records an explicitly reviewed split-branch
	// terminalization. It deliberately does not set IntegratedAtOrigin: the
	// original head did not land as a whole.
	SupersededAtOrigin    bool   `json:"superseded_at_origin,omitempty"`
	SupersessionReceipt   string `json:"supersession_receipt,omitempty"`
	SupersessionReviewer  string `json:"supersession_reviewer,omitempty"`
	SupersessionReceiptID string `json:"supersession_receipt_id,omitempty"`
	SupersessionRejection string `json:"supersession_rejection,omitempty"`
	// AbsorbedByRejection explains why an explicitly supplied --absorbed-by
	// receipt did not hold. An operator pointer that fails verification is a
	// precise, reportable refusal of that candidate, never a malformed
	// worktree and never a reason to abort a fleet-wide sweep.
	AbsorbedByRejection string `json:"absorbed_by_rejection,omitempty"`
	Clean               bool   `json:"clean"`
	LocallyMerged       bool   `json:"locally_merged"`
	Locked              bool   `json:"locked"`
	// LockOwner and LockOwnerPID describe who holds Locked, so a refusal
	// can distinguish a peer operation still running from a recoverable
	// remnant of one that was interrupted. See diagnoseTaskLock.
	LockOwner    LockOwnerState `json:"lock_owner,omitempty"`
	LockOwnerPID int            `json:"lock_owner_pid,omitempty"`
	LastCommit   time.Time      `json:"last_commit"`
	Owners       []OwnerView    `json:"owners,omitempty"`
	// WorkLogSessionID is the immutable session link from the active private
	// claim. It lets session park recover a claim even when the owner event was
	// not projected, while remaining absent for legacy claims.
	WorkLogSessionID  string       `json:"work_log_session_id,omitempty"`
	OwnerState        string       `json:"owner_state"`
	OpenPullRequest   *PullRequest `json:"open_pull_request,omitempty"`
	MergedPullRequest *PullRequest `json:"merged_pull_request,omitempty"`
	// External marks a worktree adopted by `wb worktree adopt` from outside
	// every WB worktrees root. Its WorktreeDir is the real, never-relocated
	// checkout path; only a small registration entry — never the checkout
	// itself — lives under the WB task directory. See openAdoptedCleanupWorktree
	// and locateAdoptedWorktree.
	External bool `json:"external,omitempty"`
	// Local marks WB's default <canonical>/.worktrees/<task> placement.
	// It is managed by WB (unlike External) but uses WB_HOME for the task lock.
	Local bool `json:"local,omitempty"`
	// Detached marks a checkout with no current branch. Branch is empty for
	// one, so every branch-shaped operation must skip it rather than act on an
	// empty ref. It is populated only when ListOptions.IncludeDetached is set.
	Detached bool `json:"detached,omitempty"`
	// Owner is the agent identity that last took custody, or "orphaned" when
	// none is live. It is the human-readable half of OwnerState, carried here
	// so an inventory row can name who to ask before removing anything.
	Owner string `json:"owner,omitempty"`
	// CreatedAt is the immutable manifest's creation time, falling back to the
	// worktree directory's own modification time for a checkout WB did not
	// create. AgeSeconds and Expired are derived from it against ListOptions.TTL.
	CreatedAt  time.Time `json:"created_at,omitempty"`
	AgeSeconds int64     `json:"age_seconds,omitempty"`
	TTLSeconds int64     `json:"ttl_seconds,omitempty"`
	Expired    bool      `json:"expired,omitempty"`
	// HeadUnknownToRemote records that GitHub's commit index has never seen
	// this head: the checkout holds work that was never pushed anywhere.
	HeadUnknownToRemote bool `json:"head_unknown_to_remote,omitempty"`
	// LastActivityAt is the newest sign that anyone is using this checkout:
	// a heartbeat, an edited file, a Work Log event, or a commit. It is what
	// "in use" is decided from, because a live process id is evidence about a
	// process and the question is about a worktree.
	LastActivityAt time.Time `json:"last_activity_at,omitempty"`
	// Landing is the commit-identity landing evidence for a head that is not
	// itself contained in the target: the merged pull request of an ancestor,
	// plus the local commits stacked on top of it. A squash merge produces
	// exactly this shape, and reporting it as a bare "awaiting push" was 7 of
	// 11 refusals in the measured sweep. See landingEvidence.
	Landing *LandingEvidence `json:"landing,omitempty"`
	// supersessionReceipt is retained only for the current process so the
	// terminal Work Log can embed the exact reviewed evidence. The public JSON
	// carries the receipt path and reviewer identity; the private archive gets
	// the complete receipt before any Git deletion.
	supersessionReceipt *SupersessionReceipt
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
	// NonBlocking identifies visible foreign filesystem debris that has no
	// corresponding canonical repository. It is never a validated WB asset
	// and must not prevent a real sibling from reaching its own safe terminal
	// transition. Any WB-shaped path remains blocking.
	NonBlocking bool `json:"non_blocking,omitempty"`
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
	Repository    string `json:"repository,omitempty"`
	NonBlocking   bool   `json:"non_blocking,omitempty"`
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
	// Purged records the terminal artefacts this read path swept. It is
	// evidence for a receipt, never a per-invocation log line: see
	// purgeTerminalArtefacts.
	Purged []PurgedArtefact `json:"purged,omitempty"`
}

// CleanupOptions controls planning and removal of merged WB tasks.
type CleanupOptions struct {
	ProjectsRoot string
	Task         string
	// Tasks is an exact set of task names. Task remains for compatibility with
	// callers that select one task; callers must not set both.
	Tasks []string
	// Base is only the fallback for legacy candidates without a recorded
	// manifest/Work Log target. Recorded targets are resolved per candidate;
	// cleanup never applies one global base to a heterogeneous task.
	Base string
	// ExactRepository limits a named-task cleanup transaction to one exact
	// owner/repository slug. It is intended for repository-scoped orchestrators
	// such as worktree merge, where another repository may share the same task.
	// Filter remains the user-facing substring selector; this additional gate is
	// applied to the already inspected plan before any mutation.
	ExactRepository string
	// Filter narrows both which candidates are validated and which are acted
	// on to those whose owner/repository slug contains this substring — see
	// ListOptions.Filter. An empty Filter matches everything, preserving
	// today's behavior exactly.
	Filter string
	// AbsorbedBy is the optional landing receipt pointer described on
	// ListOptions.AbsorbedBy. It is verified, never trusted.
	AbsorbedBy string
	// SupersededBy is a path to an explicit trusted-reviewer supersession
	// receipt. It is only valid for a named cleanup task and is never inferred
	// from CI, PR state, or content similarity.
	SupersededBy string
	// MergeReceiptProofs are exact, orchestrator-produced cleanup proofs for
	// sources whose content was landed by an integration candidate and then
	// represented by a distinct commit with the same tree (for example, a
	// squash merge). They are deliberately unavailable to the user-facing
	// cleanup command: every proof is still rechecked against the source,
	// candidate, landing, and freshly fetched target before it can affect one
	// matching source worktree.
	MergeReceiptProofs []MergeReceiptCleanupProof
	// Progress is passed straight through to the inventory walk; see
	// ListOptions. A fleet-wide run is unobservable without it.
	Progress func(ListProgress)
	// Workers is the single concurrency ceiling for both phases: how many
	// candidates the inventory walk inspects at once (see ListOptions) and how
	// many tasks --all-merged applies at once. Apply concurrency is bounded by
	// the canonical repository rather than by this number — Git allows one
	// writer per clone — so raising it past the largest per-repository group
	// buys nothing. One makes both phases serial again.
	Workers   int
	AllMerged bool
	Apply     bool
	// ResumeInterrupted authorizes recovery of exactly the named task's
	// descriptor-validated interrupted lock before normal terminal cleanup.
	// It is deliberately unavailable to fleet cleanup.
	ResumeInterrupted bool
	DeleteRemote      bool
	// RequireRemoteRetirement asserts that this transaction must not finish a
	// named task while its source branch is still present on origin, because
	// the branch would survive as invisible backlog. It is an *evidence*
	// gate, not a flag-shape one: a candidate whose origin branch is already
	// gone has nothing left to retire and is cleaned without --remote. Only
	// the user-facing cleanup command sets it; orchestrators such as worktree
	// merge own their own remote-retirement sequencing.
	RequireRemoteRetirement bool
	OlderThan               time.Duration
	ReportDir               string
	Now                     func() time.Time
	// AllowResidue widens eligibility past exactly one refusal: a branch whose
	// work is provably landed by commit identity but which holds local commits
	// past the landed head. It is not a force flag — no proof is skipped, the
	// landing receipt must still hold — and the residual commits are printed
	// before they are discarded, because deleting the branch discards them.
	AllowResidue bool
	// IncludeDetached lets a detached checkout — the shape every pull-request
	// review creates — reach cleanup at all. It is only ever safe alongside
	// evidence that the checkout's head is a merged pull request's head, which
	// is what worktree gc classifies before it delegates here.
	IncludeDetached bool
	// TTL is reporting only, threaded to the inventory so a cleanup receipt can
	// state a candidate's age against the fleet's expiry window.
	TTL time.Duration
	// ResidueDepth bounds the commit-to-pull-request walk. See ListOptions.
	ResidueDepth int
	// Activity threads ListOptions.Activity, so a re-verification under the
	// task lock asks the same question the plan asked.
	Activity bool
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
	// beforeCleanupResidueRemoval simulates a crash/failure after Git
	// unregistered a worktree it could not finish deleting and before WB
	// removes what was left. It produces the one state no other seam can: an
	// unregistered checkout still on disk, with a backlog record stranded at
	// removing_worktree.
	beforeCleanupResidueRemoval func(worktree string) error
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
	// afterResumeInterruptedLock models a successor or early failure after
	// recovery acquired the exact stale descriptor. Cleanup must preserve the
	// lock until an eligible cleanup transaction owns it.
	afterResumeInterruptedLock func(lockPath string) error
	// beforeRecoveredLockQuarantine is a test-only seam immediately before a
	// recovered lock's descriptor-anchored no-replace retirement. It proves a
	// failed retirement never claims terminal recovery in JSON or reports.
	beforeRecoveredLockQuarantine func(lockPath string)
}

// MergeReceiptCleanupProof binds one source worktree to the exact candidate
// and landing identities recorded by worktree merge. It is an internal
// orchestration receipt, not a general replacement for --absorbed-by.
type MergeReceiptCleanupProof struct {
	Repository     string
	Target         string
	SourceTask     string
	SourceWorktree string
	SourceBranch   string
	SourceSHA      string
	CandidateSHA   string
	LandingSHA     string
}

// CleanupResult records one repository's cleanup decision and outcome.
type CleanupResult struct {
	ListResult
	Eligible      bool `json:"eligible"`
	Applied       bool `json:"applied"`
	RemoteDeleted bool `json:"remote_deleted"`
	WorktreeGone  bool `json:"worktree_gone"`
	// WorktreeResidueRemoved records that Git unregistered the worktree, failed
	// to finish deleting it, and WB removed what was left. It is audit
	// evidence, not a warning: the task completes either way, and the operator
	// deserves to know which of the two paths deleted the checkout.
	WorktreeResidueRemoved bool   `json:"worktree_residue_removed,omitempty"`
	BranchDeleted          bool   `json:"branch_deleted"`
	BacklogID              string `json:"backlog_id,omitempty"`
	Reason                 string `json:"reason,omitempty"`
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
	// Purged records the terminal artefacts the inventory walk swept on its way
	// here. They are never part of the plan an operator approves: an empty
	// retired stage and an inert retired lock are WB's own debris, and their
	// removal is maintenance rather than a cleanup decision.
	Purged []PurgedArtefact `json:"purged,omitempty"`
	// Quarantined names the durable cleanup records this run declined to act
	// on. They are reported rather than swallowed, and they never abort the
	// run: the backlog directory is shared by every task on the machine, and
	// one record WB cannot validate must not refuse everybody else's cleanup.
	Quarantined []LifecycleBacklogQuarantine `json:"quarantined,omitempty"`
	Recovery    *InterruptedLockRecovery     `json:"recovery,omitempty"`
	// ResolvedTasks are the physical task namespaces Cleanup actually
	// inspected after expanding any logical effort aliases (session-resume-*
	// member directories). A named `wb worktree cleanup <effort>` invocation
	// must judge apply success against these identities, not the pre-resolution
	// selector that produced them.
	ResolvedTasks []string `json:"resolved_tasks,omitempty"`
}

// InterruptedLockRecovery is durable operator-visible evidence for the one
// explicitly named interrupted task lock a cleanup command inspected.
type InterruptedLockRecovery struct {
	Task          string `json:"task"`
	WorktreesRoot string `json:"worktrees_root"`
	Path          string `json:"path"`
	PID           int    `json:"pid"`
	Disposition   string `json:"disposition"`
	Applied       bool   `json:"applied"`
	Reason        string `json:"reason,omitempty"`
}

type cleanupReport struct {
	GeneratedAt  time.Time                `json:"generated_at"`
	Phase        string                   `json:"phase"`
	Task         string                   `json:"task,omitempty"`
	Tasks        []string                 `json:"tasks,omitempty"`
	Filter       string                   `json:"filter,omitempty"`
	AllMerged    bool                     `json:"all_merged"`
	Apply        bool                     `json:"apply"`
	DeleteRemote bool                     `json:"delete_remote"`
	OlderThan    string                   `json:"older_than"`
	Results      []CleanupResult          `json:"results"`
	Diagnostics  []ListDiagnostic         `json:"diagnostics,omitempty"`
	Artifacts    []LifecycleArtifact      `json:"artifacts,omitempty"`
	Recovery     *InterruptedLockRecovery `json:"recovery,omitempty"`
}

// ValidateTerminalCleanupReports proves that the receipt's referenced cleanup
// reports are structurally valid and that every expected task has one durable
// successful terminal cleanup. Historical failed attempts are retained as
// audit evidence, but each must precede that task's successful later report.
// It is intentionally stricter than a report-path count: paths alone say
// nothing about whether cleanup actually completed.
func ValidateTerminalCleanupReports(paths []string, repository string, expectedTasks []string) error {
	expected := make(map[string]bool, len(expectedTasks))
	for _, task := range expectedTasks {
		if task == "" || expected[task] {
			return fmt.Errorf("terminal cleanup expected task identities are inconsistent")
		}
		expected[task] = true
	}
	if len(expected) == 0 {
		return fmt.Errorf("terminal cleanup has no expected tasks")
	}
	seenPaths := make(map[string]bool, len(paths))
	success := make(map[string]int, len(expected))
	failures := make(map[string][]int, len(expected))
	var previous time.Time
	for index, path := range paths {
		if !filepath.IsAbs(path) || seenPaths[path] {
			return fmt.Errorf("terminal cleanup report path %q is not one unique absolute path", path)
		}
		seenPaths[path] = true
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("stat terminal cleanup report %s: %w", path, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("terminal cleanup report %s is not a regular file", path)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read terminal cleanup report %s: %w", path, err)
		}
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		var report cleanupReport
		if err := decoder.Decode(&report); err != nil {
			return fmt.Errorf("decode terminal cleanup report %s: %w", path, err)
		}
		if err := requireJSONEOF(decoder); err != nil {
			return fmt.Errorf("decode terminal cleanup report %s: %w", path, err)
		}
		if report.GeneratedAt.IsZero() || (!previous.IsZero() && !report.GeneratedAt.After(previous)) {
			return fmt.Errorf("terminal cleanup report %s has non-monotonic generated_at", path)
		}
		previous = report.GeneratedAt
		if report.Phase != "applied" || !report.Apply || !expected[report.Task] || len(report.Results) != 1 {
			return fmt.Errorf("terminal cleanup report %s has inconsistent applied schema", path)
		}
		result := report.Results[0]
		if result.Task != report.Task || result.Repository != repository {
			return fmt.Errorf("terminal cleanup report %s does not match receipt task/repository identity", path)
		}
		if result.Applied {
			if !result.WorktreeGone || !result.BranchDeleted || success[result.Task] != 0 {
				return fmt.Errorf("terminal cleanup report %s has inconsistent successful cleanup evidence", path)
			}
			success[result.Task] = index + 1
			continue
		}
		if result.WorktreeGone && result.BranchDeleted || strings.TrimSpace(result.Reason) == "" {
			return fmt.Errorf("terminal cleanup report %s has inconsistent failed cleanup evidence", path)
		}
		failures[result.Task] = append(failures[result.Task], index+1)
	}
	for task := range expected {
		completed := success[task]
		if completed == 0 {
			return fmt.Errorf("terminal cleanup reports do not prove task %s completed", task)
		}
		for _, failed := range failures[task] {
			if failed >= completed {
				return fmt.Errorf("terminal cleanup reports leave task %s failed after its claimed completion", task)
			}
		}
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
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
	// selected are this task's own eligible artifact indices, resolved before
	// any task started (see cleanupApplyEntry). Rescanning every artifact here
	// would read rows a concurrently applying task is writing.
	selected []int,
	artifacts []LifecycleArtifact,
) (*os.File, string, []cleanupLifecycleArtifactHandle, error) {
	handles := make([]cleanupLifecycleArtifactHandle, 0)
	for _, index := range selected {
		artifact := artifacts[index]
		// A task namespace is retired in place, not archived: it has no
		// reserved WB name and is not a directory inside the task.
		if artifact.Kind == lifecycleArtifactKindTaskNamespace {
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
	task       *cleanupTaskHandle
	parentPath string
	parentName string
	parent     *os.File
	// closeParent means the parent is a WB-owned <task>/<owner> directory to
	// retire once empty — see removeEmptyParent. ownParent means this handle
	// opened parent itself and must close its descriptor regardless; the two
	// differ for an adopted worktree, whose parent is a real directory
	// outside every WB task that must never be retired, but whose freshly
	// opened descriptor still has to be closed like any other.
	closeParent  bool
	ownParent    bool
	worktreePath string
	worktree     *os.File
}

func (handle *cleanupWorktreeHandle) validate() error {
	// An adopted worktree's handle carries no task: it was never relocated
	// under one, so there is no held task descriptor to reauthorize here —
	// see openAdoptedCleanupWorktree.
	if handle.task != nil {
		if err := handle.task.validate(); err != nil {
			return err
		}
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
	// Reauthorize once more after the test seam so a replacement is surfaced,
	// never blindly acted on.
	if !directoryStillMatches(handle.parentPath, handle.parent) {
		return fmt.Errorf("cleanup worktree parent path changed before retention: %s", handle.parentPath)
	}
	// Retire the owner directory when it is now empty, so a terminal task
	// leaves no residue in its active namespace (#req:internal-stage-terminalization
	// covers reserved .wb-stage-*/.wb-retired-stage-* entries; an ordinary
	// empty <task>/<owner> directory left after the last repository under it
	// is cleaned up is the same class of residue and gets the same
	// treatment). The task lock is still held here, so no concurrent WB
	// operation for this same task can be adding a sibling repository
	// underneath this owner directory. AT_REMOVEDIR is itself atomic against
	// any other writer: it refuses with ENOTEMPTY rather than destroying
	// content, which is exactly how a sibling repository still present under
	// the same owner (this task not yet fully terminal) is left in place,
	// exactly as before. Any other unexpected outcome (a concurrent
	// replacement, a symlink swapped in, ...) is likewise left untouched —
	// this is a best-effort housekeeping step, never grounds to fail a
	// cleanup transaction whose branch and worktree removal already applied.
	_ = unix.Unlinkat(int(handle.task.task.Fd()), handle.parentName, unix.AT_REMOVEDIR)
	return nil
}

func (handle *cleanupWorktreeHandle) close() {
	if handle.worktree != nil {
		_ = handle.worktree.Close()
	}
	if handle.ownParent && handle.parent != nil {
		_ = handle.parent.Close()
	}
}

type githubPullRequest struct {
	Number         int        `json:"number"`
	URL            string     `json:"html_url"`
	State          string     `json:"state"`
	Base           githubRef  `json:"base"`
	Head           githubRef  `json:"head"`
	MergeCommitSHA string     `json:"merge_commit_sha"`
	MergedAt       *time.Time `json:"merged_at"`
}

type githubRef struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
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
	options.OwnerState = strings.TrimSpace(options.OwnerState)
	if options.OwnerState != "" && options.OwnerState != "active" && options.OwnerState != "orphaned" {
		return ListOutcome{}, fmt.Errorf("unsupported owner state %q; use active or orphaned", options.OwnerState)
	}
	tasks, err := normalizeTaskSelection(options.Task, options.Tasks)
	if err != nil {
		return ListOutcome{}, err
	}
	// normalizeListOptions owns the common path/base/filter validation. Task
	// selection is normalized above because it may now contain several exact
	// names rather than one string.
	options.Task = ""
	projectsRoot, _, base, filter, err := normalizeListOptions(options)
	if err != nil {
		return ListOutcome{}, err
	}
	resolution, err := wbhome.Resolve(projectsRoot)
	if err != nil {
		return ListOutcome{}, err
	}
	resolution.Read, err = appendConfiguredSharedWorktreesLayout(resolution.Read)
	if err != nil {
		return ListOutcome{}, err
	}
	outcome := ListOutcome{SchemaVersion: 1}
	// One inventory walk asks the same question once per worktree, and a fleet
	// keeps many worktrees per repository — 262 worktrees across 71 repositories
	// on the fleet this was measured against, so 73% of the fetches re-learned a
	// SHA already in hand. Scope the memo to this walk: every task in a
	// repository is then judged against one consistent target instead of
	// whichever SHA happened to be current when its own fetch ran, and the
	// pre-deletion recheck in preflightCleanupRepository still runs on the
	// caller's own context, so it stays a genuinely fresh fetch.
	ctx = withTargetHeadCache(ctx)
	if options.Workers < 1 {
		options.Workers = DefaultInspectWorkers
	}
	reporter := &listProgressReporter{report: options.Progress}
	policy := inspectPolicy{
		includeDetached: options.IncludeDetached,
		ttl:             options.TTL,
		residueEvidence: options.ResidueEvidence,
		residueDepth:    options.ResidueDepth,
		activity:        options.Activity,
		now:             options.Now,
	}
	for _, layout := range resolution.Read {
		results, diagnostics, artifacts, purged, listErr := listLayout(
			ctx, projectsRoot, resolution.Write.Home, layout, taskSelectionSet(tasks), base, filter, options.AbsorbedBy, options.GitHub, options.Workers, reporter, policy,
		)
		if listErr != nil {
			return ListOutcome{}, listErr
		}
		outcome.Results = append(outcome.Results, results...)
		outcome.Diagnostics = append(outcome.Diagnostics, diagnostics...)
		outcome.Artifacts = append(outcome.Artifacts, artifacts...)
		outcome.Purged = append(outcome.Purged, purged...)
	}
	localLayouts, localDiscoveryDiagnostics := discoverCanonicalLocalWorktreeLayouts(ctx, projectsRoot)
	outcome.Diagnostics = append(outcome.Diagnostics, localDiscoveryDiagnostics...)
	for _, layout := range localLayouts {
		results, diagnostics, artifacts, listErr := listCanonicalLocalLayout(
			ctx, projectsRoot, resolution.Write.Home, layout, taskSelectionSet(tasks), base, filter, options.AbsorbedBy, options.GitHub, options.Workers, reporter, policy,
		)
		if listErr != nil {
			return ListOutcome{}, listErr
		}
		outcome.Results = append(outcome.Results, results...)
		outcome.Diagnostics = append(outcome.Diagnostics, diagnostics...)
		outcome.Artifacts = append(outcome.Artifacts, artifacts...)
	}
	// A user-scoped shared root is a placement preference, not an ownership
	// boundary. Once it changes, an existing managed checkout must remain
	// discoverable from Git's registry and its own active private claim. The
	// claim corroborates both the exact path and task identity; a merely
	// similarly-shaped external worktree remains external.
	known := make(map[string]bool, len(outcome.Results))
	for _, result := range outcome.Results {
		known[filepath.Clean(result.WorktreeDir)] = true
	}
	claimed, claimDiagnostics := listClaimedRegistryWorktrees(
		ctx, projectsRoot, resolution.Write.Home, known, taskSelectionSet(tasks), base, filter, options.AbsorbedBy, options.GitHub, options.Workers, reporter, policy,
	)
	outcome.Results = append(outcome.Results, claimed...)
	outcome.Diagnostics = append(outcome.Diagnostics, claimDiagnostics...)
	if options.OwnerState != "" {
		filtered := outcome.Results[:0]
		for _, result := range outcome.Results {
			if result.OwnerState == options.OwnerState {
				filtered = append(filtered, result)
			}
		}
		outcome.Results = filtered
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
	sort.Slice(outcome.Purged, func(i, j int) bool { return outcome.Purged[i].Path < outcome.Purged[j].Path })
	return outcome, nil
}

// discoverCanonicalLocalWorktreeLayouts finds only `<owner>/<repository>`
// canonical clones that already contain the default `.worktrees` root. It
// does not descend through repositories, so a task checkout can never be
// discovered as another canonical clone.
func discoverCanonicalLocalWorktreeLayouts(ctx context.Context, projectsRoot string) ([]wbhome.Layout, []ListDiagnostic) {
	owners, err := os.ReadDir(projectsRoot)
	if err != nil {
		return nil, []ListDiagnostic{listDiagnostic("", "", projectsRoot, fmt.Sprintf("read projects root for canonical local worktrees: %v", err))}
	}
	layouts := make([]wbhome.Layout, 0)
	diagnostics := make([]ListDiagnostic, 0)
	for _, owner := range owners {
		ownerPath := filepath.Join(projectsRoot, owner.Name())
		ownerInfo, infoErr := owner.Info()
		if infoErr != nil {
			diagnostics = append(diagnostics, listDiagnostic("", "", ownerPath, fmt.Sprintf("inspect canonical owner entry: %v", infoErr)))
			continue
		}
		if !ownerInfo.IsDir() || ownerInfo.Mode()&os.ModeSymlink != 0 || !validSafeSegment(owner.Name()) {
			continue
		}
		repositories, readErr := os.ReadDir(ownerPath)
		if readErr != nil {
			diagnostics = append(diagnostics, listDiagnostic("", "", ownerPath, fmt.Sprintf("read canonical owner directory: %v", readErr)))
			continue
		}
		for _, repository := range repositories {
			canonical := filepath.Join(ownerPath, repository.Name())
			repositoryInfo, repositoryInfoErr := repository.Info()
			if repositoryInfoErr != nil {
				diagnostics = append(diagnostics, listDiagnostic("", "", canonical, fmt.Sprintf("inspect canonical repository entry: %v", repositoryInfoErr)))
				continue
			}
			if !repositoryInfo.IsDir() || repositoryInfo.Mode()&os.ModeSymlink != 0 || !validRepositorySegment(repository.Name()) {
				continue
			}
			root := filepath.Join(canonical, ".worktrees")
			info, statErr := os.Lstat(root)
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr != nil {
				diagnostics = append(diagnostics, listDiagnostic(root, "", root, fmt.Sprintf("inspect canonical local worktrees root: %v", statErr)))
				continue
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			gitDir, commonDir, gitErr := gitDirectories(ctx, canonical)
			if gitErr != nil {
				diagnostics = append(diagnostics, listDiagnostic(root, "", canonical, fmt.Sprintf("verify canonical local Git identity: %v", gitErr)))
				continue
			}
			if filepath.Clean(gitDir) != filepath.Clean(commonDir) || filepath.Clean(commonDir) != filepath.Join(canonical, ".git") {
				continue
			}
			layouts = append(layouts, wbhome.Layout{WorktreesRoot: root, Local: true})
		}
	}
	sort.Slice(layouts, func(i, j int) bool { return layouts[i].WorktreesRoot < layouts[j].WorktreesRoot })
	sort.Slice(diagnostics, func(i, j int) bool { return diagnostics[i].Path < diagnostics[j].Path })
	return layouts, diagnostics
}

func listCanonicalLocalLayout(
	ctx context.Context,
	projectsRoot, home string,
	layout wbhome.Layout,
	tasks map[string]bool,
	base, filter, absorbedBy string,
	withGitHub bool,
	workers int,
	reporter *listProgressReporter,
	policy inspectPolicy,
) ([]ListResult, []ListDiagnostic, []LifecycleArtifact, error) {
	entries, err := os.ReadDir(layout.WorktreesRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read canonical local worktrees under %s: %w", layout.WorktreesRoot, err)
	}
	pending := make([]pendingInspect, 0)
	diagnostics := make([]ListDiagnostic, 0)
	artifacts := make([]LifecycleArtifact, 0)
	rootArtifacts := make([]LifecycleArtifact, 0)
	for _, entry := range entries {
		path := filepath.Join(layout.WorktreesRoot, entry.Name())
		if artifact, internal := inspectLifecycleArtifact(ctx, layout.WorktreesRoot, "", path, entry); internal {
			// Local placement has no task namespace between .worktrees and the
			// checkout. An active sibling stage may be between mkdir and git
			// worktree add, so it cannot be retired without its authoritative
			// WB_HOME task lock. A nofollow-verified empty retired stage is the
			// terminal state creation itself leaves behind; report it without
			// blocking an unrelated task's rename or cleanup.
			if artifact.State == "staging" || !artifact.Eligible {
				artifact.Eligible = false
				artifact.Disposition = "unscoped_local_stage"
				artifact.Reason = "canonical local sibling stage has no task lock identity; preserve it until its owning WB_HOME task recovery is explicit"
				rootArtifacts = append(rootArtifacts, artifact)
			} else {
				artifact.Disposition = "empty_unscoped_local_retired_stage"
				artifact.Reason = "empty retired canonical local sibling stage is terminal residue; no task cleanup action is authorized"
			}
			artifacts = append(artifacts, artifact)
			continue
		}
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || !taskSelectionMatches(tasks, entry.Name()) {
			continue
		}
		if !validSafeSegment(entry.Name()) {
			diagnostics = append(diagnostics, listDiagnostic(layout.WorktreesRoot, entry.Name(), filepath.Join(layout.WorktreesRoot, entry.Name()), "invalid task directory name"))
			continue
		}
		if !hasGitMetadata(path) || !isGitRoot(ctx, path) {
			diagnostics = append(diagnostics, listDiagnostic(layout.WorktreesRoot, entry.Name(), path, "canonical local task is not a Git worktree root"))
			continue
		}
		locked, lockErr := inspectLifecycleTaskLock(home, layout, entry.Name())
		if lockErr != nil {
			diagnostics = append(diagnostics, listDiagnostic(layout.WorktreesRoot, entry.Name(), path, fmt.Sprintf("inspect authoritative task lock: %v", lockErr)))
			continue
		}
		pending = append(pending, pendingInspect{task: entry.Name(), path: path, locked: locked, commonDir: gitCommonDir(ctx, path)})
	}
	results, inspectDiagnostics := runInspections(ctx, pending, projectsRoot, home, layout, base, filter, absorbedBy, withGitHub, workers, reporter, policy)
	diagnostics = append(diagnostics, inspectDiagnostics...)
	// A top-level local stage has no task segment to associate with. It still
	// makes the local root's inventory incomplete, so it blocks every physical
	// member we did validate rather than being silently ignored by cleanup.
	for _, artifact := range rootArtifacts {
		for _, result := range results {
			diagnostics = append(diagnostics, listDiagnostic(layout.WorktreesRoot, result.Task, artifact.Path,
				"canonical local lifecycle stage blocks cleanup until recovered: "+artifact.Reason))
		}
	}
	return results, diagnostics, artifacts, nil
}

// listClaimedRegistryWorktrees recovers managed shared placements after the
// user changes worktrees.root. The old root is not guessed from its path: Git
// must still register the checkout and the checkout's projection must
// corroborate an active immutable WB claim. This deliberately excludes an
// unclaimed arbitrary worktree even when it happens to resemble WB's layout.
func listClaimedRegistryWorktrees(
	ctx context.Context,
	projectsRoot, home string,
	known map[string]bool,
	tasks map[string]bool,
	base, filter, absorbedBy string,
	withGitHub bool,
	workers int,
	reporter *listProgressReporter,
	policy inspectPolicy,
) ([]ListResult, []ListDiagnostic) {
	clones, unscanned := discoverCanonicalClones(projectsRoot)
	diagnostics := make([]ListDiagnostic, 0, len(unscanned))
	for _, item := range unscanned {
		diagnostics = append(diagnostics, listDiagnostic("", "", item, "cannot inspect canonical Git registry"))
	}
	pending := make([]pendingInspect, 0)
	for _, clone := range clones {
		linked, err := linkedWorktreesOf(ctx, clone.path)
		if err != nil {
			diagnostics = append(diagnostics, listDiagnostic("", "", clone.path, fmt.Sprintf("read canonical Git worktree registry: %v", err)))
			continue
		}
		for _, linkedWorktree := range linked {
			path := filepath.Clean(linkedWorktree.path)
			if linkedWorktree.missing {
				claim, claimErr := activeWorkLogClaimAtPath(home, path, tasks)
				if claimErr != nil {
					diagnostics = append(diagnostics, listDiagnostic("", "", path, fmt.Sprintf("inspect missing registered worktree ownership: %v", claimErr)))
					continue
				}
				if claim != nil {
					layout, layoutErr := claimedSharedWorktreeLayout(path, *claim)
					root := ""
					if layoutErr == nil {
						root = layout.WorktreesRoot
					}
					diagnostics = append(diagnostics, listDiagnostic(root, claim.Task, path,
						"Git still registers this active WB-managed worktree but its working tree is missing; preserve the claim and recover or prune it explicitly"))
				}
				continue
			}
			if known[path] {
				continue
			}
			claim, _, _, claimErr := activeWorkLogClaim(home, path)
			if claimErr != nil {
				// Most Git worktrees are not WB-managed. A real local manifest
				// makes a claim failure material evidence rather than absence.
				if manifest, manifestErr := ReadManifest(path); manifestErr == nil && validSafeSegment(manifest.EffortID) {
					diagnostics = append(diagnostics, listDiagnostic("", manifest.EffortID, path, fmt.Sprintf("corroborate managed registry worktree claim: %v", claimErr)))
				}
				continue
			}
			if !taskSelectionMatches(tasks, claim.Task) {
				continue
			}
			layout, layoutErr := claimedSharedWorktreeLayout(path, claim)
			if layoutErr != nil {
				diagnostics = append(diagnostics, listDiagnostic("", claim.Task, path, layoutErr.Error()))
				continue
			}
			if !filterMatches(filter, claim.Repository, path) {
				continue
			}
			locked, lockErr := inspectLifecycleTaskLock(home, layout, claim.Task)
			if lockErr != nil {
				diagnostics = append(diagnostics, listDiagnostic(layout.WorktreesRoot, claim.Task, path, fmt.Sprintf("inspect authoritative task lock: %v", lockErr)))
				continue
			}
			pending = append(pending, pendingInspect{task: claim.Task, path: path, slug: claim.Repository,
				locked: locked, commonDir: gitCommonDir(ctx, path)})
			known[path] = true
		}
	}
	if len(pending) == 0 {
		return nil, diagnostics
	}
	results := make([]ListResult, 0, len(pending))
	// Every queued path has its own verified physical root, so inspect one at
	// a time. This retains the normal per-canonical serialization while not
	// treating the currently configured shared root as an ownership oracle.
	for _, pendingEntry := range pending {
		claim, _, _, err := activeWorkLogClaim(home, pendingEntry.path)
		if err != nil {
			diagnostics = append(diagnostics, listDiagnostic("", pendingEntry.task, pendingEntry.path, fmt.Sprintf("re-read managed registry claim: %v", err)))
			continue
		}
		layout, err := claimedSharedWorktreeLayout(pendingEntry.path, claim)
		if err != nil {
			diagnostics = append(diagnostics, listDiagnostic("", pendingEntry.task, pendingEntry.path, err.Error()))
			continue
		}
		inspectedResults, inspected := runInspections(ctx, []pendingInspect{pendingEntry}, projectsRoot, home, layout, base, filter, absorbedBy, withGitHub, workers, reporter, policy)
		results = append(results, inspectedResults...)
		diagnostics = append(diagnostics, inspected...)
	}
	return results, diagnostics
}

// activeWorkLogClaimAtPath finds the immutable active claim for a registered
// worktree whose directory is already gone, so its editable projection can no
// longer be read. It is deliberately task-scoped before opening private claim
// runs: one named cleanup must neither report nor act on another task.
func activeWorkLogClaimAtPath(home, worktree string, tasks map[string]bool) (*workLogClaim, error) {
	worklogs, err := openAbsoluteDirectoryNoFollow(filepath.Join(home, "worklogs"), false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = worklogs.Close() }()
	efforts, err := worklogs.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	sort.Strings(efforts)
	for _, effort := range efforts {
		if !validSafeSegment(effort) || !taskSelectionMatches(tasks, effort) {
			continue
		}
		effortDirectory, openErr := openPrivateChild(worklogs, effort, false)
		if openErr != nil {
			return nil, openErr
		}
		runs, runErr := openPrivateChild(effortDirectory, "runs", false)
		_ = effortDirectory.Close()
		if runErr != nil {
			return nil, runErr
		}
		runNames, readErr := runs.Readdirnames(-1)
		if readErr != nil {
			_ = runs.Close()
			return nil, readErr
		}
		sort.Strings(runNames)
		for _, run := range runNames {
			if !validSafeSegment(run) {
				_ = runs.Close()
				return nil, fmt.Errorf("unsafe Work Log run %q", run)
			}
			runDirectory, openErr := openPrivateChild(runs, run, false)
			if openErr != nil {
				_ = runs.Close()
				return nil, openErr
			}
			claims, claimsErr := openPrivateChild(runDirectory, "claims", false)
			_ = runDirectory.Close()
			if claimsErr != nil {
				_ = runs.Close()
				return nil, claimsErr
			}
			claimNames, namesErr := claims.Readdirnames(-1)
			if namesErr != nil {
				_ = claims.Close()
				_ = runs.Close()
				return nil, namesErr
			}
			sort.Strings(claimNames)
			for _, name := range claimNames {
				claimID := strings.TrimSuffix(name, ".json")
				if name != claimID+".json" || !validClaimID(claimID) {
					_ = claims.Close()
					_ = runs.Close()
					return nil, fmt.Errorf("unsafe Work Log claim entry %q", name)
				}
				var claim workLogClaim
				if readErr := readJSONAt(claims, name, &claim); readErr != nil {
					_ = claims.Close()
					_ = runs.Close()
					return nil, readErr
				}
				if claim.Lifecycle == "active" && claim.Task == effort && filepath.Clean(claim.Worktree) == filepath.Clean(worktree) {
					_ = claims.Close()
					_ = runs.Close()
					return &claim, nil
				}
			}
			_ = claims.Close()
		}
		_ = runs.Close()
	}
	return nil, nil
}

// claimedSharedWorktreeLayout proves the sole accepted old-shared-root shape.
// An adopted checkout has an active claim too, but it is intentionally not
// accepted here: adoption remains represented by its WB-home pointer and its
// ListResult.External flag rather than becoming a managed shared worktree.
func claimedSharedWorktreeLayout(path string, claim workLogClaim) (wbhome.Layout, error) {
	owner, repository, err := splitRepository(claim.Repository)
	if err != nil || !validSafeSegment(claim.Task) {
		return wbhome.Layout{}, fmt.Errorf("managed registry claim has invalid repository or task identity")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	expected := filepath.Join(root, claim.Task, owner, repository)
	if filepath.Clean(expected) != filepath.Clean(path) {
		return wbhome.Layout{}, fmt.Errorf("active WB claim does not corroborate shared worktree layout")
	}
	return wbhome.Layout{WorktreesRoot: root}, nil
}

// lifecycleTaskLockRoot keeps physical placement separate from WB's logical
// task authority. Default-local and user-configured shared placements use the
// WB_HOME task lock; the historic WB_HOME and projects-root legacy layouts
// retain their physical lock roots for compatibility.
func lifecycleTaskLockRoot(home string, layout wbhome.Layout) string {
	current := filepath.Join(home, "worktrees")
	if layout.Local || (!layout.Legacy && filepath.Clean(layout.WorktreesRoot) != filepath.Clean(current)) {
		return current
	}
	return layout.WorktreesRoot
}

func inspectLifecycleTaskLock(home string, layout wbhome.Layout, task string) (bool, error) {
	_, err := os.Lstat(filepath.Join(lifecycleTaskLockRoot(home, layout), task, ".lock"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// cleanupInspectPolicy is the one place a cleanup transaction's widenings
// become an inventory policy, so the plan, the preflight, and the final
// re-inspection under the task lock all ask the same question.
func cleanupInspectPolicy(options CleanupOptions) inspectPolicy {
	return inspectPolicy{
		includeDetached: options.IncludeDetached,
		ttl:             options.TTL,
		residueEvidence: cleanupWantsResidueEvidence(options),
		residueDepth:    options.ResidueDepth,
		activity:        options.Activity,
		now:             options.Now,
	}
}

// cleanupWantsResidueEvidence keeps the extra commit-index reads where they
// earn their cost. A named task is a handful of candidates and its operator is
// asking "why can I not finish this?", which is exactly the question
// "landed + residue" answers. A fleet-wide --all-merged sweep walks dozens of
// legitimately unlanded worktrees, and paying up to ResidueDepth API reads for
// each of them to re-learn that they are unlanded is the redundant work that
// verbs are required not to do.
func cleanupWantsResidueEvidence(options CleanupOptions) bool {
	return options.AllowResidue || len(options.Tasks) > 0 || options.Task != ""
}

func listLayout(
	ctx context.Context,
	projectsRoot string,
	home string,
	layout wbhome.Layout,
	tasks map[string]bool,
	base, filter, absorbedBy string,
	withGitHub bool,
	workers int,
	reporter *listProgressReporter,
	policy inspectPolicy,
) ([]ListResult, []ListDiagnostic, []LifecycleArtifact, []PurgedArtefact, error) {
	taskEntries, err := os.ReadDir(layout.WorktreesRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("read worktree tasks under %s: %w", layout.WorktreesRoot, err)
	}
	results := make([]ListResult, 0)
	diagnostics := make([]ListDiagnostic, 0)
	artifacts := make([]LifecycleArtifact, 0)
	purged := make([]PurgedArtefact, 0)
	// The walk below is local and cheap; inspection is what contacts origin.
	// Collect candidates first, then inspect them concurrently, so one slow
	// remote cannot hold up the other several hundred.
	pending := make([]pendingInspect, 0)
	for _, taskEntry := range taskEntries {
		if !taskEntry.IsDir() || strings.HasPrefix(taskEntry.Name(), ".") || !taskSelectionMatches(tasks, taskEntry.Name()) {
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
		// Sweep this task's terminal artefacts before the directory is read, so
		// what they leave behind never reaches the inventory as backlog and
		// never becomes an `info:` line. See purgeTerminalArtefacts.
		purged = append(purged, purgeTerminalArtefacts(layout.WorktreesRoot, taskEntry.Name())...)
		_, lockErr := os.Stat(filepath.Join(taskRoot, ".lock"))
		locked := lockErr == nil
		if lockErr != nil && !vanishedDuringWalk(lockErr) {
			// A task retired by another run reports ErrNotExist here and is
			// indistinguishable from an unlocked task, so it falls through to the
			// directory read below, which recognises the vanished task for what it
			// is. Anything else is a real inspection failure, and it is scoped to
			// this task rather than discarding every other task in the sweep.
			diagnostics = append(diagnostics, listDiagnostic(layout.WorktreesRoot, taskEntry.Name(), taskRoot, fmt.Sprintf("inspect task lock: %v", lockErr)))
			continue
		}
		entries, readErr := os.ReadDir(taskRoot)
		if readErr != nil {
			// A task directory that disappeared mid-walk has already reached the
			// state cleanup exists to produce, so converging on it is success and
			// stays silent. Any other read failure is scoped to this task alone.
			if vanishedDuringWalk(readErr) {
				continue
			}
			diagnostics = append(diagnostics, listDiagnostic(layout.WorktreesRoot, taskEntry.Name(), taskRoot, fmt.Sprintf("read task directory: %v", readErr)))
			continue
		}
		for _, entry := range entries {
			candidate := filepath.Join(taskRoot, entry.Name())
			if artifact, internal := inspectLifecycleArtifact(ctx, layout.WorktreesRoot, taskEntry.Name(), candidate, entry); internal {
				artifacts = append(artifacts, artifact)
				continue
			}
			if !entry.IsDir() {
				continue
			}
			if hasGitMetadata(candidate) && isGitRoot(ctx, candidate) {
				pending = append(pending, pendingInspect{
					task: taskEntry.Name(), path: candidate, slug: entry.Name(),
					locked: locked, commonDir: gitCommonDir(ctx, candidate),
				})
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
				// A current-layout path already carries its raw owner/repository
				// identity. Apply --filter before starting a Git subprocess so a
				// narrow inventory does not validate every historical checkout.
				// Repository-rename mismatches remain visible when their on-disk
				// identity matches the filter; the documented filter contract is
				// path-derived identity, not an unbounded canonical-name search.
				if !filterMatches(filter, repositoryPath, slug) {
					continue
				}
				if hasGitMetadata(repositoryPath) && isGitRoot(ctx, repositoryPath) {
					pending = append(pending, pendingInspect{
						task: taskEntry.Name(), path: repositoryPath, ownerName: entry.Name(),
						slug: slug, locked: locked, commonDir: gitCommonDir(ctx, repositoryPath),
					})
					continue
				}
				// An adopted external worktree registers here as a plain
				// directory holding one pointer file instead of Git metadata —
				// see readAdoptedWorktreePointer. Everything past this point
				// operates on the real, never-relocated checkout the pointer
				// names, exactly as if it had been created there directly.
				if external, ok := readAdoptedWorktreePointer(repositoryPath); ok {
					if !filterMatches(filter, external, slug) {
						continue
					}
					if !hasGitMetadata(external) || !isGitRoot(ctx, external) {
						diagnostics = append(diagnostics, listDiagnostic(layout.WorktreesRoot, taskEntry.Name(), repositoryPath,
							fmt.Sprintf("adopted worktree registration points at %s, which is no longer a Git worktree root", external)))
						continue
					}
					pending = append(pending, pendingInspect{
						task: taskEntry.Name(), path: external, ownerName: entry.Name(),
						slug: slug, locked: locked, commonDir: gitCommonDir(ctx, external), external: true,
					})
					continue
				}
				if strings.HasPrefix(repositoryEntry.Name(), ".") {
					continue
				}
				if !validSafeSegment(repositoryEntry.Name()) {
					diagnostics = append(diagnostics, listDiagnostic(layout.WorktreesRoot, taskEntry.Name(), repositoryPath, "invalid repository directory name"))
					continue
				}
				diagnostic := listDiagnostic(layout.WorktreesRoot, taskEntry.Name(), repositoryPath, "candidate is not a Git worktree root")
				canonicalPath := filepath.Join(projectsRoot, entry.Name(), repositoryEntry.Name())
				if !hasGitMetadata(canonicalPath) || !isGitRoot(ctx, canonicalPath) {
					diagnostic.NonBlocking = true
					diagnostic.Message = "foreign non-Git debris (no canonical repository); visible but does not block valid siblings"
				}
				diagnostics = append(diagnostics, diagnostic)
			}
		}
	}
	inspected, inspectDiagnostics := runInspections(
		ctx, pending, projectsRoot, home, layout, base, filter, absorbedBy, withGitHub, workers, reporter, policy,
	)
	results = append(results, inspected...)
	diagnostics = append(diagnostics, inspectDiagnostics...)
	return results, diagnostics, artifacts, purged, nil
}

// runInspections inspects every queued candidate, up to workers at a time,
// serialised per canonical clone.
//
// Output order is not preserved and does not need to be: ListWithDiagnostics
// sorts results and diagnostics by (task, repository, path) before returning,
// so this concurrent phase produces exactly what the serial one did.
func runInspections(
	ctx context.Context,
	pending []pendingInspect,
	projectsRoot string,
	home string,
	layout wbhome.Layout,
	base, filter, absorbedBy string,
	withGitHub bool,
	workers int,
	reporter *listProgressReporter,
	policy inspectPolicy,
) ([]ListResult, []ListDiagnostic) {
	if len(pending) == 0 {
		return nil, nil
	}
	if workers < 1 {
		workers = 1
	}
	if workers > len(pending) {
		workers = len(pending)
	}

	locks := newCloneLocks()
	jobs := make(chan pendingInspect)
	var (
		mu          sync.Mutex
		results     []ListResult
		diagnostics []ListDiagnostic
		wg          sync.WaitGroup
	)

	go func() {
		defer close(jobs)
		for _, job := range pending {
			jobs <- job
		}
	}()

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				token := reporter.start(job.task, job.path, withGitHub)

				lock := locks.get(job.commonDir)
				lock.Lock()
				result, inspectErr := inspectLifecycleWorktree(
					ctx, projectsRoot, home, layout, job.task, job.path, base, absorbedBy, withGitHub, job.locked, job.external, policy,
				)
				lock.Unlock()

				reporter.finish(token, job.task, result.Repository, job.path, withGitHub)

				mu.Lock()
				switch {
				case inspectErr != nil:
					if filterMatches(filter, inspectErrorFilterCandidates(job.ownerName, job.path, job.slug, inspectErr)...) {
						diagnostics = append(diagnostics, listDiagnosticForInspectError(
							layout.WorktreesRoot, job.task, job.path, job.ownerName, inspectErr,
						))
					}
				case filterMatches(filter, result.Repository):
					results = append(results, result)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return results, diagnostics
}

func inspectLifecycleArtifact(ctx context.Context, worktreesRoot, task, path string, entry os.DirEntry) (LifecycleArtifact, bool) {
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
		if repository, err := OriginSlug(ctx, path); err == nil {
			artifact.Repository = repository
		}
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

// vanishedDuringWalk reports whether err means the path stopped existing while
// the sweep was walking it. A concurrent cleanup retiring a task is the normal
// state of a fleet with more than one agent on it, and the retired task is
// precisely the outcome this command converges on, so callers skip it rather
// than failing the whole run.
func vanishedDuringWalk(err error) bool {
	return errors.Is(err, os.ErrNotExist)
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

// hasGitMetadata avoids spawning Git for ordinary task and owner directories.
// Every Git worktree root has a .git entry (normally a gitdir file), and a
// candidate with any .git entry still goes through isGitRoot for authoritative
// validation. An unreadable entry deliberately remains a Git candidate so the
// existing Git diagnostic path is preserved.
func hasGitMetadata(path string) bool {
	_, err := os.Lstat(filepath.Join(path, ".git"))
	return err == nil || !errors.Is(err, os.ErrNotExist)
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
	// Remote parked-session receivers use a resume/member-derived physical
	// task directory so concurrent resumes cannot collide. Their immutable
	// manifest retains the logical effort, which is what an operator naturally
	// supplies to cleanup. Resolve that alias before inventory so the existing
	// descriptor-anchored cleanup transaction remains the only removal path.
	aliasLayouts, err := appendConfiguredSharedWorktreesLayout(resolution.Read)
	if err != nil {
		return CleanupOutcome{}, err
	}
	// The authoritative inventory below reports discovery diagnostics with
	// task scope. Alias expansion itself only uses roots it could validate.
	localLayouts, _ := discoverCanonicalLocalWorktreeLayouts(ctx, normalized.ProjectsRoot)
	aliasLayouts = append(aliasLayouts, localLayouts...)
	normalized.Tasks, err = resolveLogicalCleanupTasks(aliasLayouts, normalized.Tasks)
	if err != nil {
		return CleanupOutcome{}, err
	}
	normalized.Task = ""
	if len(normalized.Tasks) == 1 {
		normalized.Task = normalized.Tasks[0]
	}
	// The report is a mutation too. Probe the platform capability before
	// creating its default directory or making any other apply-time change.
	if normalized.Apply {
		if err := requireGitFilesystemCapability(); err != nil {
			return CleanupOutcome{}, err
		}
	}
	var recoveredTask *cleanupTaskHandle
	var recovery *InterruptedLockRecovery
	if normalized.ResumeInterrupted {
		recoveredTask, recovery, err = reclaimNamedInterruptedCleanupTask(resolution, normalized.Task)
		if err != nil {
			return CleanupOutcome{}, err
		}
		defer func() {
			if recoveredTask == nil {
				return
			}
			// Validation alone never authorizes a state transition. Only an
			// eligible normal cleanup transaction takes ownership and releases
			// this lock after its own terminal gates have passed.
			recoveredTask.preserveLock()
			recoveredTask.close()
		}()
		if normalized.afterResumeInterruptedLock != nil {
			if err := normalized.afterResumeInterruptedLock(recovery.Path); err != nil {
				return CleanupOutcome{}, err
			}
		}
	}
	now := normalized.Now()
	if normalized.ReportDir == "" && normalized.Apply {
		normalized.ReportDir = DefaultCleanupReportDir(resolution.Write.Home, now)
	}
	listed, err := ListWithDiagnostics(ctx, ListOptions{
		ProjectsRoot:    normalized.ProjectsRoot,
		Tasks:           normalized.Tasks,
		Base:            normalized.Base,
		Filter:          normalized.Filter,
		AbsorbedBy:      normalized.AbsorbedBy,
		GitHub:          true,
		Progress:        normalized.Progress,
		Workers:         normalized.Workers,
		IncludeDetached: normalized.IncludeDetached,
		TTL:             normalized.TTL,
		Activity:        normalized.Activity,
		ResidueEvidence: cleanupWantsResidueEvidence(normalized),
		ResidueDepth:    normalized.ResidueDepth,
		Now:             normalized.Now,
	})
	if err != nil {
		return CleanupOutcome{}, err
	}
	if normalized.ExactRepository != "" {
		selected := listed.Results[:0]
		for _, result := range listed.Results {
			if result.Repository == normalized.ExactRepository {
				selected = append(selected, result)
			}
		}
		listed.Results = selected
	}
	for index := range listed.Results {
		if err := applyMergeReceiptCleanupProof(ctx, normalized.MergeReceiptProofs, &listed.Results[index]); err != nil {
			return CleanupOutcome{}, err
		}
		if err := applySupersessionReceipt(ctx, normalized.SupersededBy, &listed.Results[index]); err != nil {
			return CleanupOutcome{}, err
		}
	}
	if recovery != nil {
		for index := range listed.Results {
			if listed.Results[index].Task == recovery.Task &&
				logicalCleanupTaskKey(listed.Results[index], resolution.Write.Home) == cleanupTaskKey(recovery.WorktreesRoot, recovery.Task) {
				listed.Results[index].Locked = false
			}
		}
	}
	recognizedWorktreesRoots := make([]string, 0, len(resolution.Read))
	for _, layout := range resolution.Read {
		recognizedWorktreesRoots = append(recognizedWorktreesRoots, layout.WorktreesRoot)
	}
	backlog, backlogQuarantine, err := loadResumableLifecycleBacklog(ctx, resolution.Write.Home, normalized.ProjectsRoot, recognizedWorktreesRoots, taskSelectionSet(normalized.Tasks), normalized.Filter, "removed")
	if err != nil {
		return CleanupOutcome{}, err
	}
	if normalized.ExactRepository != "" {
		selected := backlog[:0]
		for _, record := range backlog {
			if record.Repository == normalized.ExactRepository {
				selected = append(selected, record)
			}
		}
		backlog = selected
	}
	if normalized.ExactRepository != "" && len(listed.Results) == 0 && len(backlog) == 0 {
		return CleanupOutcome{}, fmt.Errorf("WB worktree task %q has no repository %q", normalized.Task, normalized.ExactRepository)
	}
	// A task directory with no repositories under it yields no candidate and no
	// diagnostic, so it is invisible to inventory. Discover it here, before any
	// apply, so a dry run states it and an apply acts only on what was planned.
	namespaces, err := emptyTaskNamespaces(resolution.Read, taskSelectionSet(normalized.Tasks), normalized.Filter, resolution.Write.Home)
	if err != nil {
		return CleanupOutcome{}, err
	}
	listed.Artifacts = append(listed.Artifacts, namespaces...)
	// A malformed candidate never aborts the run — see blockDiagnosedTasks. It
	// is legitimate history (for example a renamed canonical repository, see
	// RepositoryRenameMismatchError), not evidence that anyone's work is at
	// risk, and one unreadable entry anywhere in the fleet must not deadlock
	// cleanup everywhere else. --filter (and the exact-match task argument
	// above) already scoped listed.Diagnostics to the current selection, so
	// every diagnostic here is one the caller asked to see.
	for _, task := range normalized.Tasks {
		if !cleanupTaskWasFound(task, listed, backlog) {
			return CleanupOutcome{}, fmt.Errorf("WB worktree task %q was not found", task)
		}
	}

	results := make([]CleanupResult, len(listed.Results))
	for index, entry := range listed.Results {
		eligible, reason := cleanupEligibility(entry, normalized, now)
		if eligible {
			if err := preflightWorkLogClaimReadOnly(resolution.Write.Home, entry.WorktreeDir, entry.HeadSHA); err != nil {
				eligible = false
				reason = fmt.Sprintf("preflight Work Log for %s: %v", entry.Repository, err)
			}
		}
		results[index] = CleanupResult{ListResult: entry, Eligible: eligible, Reason: reason}
	}
	// Residue reads back as a candidate that is no longer a Git worktree root,
	// which is exactly the malformed-candidate shape blockDiagnosedTasks blocks
	// a whole task on. A backlog record naming that exact path is the record of
	// WB's own interrupted removal of it, and blocking the task on it would
	// block the one run able to finish it.
	backlogPaths := make(map[string]bool, len(backlog))
	for _, record := range backlog {
		backlogPaths[filepath.Clean(record.WorktreeDir)] = true
		results = append(results, CleanupResult{ListResult: ListResult{
			Task: record.Task, Repository: record.Repository, CanonicalDir: record.CanonicalDir,
			WorktreeDir: record.WorktreeDir, WorktreesRoot: record.WorktreesRoot,
			Branch: record.Branch, Base: record.Base, HeadSHA: record.HeadSHA,
			External: record.External,
		}, Eligible: true, WorktreeGone: true, BacklogID: record.ID,
			Reason: "durable cleanup backlog awaiting exact local branch retirement"})
	}
	blockDiagnosedTasks(results, listed.Diagnostics, backlogPaths)
	scopeLifecycleArtifacts(listed.Artifacts, normalized.ExactRepository, normalized.Filter)
	blockArtifactTasks(results, listed.Artifacts)
	blockUnsafeTasks(results)
	blockEffortsWithLiveDescendants(results, recognizedWorktreesRoots)
	outcome := CleanupOutcome{Results: results, Diagnostics: listed.Diagnostics, Artifacts: listed.Artifacts,
		Purged: listed.Purged, Quarantined: backlogQuarantine, Recovery: recovery,
		ResolvedTasks: append([]string(nil), normalized.Tasks...)}
	// A cleanup plan is read-only even when a caller supplies ReportDir. Audit
	// artifacts are created only for an apply attempt, after the platform
	// capability preflight has succeeded.
	if normalized.Apply && normalized.ReportDir != "" {
		outcome.ReportPath, err = writeCleanupReport(normalized, now, "planned", outcome.Results, outcome.Diagnostics, outcome.Artifacts, outcome.Recovery)
		if err != nil {
			return outcome, err
		}
	}
	if !normalized.Apply {
		return outcome, nil
	}

	fail := func(cleanupErr error) (CleanupOutcome, error) {
		if normalized.ReportDir != "" {
			if _, reportErr := writeCleanupReport(normalized, now, "failed", outcome.Results, outcome.Diagnostics, outcome.Artifacts, outcome.Recovery); reportErr != nil {
				return outcome, fmt.Errorf("%w; write failed cleanup report: %v", cleanupErr, reportErr)
			}
		}
		return outcome, cleanupErr
	}
	// A named cleanup is the operator's exact subject. Preserve its established
	// failure contract when the read-only Work Log gate has already found the
	// same mismatch that apply's locked preflight would reject. Fleet cleanup
	// instead reports the ineligible task and keeps processing healthy tasks.
	if len(normalized.Tasks) == 1 {
		for _, result := range outcome.Results {
			if strings.HasPrefix(result.Reason, "preflight Work Log for ") {
				return fail(errors.New(result.Reason))
			}
		}
	}
	for backlogIndex := range backlog {
		// A record whose worktree path is still present is one Git unregistered
		// and could not finish deleting. Note that before resuming, because a
		// successful resume is what removes it.
		_, residueErr := os.Lstat(backlog[backlogIndex].WorktreeDir)
		residuePresent := residueErr == nil
		if err := resumeLifecycleBacklog(ctx, resolution.Write.Home, &backlog[backlogIndex], normalized.DeleteRemote); err != nil {
			return fail(err)
		}
		for resultIndex := range outcome.Results {
			if outcome.Results[resultIndex].BacklogID == backlog[backlogIndex].ID {
				outcome.Results[resultIndex].Applied = true
				outcome.Results[resultIndex].BranchDeleted = true
				outcome.Results[resultIndex].WorktreeResidueRemoved = residuePresent
				// resumeLifecycleBacklog never deletes a remote branch itself: it
				// refuses to proceed unless a fresh `git ls-remote` already shows
				// origin/<branch> gone (see its remoteBranchHead check). A record
				// with a non-empty RemoteHeadSHA means a remote branch existed at
				// seal time — the interrupted attempt that sealed it, not this
				// resume, is what deleted it, most likely moments before the crash
				// that left this backlog behind. That successful resume is itself
				// the proof the remote branch is gone now, so the report must
				// credit the deletion instead of defaulting to false and silently
				// under-claiming what WB actually did.
				outcome.Results[resultIndex].RemoteDeleted = backlog[backlogIndex].RemoteHeadSHA != ""
				outcome.Results[resultIndex].Reason = "resumed durable cleanup backlog"
			}
		}
	}
	// Hold the same per-task lock used by worktree creation across that task's
	// complete recheck-and-remove sequence, and close all of its retained
	// descriptors when it finishes: an --all-merged run must not retain every
	// task's lock and file descriptors for its entire duration. Tasks overlap
	// only where Git permits it — see runCleanupApply and
	// acquireRepositoryWriteLocks.
	if normalized.beforeCleanupLocks != nil {
		normalized.beforeCleanupLocks()
	}
	// Resolve every task's plan before the first one starts. See
	// cleanupApplyEntry: a task that re-scanned the whole outcome to find its
	// own rows would, under concurrency, be reading the fields its neighbours
	// are writing.
	entries := planCleanupApply(outcome, resolution.Write.Home)
	repositoryLocks := newCloneLocks()
	remoteGate := newRemoteBranchDeletionGate(normalized.Workers)
	applyTask := func(entry cleanupApplyEntry) error {
		selection := entry.selection
		// A recovered lock may only become a normal cleanup transaction after the
		// named task itself has an eligible, present worktree. Lifecycle artifacts
		// alone are not authority to consume an interrupted task lock: they can be
		// left behind by an ineligible, filtered, or otherwise skipped task.
		if recoveredTask != nil && recovery != nil && selection.WorktreesRoot == recovery.WorktreesRoot && selection.Task == recovery.Task && !entry.hasEligibleWorktree {
			return nil
		}
		if !entry.canApply {
			return nil
		}
		var task *cleanupTaskHandle
		recoveredTransaction := false
		if recoveredTask != nil && recovery != nil && selection.WorktreesRoot == recovery.WorktreesRoot && selection.Task == recovery.Task {
			task = recoveredTask
			recoveredTask = nil // ownership transfers to this cleanup transaction.
			if err := task.validateHeldLock(); err != nil {
				task.preserveLock()
				task.close()
				return err
			}
			recoveredTransaction = true
		} else {
			acquired, acquireErr := acquireCleanupTaskAtOrCreate(selection.WorktreesRoot, selection.Task)
			if acquireErr != nil {
				return acquireErr
			}
			task = acquired
		}
		defer func() {
			retireNamespace := true
			if selection.WorktreesRoot == filepath.Join(resolution.Write.Home, "worktrees") {
				// A filtered cleanup may leave physical members in other canonical
				// repositories. Check the whole task while its lock is still held;
				// an empty coordination directory alone does not prove terminality.
				inventory, inventoryErr := ListWithDiagnostics(ctx, ListOptions{
					ProjectsRoot: normalized.ProjectsRoot, Task: selection.Task, Workers: 1,
				})
				retireNamespace = inventoryErr == nil && len(inventory.Results) == 0 && len(inventory.Diagnostics) == 0
			}
			if recoveredTransaction && (recovery == nil || !recovery.Applied) {
				task.preserveLock()
			} else if releaseErr := task.lock.release(); releaseErr == nil {
				purgeTerminalTaskLockDebris(task)
				if retireNamespace {
					removeEmptyTaskDirectory(task)
				}
			}
			task.close()
		}()
		// Corroborate every repository, exact remote target SHA, and private
		// Work Log before the first terminal write or Git deletion. The task
		// lock prevents another WB lifecycle operation from racing this phase;
		// every destructive step still repeats its local/network recheck below.
		for _, index := range entry.resultIndices {
			refreshed, preflightErr := preflightCleanupRepository(ctx, normalized, now, task, outcome.Results[index], resolution.Write.Home)
			if preflightErr != nil {
				return preflightErr
			}
			outcome.Results[index].ListResult = refreshed
		}
		artifactArchive, artifactArchivePath, artifactHandles, artifactErr := prepareCleanupLifecycleArtifacts(
			resolution.Write.Home, task, entry.artifactIndices, outcome.Artifacts,
		)
		if artifactErr != nil {
			return artifactErr
		}
		if artifactArchive != nil {
			defer func() { _ = artifactArchive.Close() }()
		}
		defer closeCleanupLifecycleArtifacts(artifactHandles)
		for _, index := range entry.resultIndices {
			worktree, err := openCleanupWorktree(task, outcome.Results[index])
			if err != nil {
				return err
			}
			if err := worktree.validate(); err != nil {
				worktree.close()
				return err
			}
			refreshed, err := inspectLifecycleWorktree(
				ctx,
				normalized.ProjectsRoot,
				resolution.Write.Home,
				wbhome.Layout{WorktreesRoot: outcome.Results[index].WorktreesRoot, Local: outcome.Results[index].Local},
				outcome.Results[index].Task,
				outcome.Results[index].WorktreeDir,
				normalized.Base,
				normalized.AbsorbedBy,
				true,
				false, // The task is locked by this cleanup operation.
				outcome.Results[index].External,
				cleanupInspectPolicy(normalized),
			)
			if err != nil {
				worktree.close()
				return err
			}
			if err := applyMergeReceiptCleanupProof(ctx, normalized.MergeReceiptProofs, &refreshed); err != nil {
				worktree.close()
				return fmt.Errorf("cleanup receipt proof for %s: %w", refreshed.Repository, err)
			}
			if err := applySupersessionReceipt(ctx, normalized.SupersededBy, &refreshed); err != nil {
				worktree.close()
				return fmt.Errorf("supersession receipt for %s: %w", refreshed.Repository, err)
			}
			if err := worktree.validate(); err != nil {
				worktree.close()
				return err
			}
			eligible, reason := cleanupEligibility(refreshed, normalized, now)
			if !eligible {
				worktree.close()
				return fmt.Errorf("cleanup safety changed for %s: %s", refreshed.Repository, reason)
			}
			if refreshed.HeadSHA != outcome.Results[index].HeadSHA {
				worktree.close()
				return fmt.Errorf("cleanup safety changed for %s: branch head moved", refreshed.Repository)
			}
			outcome.Results[index].ListResult = refreshed
			canonical, err := openCanonicalRepository(refreshed.CanonicalDir)
			if err != nil {
				worktree.close()
				return fmt.Errorf("open cleanup canonical repository %s: %w", refreshed.CanonicalDir, err)
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
				return fmt.Errorf("cleanup canonical repository changed before Git operations: %w", err)
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
				return err
			}
			// Archive the recoverable run record while every Git asset still
			// exists. Remote branch deletion is destructive too, so it must never
			// precede the durable terminal/outbox record.
			var sealErr error
			if refreshed.SupersededAtOrigin && refreshed.supersessionReceipt != nil {
				sealErr = sealWorkLogForSupersession(resolution.Write.Home, refreshed.WorktreeDir, refreshed.HeadSHA, refreshed.supersessionReceipt)
			} else {
				sealErr = sealWorkLogForCleanup(resolution.Write.Home, refreshed.WorktreeDir, refreshed.HeadSHA)
			}
			if sealErr != nil {
				closeCanonical()
				worktree.close()
				return fmt.Errorf("seal work log before removing %s: %w", refreshed.WorktreeDir, sealErr)
			}
			backlogRecord := newLifecycleBacklogRecord(normalized.ProjectsRoot, refreshed, "removed")
			if err := persistLifecycleBacklog(resolution.Write.Home, &backlogRecord, lifecycleStageSealed); err != nil {
				closeCanonical()
				worktree.close()
				return err
			}
			outcome.Results[index].BacklogID = backlogRecord.ID
			if normalized.DeleteRemote && refreshed.RemoteHeadSHA != "" {
				if err := persistLifecycleBacklog(resolution.Write.Home, &backlogRecord, lifecycleStageRetiringRemote); err != nil {
					closeCanonical()
					worktree.close()
					return err
				}
				// Every authorization and the network call itself sit inside one
				// gate slot, so the bound counts branch deletions actually in
				// flight against origin rather than tasks that intend one. The
				// slot is taken after this task's repository locks and released
				// before them; nothing holding a slot waits for a lock, so the
				// two resources cannot form a cycle.
				deleteErr := func() error {
					releaseRemoteSlot := remoteGate.enter()
					defer releaseRemoteSlot()
					if err := worktree.validate(); err != nil {
						return err
					}
					if normalized.beforeCleanupNetworkBranchOperation != nil {
						normalized.beforeCleanupNetworkBranchOperation(refreshed.WorktreeDir)
					}
					if err := worktree.validate(); err != nil {
						return err
					}
					if normalized.afterCleanupGitAuthorization != nil {
						normalized.afterCleanupGitAuthorization("delete remote branch")
					}
					if err := validateRecoveredCleanupLock(recoveredTransaction, task); err != nil {
						return err
					}
					if err := runSecureCleanupGitHelper(ctx, canonical, worktree.parent, worktree.worktree, worktree.parentPath, refreshed.WorktreeDir, "push", "--force-with-lease=refs/heads/"+refreshed.Branch+":"+refreshed.HeadSHA, "origin", ":refs/heads/"+refreshed.Branch); err != nil {
						return fmt.Errorf("delete remote branch %s at %s: %w", refreshed.Branch, refreshed.HeadSHA, err)
					}
					return nil
				}()
				if deleteErr != nil {
					closeCanonical()
					worktree.close()
					return deleteErr
				}
				outcome.Results[index].RemoteDeleted = true
				if err := persistLifecycleBacklog(resolution.Write.Home, &backlogRecord, lifecycleStageRemoteRetired); err != nil {
					closeCanonical()
					worktree.close()
					return err
				}
			}
			if err := worktree.validate(); err != nil {
				closeCanonical()
				worktree.close()
				return err
			}
			if normalized.afterCleanupGitAuthorization != nil {
				normalized.afterCleanupGitAuthorization("remove worktree")
			}
			if err := validateRecoveredCleanupLock(recoveredTransaction, task); err != nil {
				closeCanonical()
				worktree.close()
				return err
			}
			if err := persistLifecycleBacklog(resolution.Write.Home, &backlogRecord, lifecycleStageRemovingWorktree); err != nil {
				closeCanonical()
				worktree.close()
				return err
			}
			if removeErr := runSecureCleanupGitHelper(ctx, canonical, worktree.parent, worktree.worktree, worktree.parentPath, refreshed.WorktreeDir, "worktree", "remove", refreshed.WorktreeDir); removeErr != nil {
				// Git deletes the working tree first and the registration
				// second, and it deletes the registration even when the
				// tree delete failed partway. Ask which of the two failures
				// this was before deciding whether the task is finishable.
				residue, residueErr := worktreeRemovalLeftResidue(ctx, canonical, refreshed.WorktreeDir)
				if residueErr != nil {
					closeCanonical()
					worktree.close()
					return fmt.Errorf("remove worktree %s: %w; inspect its registration afterwards: %v", refreshed.WorktreeDir, removeErr, residueErr)
				}
				if !residue {
					closeCanonical()
					worktree.close()
					return fmt.Errorf("remove worktree %s: %w", refreshed.WorktreeDir, removeErr)
				}
				if normalized.beforeCleanupResidueRemoval != nil {
					if err := normalized.beforeCleanupResidueRemoval(refreshed.WorktreeDir); err != nil {
						closeCanonical()
						worktree.close()
						return err
					}
				}
				removed, repairErr := removeUnregisteredWorktreeResidue(worktree, refreshed.WorktreeDir)
				if repairErr != nil {
					closeCanonical()
					worktree.close()
					return fmt.Errorf("remove worktree %s: %w; %v", refreshed.WorktreeDir, removeErr, repairErr)
				}
				outcome.Results[index].WorktreeResidueRemoved = removed
			}
			outcome.Results[index].WorktreeGone = true
			if err := persistLifecycleBacklog(resolution.Write.Home, &backlogRecord, lifecycleStageWorktreeRemoved); err != nil {
				closeCanonical()
				worktree.close()
				return err
			}
			if normalized.afterCleanupWorktreeRemoval != nil {
				if err := normalized.afterCleanupWorktreeRemoval(refreshed.WorktreeDir); err != nil {
					closeCanonical()
					worktree.close()
					return fmt.Errorf("after worktree removal for %s: %w", refreshed.Repository, err)
				}
			}
			if err := task.validate(); err != nil {
				closeCanonical()
				worktree.close()
				return err
			}
			if normalized.afterCleanupGitAuthorization != nil {
				normalized.afterCleanupGitAuthorization("delete local branch")
			}
			if err := validateRecoveredCleanupLock(recoveredTransaction, task); err != nil {
				closeCanonical()
				worktree.close()
				return err
			}
			if err := persistLifecycleBacklog(resolution.Write.Home, &backlogRecord, lifecycleStageRemovingLocalBranch); err != nil {
				closeCanonical()
				worktree.close()
				return err
			}
			// A detached checkout has no branch ref of its own: a review
			// checkout points straight at a commit. Deleting the checkout is
			// the whole of its retirement, and asking Git to delete
			// refs/heads/ with an empty name would be a request to remove
			// something that was never created.
			if refreshed.Branch != "" {
				if err := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "update-ref", "-d", "refs/heads/"+refreshed.Branch, refreshed.HeadSHA); err != nil {
					closeCanonical()
					worktree.close()
					return fmt.Errorf("delete local branch %s at %s: %w", refreshed.Branch, refreshed.HeadSHA, err)
				}
				outcome.Results[index].BranchDeleted = true
			}
			if err := worktree.removeEmptyParent(normalized.afterCleanupParentAuthorization); err != nil {
				closeCanonical()
				worktree.close()
				return err
			}
			if refreshed.External {
				owner, repository, splitErr := splitRepository(refreshed.Repository)
				if splitErr != nil {
					closeCanonical()
					worktree.close()
					return fmt.Errorf("resolve adopted worktree registration identity for %s: %w", refreshed.Repository, splitErr)
				}
				if err := removeAdoptedRegistration(task, owner, repository); err != nil {
					closeCanonical()
					worktree.close()
					return err
				}
			}
			if err := persistLifecycleBacklog(resolution.Write.Home, &backlogRecord, lifecycleStageComplete); err != nil {
				closeCanonical()
				worktree.close()
				return err
			}
			worktree.close()
			closeCanonical()
			outcome.Results[index].Applied = true
		}
		if err := archiveCleanupLifecycleArtifacts(task, artifactArchive, artifactArchivePath, artifactHandles, outcome.Artifacts); err != nil {
			return err
		}
		if recoveredTransaction {
			if normalized.beforeRecoveredLockQuarantine != nil {
				task.lock.beforeRelease = func() { normalized.beforeRecoveredLockQuarantine(recovery.Path) }
			}
			if err := task.lock.release(); err != nil {
				return fmt.Errorf("quarantine recovered cleanup lock: %w", err)
			}
			task.lock = operationLock{}
			purgeTerminalTaskLockDebris(task)
			recovery.Disposition = "quarantined"
			recovery.Applied = true
		}
		return nil
	}
	// Interrupted-lock recovery and one named task stay on the serial path with
	// the recovered handle they were written for. An explicit named batch and a
	// fleet sweep fan out through the same scheduler.
	taskErrs := runCleanupApply(entries, normalized.Workers, repositoryLocks, len(normalized.Tasks) == 1 && !normalized.AllMerged, applyTask)
	// Fold the per-task outcomes back in walk order, never completion order, so
	// the report reads identically however the workers happened to interleave.
	for index := range entries {
		cleanupErr := taskErrs[index]
		if cleanupErr == nil {
			continue
		}
		selection := entries[index].selection
		// One named task is the operator's exact subject, so its failure is the
		// answer to what they asked and still ends the command. A named batch is
		// intentionally fleet-like here: one failure is scoped to that task.
		if len(normalized.Tasks) == 1 && !normalized.AllMerged {
			return fail(cleanupErr)
		}
		// A fleet sweep is a different question. One task that cannot be
		// corroborated — a branch that no longer matches its Work Log claim, a
		// head that moved, a sibling gone unclean — says nothing about the
		// other tasks already proven eligible, and discarding them costs a
		// `git fetch` each to rebuild. Scope the failure to its own task,
		// exactly as CleanupOutcome documents for a malformed candidate, and
		// let the rest of the run finish.
		// fail writes the audit report for this failure; its error is the one
		// the operator must read, wrapped when that write itself failed.
		_, cleanupErr = fail(cleanupErr)
		outcome.Diagnostics = append(outcome.Diagnostics, listDiagnostic(
			selection.WorktreesRoot,
			selection.Task,
			filepath.Join(selection.WorktreesRoot, selection.Task),
			cleanupErr.Error(),
		))
		for _, resultIndex := range entries[index].resultIndices {
			if outcome.Results[resultIndex].Applied {
				continue
			}
			outcome.Results[resultIndex].Reason = cleanupErr.Error()
		}
	}
	// Retire the namespaces earlier releases left behind, under the same
	// per-task lock. Removing an empty task root after releasing its lock used
	// to open an ABA window where a concurrent create could build an
	// unreachable task directory at the same pathname; every operation now
	// refuses a directory that was unlinked while it was starting (see
	// acquireLockAtReclaimingInterrupted), which turns that race into a
	// retryable error and lets the namespace be retired instead of accumulating
	// one empty shell per finished task.
	retireEmptyTaskNamespaces(outcome.Artifacts)
	if normalized.ReportDir != "" {
		phase := "applied"
		if outcome.Recovery != nil && !outcome.Recovery.Applied {
			phase = "validated"
		}
		outcome.ReportPath, err = writeCleanupReport(normalized, now, phase, outcome.Results, outcome.Diagnostics, outcome.Artifacts, outcome.Recovery)
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

func logicalCleanupTaskKey(result ListResult, home string) string {
	root := result.WorktreesRoot
	if result.Local {
		root = filepath.Join(home, "worktrees")
	}
	return cleanupTaskKey(root, result.Task)
}

func cleanupTaskSelections(outcome CleanupOutcome, home string) []cleanupTaskSelection {
	byKey := make(map[string]cleanupTaskSelection)
	for _, result := range outcome.Results {
		if result.BacklogID != "" {
			continue
		}
		key := logicalCleanupTaskKey(result.ListResult, home)
		root := result.WorktreesRoot
		if result.Local {
			root = filepath.Join(home, "worktrees")
		}
		byKey[key] = cleanupTaskSelection{WorktreesRoot: root, Task: result.Task}
	}
	for _, artifact := range outcome.Artifacts {
		// An unscoped local stage is intentionally fail-closed inventory, not a
		// task transaction. It has no lock namespace to acquire and must never
		// synthesize an empty-task cleanup apply entry.
		if artifact.Task == "" {
			continue
		}
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

func cleanupTaskCanApply(outcome CleanupOutcome, taskKey, home string) bool {
	hasPending := false
	for _, result := range outcome.Results {
		if result.BacklogID != "" || logicalCleanupTaskKey(result.ListResult, home) != taskKey {
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

// cleanupTaskHasEligibleWorktree distinguishes a normal cleanup transaction
// from artifact-only work. Interrupted-lock recovery is deliberately narrower:
// it must preserve the exact recovered lock unless that named task's present
// worktree has passed the ordinary eligibility gates.
func cleanupTaskHasEligibleWorktree(outcome CleanupOutcome, taskKey, home string) bool {
	for _, result := range outcome.Results {
		if result.BacklogID == "" && !result.WorktreeGone && logicalCleanupTaskKey(result.ListResult, home) == taskKey && result.Eligible {
			return true
		}
	}
	return false
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
	tasks, err := normalizeTaskSelection(options.Task, options.Tasks)
	if err != nil {
		return CleanupOptions{}, err
	}
	projectsRoot, _, base, filter, err := normalizeListOptions(ListOptions{
		ProjectsRoot: options.ProjectsRoot,
		Base:         options.Base,
		Filter:       options.Filter,
	})
	if err != nil {
		return CleanupOptions{}, err
	}
	options.ProjectsRoot = projectsRoot
	options.Tasks = tasks
	options.Task = ""
	if len(tasks) == 1 {
		options.Task = tasks[0]
	}
	options.Base = base
	options.Filter = filter
	options.ExactRepository = strings.TrimSpace(options.ExactRepository)
	if options.ExactRepository != "" {
		if _, _, err := splitRepository(options.ExactRepository); err != nil {
			return CleanupOptions{}, err
		}
		if options.AllMerged || len(options.Tasks) != 1 {
			return CleanupOptions{}, fmt.Errorf("exact repository cleanup requires one explicit task")
		}
		if options.Filter != "" && options.Filter != options.ExactRepository {
			return CleanupOptions{}, fmt.Errorf("exact repository cleanup cannot be combined with a different substring filter")
		}
		// Narrow the filesystem walk before the exact post-inspection gate.
		options.Filter = options.ExactRepository
	}
	options.AbsorbedBy = strings.TrimSpace(options.AbsorbedBy)
	options.SupersededBy = strings.TrimSpace(options.SupersededBy)
	if options.SupersededBy != "" {
		if options.AllMerged || len(options.Tasks) != 1 {
			return CleanupOptions{}, fmt.Errorf("supersession cleanup requires one explicit task")
		}
		options.SupersededBy, err = filepath.Abs(options.SupersededBy)
		if err != nil {
			return CleanupOptions{}, fmt.Errorf("resolve supersession receipt: %w", err)
		}
	}
	if len(options.Tasks) == 0 && !options.AllMerged {
		return CleanupOptions{}, fmt.Errorf("supply one or more tasks or use --all-merged")
	}
	if len(options.Tasks) != 0 && options.AllMerged {
		return CleanupOptions{}, fmt.Errorf("tasks and --all-merged cannot be combined")
	}
	if options.ResumeInterrupted && len(options.Tasks) != 1 {
		return CleanupOptions{}, fmt.Errorf("resume interrupted cleanup requires one explicit task")
	}
	if options.OlderThan < 0 {
		return CleanupOptions{}, fmt.Errorf("--older-than cannot be negative")
	}
	// One ceiling for both phases, defaulted once here rather than differently
	// in each. ListWithDiagnostics applies the same default to the inventory
	// walk; leaving apply to fall back to 1 would make an unset Workers mean
	// two different things in one command.
	if options.Workers < 1 {
		options.Workers = DefaultInspectWorkers
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

func normalizeTaskSelection(task string, tasks []string) ([]string, error) {
	if strings.TrimSpace(task) != "" && len(tasks) != 0 {
		return nil, fmt.Errorf("task and tasks cannot be combined")
	}
	if len(tasks) == 0 && strings.TrimSpace(task) != "" {
		tasks = []string{task}
	}
	seen := make(map[string]bool, len(tasks))
	selected := make([]string, 0, len(tasks))
	for _, candidate := range tasks {
		candidate = strings.TrimSpace(candidate)
		if !validSafeSegment(candidate) {
			return nil, fmt.Errorf("task %q must be one safe path segment", candidate)
		}
		if !seen[candidate] {
			seen[candidate] = true
			selected = append(selected, candidate)
		}
	}
	sort.Strings(selected)
	return selected, nil
}

func taskSelectionSet(tasks []string) map[string]bool {
	if len(tasks) == 0 {
		return nil
	}
	selected := make(map[string]bool, len(tasks))
	for _, task := range tasks {
		selected[task] = true
	}
	return selected
}

func taskSelectionMatches(tasks map[string]bool, task string) bool {
	return len(tasks) == 0 || tasks[task]
}

// resolveLogicalCleanupTasks expands an explicit logical effort to the
// physical task namespaces that contain manifests for it. Normal WB tasks
// already use the effort as their directory name, so they need no special
// handling. A remote parked-session target is the one supported exception:
// its namespace is session-resume-<resume>-<member> while the target manifest
// names the source effort. A single resume can have several member namespaces,
// so all exact manifest matches are returned for one audited cleanup pass.
func resolveLogicalCleanupTasks(layouts []wbhome.Layout, tasks []string) ([]string, error) {
	if len(tasks) == 0 {
		return tasks, nil
	}
	resolved := make([]string, 0, len(tasks))
	seen := make(map[string]bool, len(tasks))
	for _, logical := range tasks {
		matches := make([]string, 0)
		for _, layout := range layouts {
			entries, err := os.ReadDir(layout.WorktreesRoot)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("read worktree tasks under %s: %w", layout.WorktreesRoot, err)
			}
			for _, taskEntry := range entries {
				if !taskEntry.IsDir() || strings.HasPrefix(taskEntry.Name(), ".") || !validSafeSegment(taskEntry.Name()) {
					continue
				}
				if taskEntry.Name() == logical {
					matches = append(matches, taskEntry.Name())
					continue
				}
				// Only session-resume namespaces are eligible for this alias. This
				// prevents a stale or hand-written manifest from unexpectedly
				// changing the meaning of an ordinary task selector.
				if !strings.HasPrefix(taskEntry.Name(), "session-resume-") {
					continue
				}
				taskRoot := filepath.Join(layout.WorktreesRoot, taskEntry.Name())
				if layout.Local {
					manifest, manifestErr := ReadManifest(taskRoot)
					if manifestErr == nil && manifest.EffortID == logical {
						matches = append(matches, taskEntry.Name())
					}
					continue
				}
				owners, readErr := os.ReadDir(taskRoot)
				if readErr != nil {
					if errors.Is(readErr, os.ErrNotExist) {
						continue
					}
					return nil, fmt.Errorf("read session-resume task %s: %w", taskEntry.Name(), readErr)
				}
				aliasFound := false
				for _, owner := range owners {
					if !owner.IsDir() || strings.HasPrefix(owner.Name(), ".") {
						continue
					}
					repositories, readErr := os.ReadDir(filepath.Join(taskRoot, owner.Name()))
					if readErr != nil {
						if errors.Is(readErr, os.ErrNotExist) {
							continue
						}
						return nil, fmt.Errorf("read session-resume task %s owner: %w", taskEntry.Name(), readErr)
					}
					for _, repository := range repositories {
						if !repository.IsDir() || strings.HasPrefix(repository.Name(), ".") {
							continue
						}
						manifest, manifestErr := ReadManifest(filepath.Join(taskRoot, owner.Name(), repository.Name()))
						if manifestErr == nil && manifest.EffortID == logical {
							aliasFound = true
							break
						}
					}
					if aliasFound {
						break
					}
				}
				if aliasFound {
					matches = append(matches, taskEntry.Name())
				}
			}
		}
		sort.Strings(matches)
		if len(matches) == 0 {
			matches = []string{logical}
		}
		for _, match := range matches {
			if !seen[match] {
				seen[match] = true
				resolved = append(resolved, match)
			}
		}
	}
	sort.Strings(resolved)
	return resolved, nil
}

func cleanupTaskWasFound(task string, listed ListOutcome, backlog []lifecycleBacklogRecord) bool {
	for _, result := range listed.Results {
		if result.Task == task {
			return true
		}
	}
	for _, diagnostic := range listed.Diagnostics {
		if diagnostic.Task == task {
			return true
		}
	}
	for _, artifact := range listed.Artifacts {
		if artifact.Task == task {
			return true
		}
	}
	for _, record := range backlog {
		if record.Task == task {
			return true
		}
	}
	return false
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

// resolveRecordedWorktreeBase binds lifecycle evidence to the target that was
// selected when the checkout was created. Cleanup used to pass one global
// --base through the whole inventory, which made a task containing both a
// main-based checkout and a checkout stacked on a feature branch report the
// latter as "not merged" even after its feature target received it.
//
// The manifest is the creation record and the private Work Log claim is the
// fallback for legacy worktrees that have no manifest. The caller's base is
// only a compatibility fallback for candidates that predate both records, and
// ultimately normalizes to main.
func resolveRecordedWorktreeBase(ctx context.Context, home, worktree, fallback string) (string, error) {
	fallback = strings.TrimSpace(fallback)
	if fallback == "" {
		fallback = "main"
	}
	if !validBranch(ctx, fallback) {
		return "", fmt.Errorf("invalid fallback target branch %q", fallback)
	}

	manifest, manifestErr := ReadManifest(worktree)
	if manifestErr != nil && !errors.Is(manifestErr, errManifestNotFound) {
		return "", fmt.Errorf("read worktree target record for %s: %w", worktree, manifestErr)
	}
	manifestBase := ""
	if manifestErr == nil {
		manifestBase = strings.TrimSpace(manifest.Base)
		if manifestBase == "" || !validBranch(ctx, manifestBase) {
			return "", fmt.Errorf("worktree target record for %s has invalid base %q", worktree, manifest.Base)
		}
	}
	// A parked-session target is checked out from the source feature branch,
	// but that branch is commonly deleted as soon as its work lands. The
	// target's immutable BaseSHA still pins the exact admitted commit; for
	// lifecycle containment use the operator's validated target fallback. This
	// compatibility branch is intentionally limited to the parked-session claim
	// and does not override ordinary manifests or an explicit --base choice.
	if manifestBase != "" && strings.TrimSpace(home) != "" {
		if claim, _, _, claimErr := activeWorkLogClaim(home, worktree); claimErr == nil && claim.AcquiredVia == "parked_session_resume" {
			return fallback, nil
		}
	}

	claimBase := ""
	// A manifest is the immutable checkout-local creation record. Do not
	// corroborate the active claim before lifecycle inspection has had a chance
	// to repair a branch-name mismatch (for example `wb worktree log recover
	// --reconcile-branch`). Cleanup's own Work Log preflight still performs the
	// full corroboration before any destructive operation. Claims are consulted
	// here only when a legacy checkout has no manifest.
	if manifestBase == "" && strings.TrimSpace(home) != "" {
		claim, _, _, claimErr := activeWorkLogClaim(home, worktree)
		switch {
		case claimErr == nil:
			claimBase = strings.TrimSpace(claim.Base)
			if claimBase == "" || !validBranch(ctx, claimBase) {
				return "", fmt.Errorf("work log target record for %s has invalid base %q", worktree, claim.Base)
			}
		case errors.Is(claimErr, errWorkLogProjectionNotFound):
			// Legacy or internally-created worktrees may have a manifest but no
			// Work Log projection. The manifest remains authoritative in that
			// case; a missing claim is not itself a target mismatch.
		default:
			return "", fmt.Errorf("read Work Log target record for %s: %w", worktree, claimErr)
		}
	}
	if claimBase != "" {
		return claimBase, nil
	}
	if manifestBase != "" {
		return manifestBase, nil
	}
	return fallback, nil
}

// inspectLifecycleWorktree validates one linked checkout and, when GitHub is
// requested, establishes whether its exact head is integrated into the freshly
// fetched exact origin target. absorbedBy is the optional operator-supplied
// pointer to a landing commit or merged pull request for work that reached the
// target inside a differently named integration branch; it only says where to
// look for a receipt and never substitutes for one (see absorbedLandingReceipt).
// inspectPolicy carries the reporting-only widenings of an inventory read. Its
// zero value is exactly the behaviour every mutation path had before them, so a
// caller that removes worktrees or branches keeps passing it and can never
// silently inherit a widening it did not ask for.
type inspectPolicy struct {
	// includeDetached keeps a detached checkout as a result instead of an
	// error. Branch is empty for one; every branch-shaped operation must check.
	includeDetached bool
	// ttl marks a checkout older than this expired. Reporting only.
	ttl time.Duration
	// residueEvidence enables the commit-to-pull-request walk over ancestors.
	residueEvidence bool
	// activity asks the inventory to read the four in-use signals. It costs one
	// extra local Git call per candidate, so only the verbs that decide whether
	// a checkout may be removed ask for it.
	activity bool
	// residueDepth bounds that walk.
	residueDepth int
	now          func() time.Time
}

func (policy inspectPolicy) clock() time.Time {
	if policy.now != nil {
		return policy.now()
	}
	return time.Now()
}

func inspectLifecycleWorktree(
	ctx context.Context,
	projectsRoot string,
	home string,
	layout wbhome.Layout,
	task, worktree, base, absorbedBy string,
	withGitHub, locked, external bool,
	policy inspectPolicy,
) (ListResult, error) {
	base, err := resolveRecordedWorktreeBase(ctx, home, worktree, base)
	if err != nil {
		return ListResult{}, err
	}
	root, err := git(ctx, worktree, "rev-parse", "--show-toplevel")
	if err != nil {
		return ListResult{}, fmt.Errorf("inspect %s: %w", worktree, err)
	}
	if filepath.Clean(root) != filepath.Clean(worktree) {
		return ListResult{}, fmt.Errorf("WB worktree %s has Git root %s", worktree, root)
	}
	var location managedWorktreeLocation
	if external {
		// An adopted worktree carries no task/owner/repository segment in its
		// own path — it was never moved under a WB worktrees root — so its
		// task is exactly what the WB-home registration directory it was
		// discovered under is named. Owner/repository identity is still
		// derived and verified independently from the worktree's own Git
		// plumbing, precisely as locateManagedWorktree does for a nested
		// worktree; only the source of "task" differs.
		location, err = locateAdoptedWorktree(ctx, projectsRoot, worktree, layout, task)
	} else {
		location, err = locateManagedWorktree(ctx, projectsRoot, worktree, []wbhome.Layout{layout})
	}
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
	detached := branch == ""
	if detached && !policy.includeDetached {
		return ListResult{}, fmt.Errorf("WB worktree %s is not on a feature branch", worktree)
	}
	if branch == base {
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
	// The immutable manifest may name a feature branch that was used as the
	// checkout's target. Once that branch's PR lands, GitHub/ Renovate can
	// delete the source ref. In GitHub mode the exact target fetch below is the
	// authoritative integration check and can recover the merged PR's target;
	// do not fail early by asking Git for the deleted tracking ref. Offline
	// inspection has no PR evidence to widen the target, so it remains strict.
	locallyMerged := false
	if !withGitHub {
		locallyMerged, err = isAncestor(ctx, canonical, head, "origin/"+base)
		if err != nil {
			return ListResult{}, err
		}
	}
	lastCommitValue, err := git(ctx, worktree, "show", "-s", "--format=%cI", "HEAD")
	if err != nil {
		return ListResult{}, err
	}
	lastCommit, err := time.Parse(time.RFC3339, lastCommitValue)
	if err != nil {
		return ListResult{}, fmt.Errorf("parse last commit time for %s: %w", slug, err)
	}
	// Diagnose the holder here, beside where Locked is set, so every caller
	// of this inspector gets an explainable lock without threading two more
	// parameters through its nine call sites.
	var lockOwner LockOwnerState
	var lockOwnerPID int
	if locked {
		lockOwner, lockOwnerPID = diagnoseTaskLock(filepath.Join(lifecycleTaskLockRoot(home, layout), task), task)
	}
	result := ListResult{
		Task: task, Repository: slug, CanonicalDir: canonical, WorktreeDir: worktree,
		WorktreesRoot: layout.WorktreesRoot,
		Branch:        branch, Base: base, HeadSHA: head,
		Clean: clean, LocallyMerged: locallyMerged, Locked: locked,
		LockOwner: lockOwner, LockOwnerPID: lockOwnerPID, LastCommit: lastCommit,
		External: external, Local: layout.Local, Detached: detached,
	}
	owners, ownerErr := lifecycleOwnerViews(home, worktree)
	if ownerErr != nil {
		return ListResult{}, fmt.Errorf("read owner metadata for %s: %w", worktree, ownerErr)
	}
	result.Owners, result.OwnerState = owners, worktreeOwnerState(owners)
	applyWorktreeAge(&result, worktree, policy)
	if policy.activity {
		result.LastActivityAt = LastActivity(ctx, result)
	}
	// The owner event is an append-only projection and may be absent after an
	// interrupted creation. Prefer the immutable claim's session link when it
	// exists so park can identify the intended member before attempting custody
	// capture; legacy claims simply leave this field empty.
	if home, homeErr := wbhome.Root(projectsRoot); homeErr == nil {
		if claim, _, _, claimErr := activeWorkLogClaim(home, worktree); claimErr == nil {
			result.WorkLogSessionID = strings.TrimSpace(claim.WBSessionID)
		}
	}
	if withGitHub {
		var pullRequests []githubPullRequest
		var known bool
		integrationBase := base
		result.RemoteTargetSHA, err = fetchRemoteTargetHead(ctx, canonical, integrationBase)
		if err != nil {
			// A timeout or other transport failure is not evidence that the
			// recorded target was deleted. Keep it diagnostic and fail closed;
			// only Git's explicit missing-ref response may widen the target from
			// an immutable merged-PR receipt.
			if !isMissingRemoteTargetError(err) {
				return ListResult{}, err
			}
			var pullRequestErr error
			pullRequests, known, pullRequestErr = githubPullRequestsForCommit(ctx, worktree, slug, head)
			if pullRequestErr != nil {
				return ListResult{}, pullRequestErr
			}
			// A deleted recorded target is recoverable only when GitHub's
			// immutable commit index supplies one unambiguous merged PR for this
			// exact head. The PR target is then fetched freshly and all ordinary
			// containment/tree checks below still run against that target.
			var ok bool
			integrationBase, ok = mergedPullRequestTarget(ctx, pullRequests, head, base)
			if !ok {
				return ListResult{}, err
			}
			result.RemoteTargetSHA, err = fetchRemoteTargetHead(ctx, canonical, integrationBase)
			if err != nil {
				return ListResult{}, err
			}
			result.Base = integrationBase
			result.HeadUnknownToRemote = !known
			result.OpenPullRequest, result.MergedPullRequest = matchingPullRequests(pullRequests, slug, integrationBase, head)
		} else {
			var pullRequestErr error
			pullRequests, known, pullRequestErr = githubPullRequestsForCommit(ctx, worktree, slug, head)
			if pullRequestErr != nil {
				return ListResult{}, pullRequestErr
			}
			result.HeadUnknownToRemote = !known
			result.OpenPullRequest, result.MergedPullRequest = matchingPullRequests(pullRequests, slug, base, head)
		}
		base = integrationBase
		result.Base = base
		result.IntegratedAtOrigin, err = isAncestor(ctx, canonical, head, result.RemoteTargetSHA)
		if err != nil {
			return ListResult{}, err
		}
		// LocallyMerged historically described the remote-tracking ref. Once an
		// exact fetched target is available, report the stronger observation.
		result.LocallyMerged = result.IntegratedAtOrigin
		if branch != "" {
			// A detached checkout has no branch on origin to read, and asking
			// for the head of an empty ref is a question with no answer.
			result.RemoteHeadSHA, err = remoteBranchHead(ctx, canonical, branch)
			if err != nil {
				return ListResult{}, err
			}
		}
		if !result.IntegratedAtOrigin {
			result.RebaseMergedAtOrigin, err = rebaseMergedPullRequestIntegrated(ctx, canonical, head, result.RemoteTargetSHA, result.MergedPullRequest)
			if err != nil {
				return ListResult{}, err
			}
			result.IntegratedAtOrigin = result.RebaseMergedAtOrigin
		}
		if !result.IntegratedAtOrigin {
			receipt, rejection, err := absorbedLandingReceipt(
				ctx, worktree, canonical, slug, head, base, result.RemoteTargetSHA, absorbedBy, pullRequests,
			)
			if err != nil {
				return ListResult{}, err
			}
			result.AbsorbedByRejection = rejection
			if receipt != nil {
				result.AbsorbedAtOrigin = true
				result.AbsorbedBySHA = receipt.LandingSHA
				result.IntegratedAtOrigin = true
				if result.MergedPullRequest == nil {
					result.MergedPullRequest = receipt.PullRequest
				}
			}
		}
		if !result.IntegratedAtOrigin && policy.residueEvidence {
			// Last, and only when every cheaper proof has said no: ask whether
			// an ancestor landed and this checkout is carrying residue on top
			// of it. Reporting that is the difference between "awaiting push"
			// and "landed, and here are the two commits that did not".
			result.Landing, err = landingEvidence(
				ctx, worktree, canonical, slug, head, base, result.RemoteTargetSHA, policy.residueDepth,
			)
			if err != nil {
				return ListResult{}, err
			}
			if result.Landing != nil && result.Landing.PullRequest != nil && result.MergedPullRequest == nil {
				result.MergedPullRequest = result.Landing.PullRequest
			}
		}
	}
	return result, nil
}

// applyWorktreeAge records who holds a checkout and how long it has been
// there. Nothing in WB could previously tell an abandoned worktree from a
// paused one: the inventory showed `owners=orphaned` for 20 of them and no age
// at all, so nothing ever prompted a sweep and the sweep only happened when the
// disk filled.
func applyWorktreeAge(result *ListResult, worktree string, policy inspectPolicy) {
	result.Owner = worktreeOwnerName(result.Owners, result.OwnerState)
	if manifest, err := ReadManifest(worktree); err == nil && !manifest.CreatedAt.IsZero() {
		result.CreatedAt = manifest.CreatedAt.UTC()
	} else if info, statErr := os.Stat(worktree); statErr == nil {
		// A checkout WB did not create — an adopted or hand-made one — still
		// has an age, and reporting the directory's own is honest as long as
		// nothing claims it came from a manifest.
		result.CreatedAt = info.ModTime().UTC()
	}
	if result.CreatedAt.IsZero() {
		return
	}
	now := policy.clock()
	if age := now.Sub(result.CreatedAt); age > 0 {
		result.AgeSeconds = int64(age / time.Second)
	}
	if policy.ttl > 0 {
		result.TTLSeconds = int64(policy.ttl / time.Second)
		result.Expired = now.Sub(result.CreatedAt) > policy.ttl
	}
}

// worktreeOwnerName names the agent to ask before removing a checkout.
func worktreeOwnerName(owners []OwnerView, state string) string {
	for index := len(owners) - 1; index >= 0; index-- {
		if agent := strings.TrimSpace(owners[index].Agent); agent != "" {
			return agent
		}
	}
	return state
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
// integration decision through an invocation-private ref rather than a stale
// tracking ref or shared FETCH_HEAD. Cleanup repeats this immediately before
// deletion, so a force-pushed target cannot reuse old evidence.
func fetchRemoteTargetHead(ctx context.Context, repository, branch string) (string, error) {
	if cache := targetHeadCacheFrom(ctx); cache != nil {
		return cache.resolve(repository, branch, func() (string, error) {
			return fetchRemoteTargetHeadUncached(ctx, repository, branch)
		})
	}
	return fetchRemoteTargetHeadUncached(ctx, repository, branch)
}

// remoteTargetFetchTimeout bounds one exact-target fetch. A fleet walk makes one
// of these per repository and a healthy fetch of a single ref answers in
// seconds, so this is generous rather than tight — its job is to convert a
// remote that will never answer into a reported state, not to police slow ones.
// A live sweep once sat 38 minutes on a single unanswered fetch, holding a task
// lock, with no output and no way to tell it apart from ordinary slowness.
//
// It is a var so tests can shorten it.
var remoteTargetFetchTimeout = 90 * time.Second

func isMissingRemoteTargetError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	// These are the explicit missing-ref diagnostics emitted by Git's fetch
	// transport. Do not broaden on generic network, authentication, or timeout
	// failures: those must remain non-recoverable for cleanup safety.
	for _, marker := range []string{
		"couldn't find remote ref",
		"could not find remote ref",
		"remote ref does not exist",
		"no such ref",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func fetchRemoteTargetHeadUncached(ctx context.Context, repository, branch string) (string, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, remoteTargetFetchTimeout)
	defer cancel()
	head, err := fetchOriginBranchToPrivateRef(fetchCtx, repository, branch, func(runCtx context.Context, args ...string) (string, error) {
		return git(runCtx, repository, args...)
	}, nil)
	if err != nil {
		// Distinguish our own deadline from a caller who cancelled the whole run:
		// the operator needs to know this one remote never answered, and which
		// budget it blew, rather than reading a bare "signal: killed".
		if errors.Is(fetchCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return "", fmt.Errorf("fetch exact origin/%s target: remote did not answer within %s", branch, remoteTargetFetchTimeout)
		}
		return "", fmt.Errorf("fetch exact origin/%s target: %w", branch, err)
	}
	return head, nil
}

// githubPullRequests reads pull requests associated with the immutable source
// commit rather than filtering by the current branch name. A branch can be
// renamed, deleted, or (as in a rebase merge) differ from the managed
// worktree's branch while the exact head SHA remains the durable receipt.
func githubPullRequests(ctx context.Context, worktree, repository, head string) ([]githubPullRequest, error) {
	pullRequests, _, err := githubPullRequestsForCommit(ctx, worktree, repository, head)
	return pullRequests, err
}

// githubPullRequestsForCommit additionally reports whether GitHub knows the
// commit at all. A commit it has never seen was never pushed, and a checkout
// holding one is the single class that can still lose work — so it is the one
// class no widening may ever retire, and saying "never pushed" out loud is the
// difference between a refusal an operator can act on and a mystery.
func githubPullRequestsForCommit(ctx context.Context, worktree, repository, head string) ([]githubPullRequest, bool, error) {
	result := githubobserver.Execute(ctx, worktree, "api", "--paginate", "repos/"+repository+"/commits/"+head+"/pulls")
	if result.Err != nil {
		if unknownGitHubCommit(result.Stdout) {
			// A commit GitHub has never seen has no pull request associated
			// with it, which is an answer rather than a failure. Local commits
			// on an unpushed branch are ordinary, and treating them as fatal
			// hid the whole worktree behind a malformed-candidate diagnostic —
			// including from --absorbed-by, which exists precisely for work
			// that reached the target without this commit ever being pushed.
			return nil, false, nil
		}
		return nil, false, fmt.Errorf(
			"query pull requests for %s source commit %s: %w: %s",
			repository, head, result.Err, strings.TrimSpace(string(result.Stderr)+string(result.Stdout)),
		)
	}
	var pullRequests []githubPullRequest
	if err := json.Unmarshal(result.Stdout, &pullRequests); err != nil {
		return nil, true, fmt.Errorf("decode pull requests for %s source commit %s: %w", repository, head, err)
	}
	return pullRequests, true, nil
}

// unknownGitHubCommit recognizes only GitHub's own structured answer that the
// commit does not exist there. It reads the API error body rather than
// matching human-readable text anywhere in the output, so an unrelated
// failure that merely mentions a commit is never mistaken for this one.
func unknownGitHubCommit(body []byte) bool {
	var failure struct {
		Message string `json:"message"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(body), &failure); err != nil {
		return false
	}
	return failure.Status == "422" && strings.HasPrefix(failure.Message, "No commit found for SHA")
}

func matchingPullRequests(pullRequests []githubPullRequest, repository, base, head string) (open, merged *PullRequest) {
	for _, candidate := range pullRequests {
		pullRequest := &PullRequest{
			Number: candidate.Number, URL: candidate.URL, State: candidate.State,
			Repository: repository, Base: candidate.Base.Ref, HeadSHA: candidate.Head.SHA, Merged: candidate.MergedAt,
		}
		if candidate.MergedAt != nil {
			pullRequest.State = "MERGED"
		}
		pullRequest.MergeSHA = candidate.MergeCommitSHA
		if strings.EqualFold(candidate.State, "OPEN") {
			if candidate.Base.Ref != base || candidate.Head.SHA != head {
				continue
			}
			if open == nil || candidate.Number > open.Number {
				open = pullRequest
			}
			continue
		}
		if !strings.EqualFold(pullRequest.State, "MERGED") ||
			candidate.Base.Ref != base ||
			candidate.Head.SHA != head ||
			candidate.MergedAt == nil {
			continue
		}
		if merged == nil || candidate.MergedAt.After(*merged.Merged) {
			merged = pullRequest
		}
	}
	return open, merged
}

// mergedPullRequestTarget returns the target branch of an exact-head merged
// PR when the recorded lifecycle target no longer exists. It deliberately
// refuses ambiguity: two merged PRs for the same head targeting different
// branches do not identify which remote target should authorize cleanup.
func mergedPullRequestTarget(ctx context.Context, pullRequests []githubPullRequest, head, recordedBase string) (string, bool) {
	target := ""
	for _, candidate := range pullRequests {
		if candidate.Head.SHA != head || candidate.MergedAt == nil || candidate.Base.Ref == recordedBase {
			continue
		}
		if !validBranch(ctx, candidate.Base.Ref) {
			continue
		}
		if target != "" && target != candidate.Base.Ref {
			return "", false
		}
		target = candidate.Base.Ref
	}
	return target, target != ""
}

// rebaseMergedPullRequestIntegrated recognizes the one case in which a
// branch's exact source head is correctly absent from the target history: a
// GitHub rebase merge. The immutable PR receipt must bind that exact source
// head to an exact merge-result commit. That result must be in the freshly
// fetched target and have precisely the same tree as the source; matching a
// PR number, title, or a merely similar patch is deliberately insufficient.
func rebaseMergedPullRequestIntegrated(ctx context.Context, repository, head, target string, pullRequest *PullRequest) (bool, error) {
	if pullRequest == nil || pullRequest.HeadSHA != head || !isGitObjectID(pullRequest.MergeSHA) {
		return false, nil
	}
	mergeInTarget, err := isAncestor(ctx, repository, pullRequest.MergeSHA, target)
	if err != nil || !mergeInTarget {
		return mergeInTarget, err
	}
	sourceTree, err := commitTree(ctx, repository, head)
	if err != nil {
		return false, err
	}
	mergeTree, err := commitTree(ctx, repository, pullRequest.MergeSHA)
	if err != nil {
		return false, err
	}
	return sourceTree == mergeTree, nil
}

// absorbedReceipt is the landing evidence for a branch whose exact head can
// never reach the target because a differently named integration branch
// carried its content there. A merger batching several completed candidates
// onto one integration branch and landing that branch once is the workflow a
// repository requiring linear history forces; the source branch tips are then
// absent from the target by construction, not by omission.
type absorbedReceipt struct {
	// LandingSHA is the exact commit in the target that introduced the work.
	LandingSHA string
	// PullRequest is the merged pull-request receipt when GitHub supplied one.
	PullRequest *PullRequest
}

// absorbedLandingReceipt establishes, with evidence only, that a branch's
// content reached the exact fetched origin target inside another branch.
//
// Two receipt sources are accepted, never a bare assertion. GitHub's own
// commit-to-pull-request index is preferred: it is computed by GitHub, not
// written by the author, and it already binds this immutable source commit to
// the pull request that introduced it. An operator pointer (--absorbed-by)
// covers the landings GitHub cannot associate, such as content cherry-picked
// rather than merged into the integration branch, and is held to a stricter
// bar precisely because a human chose it.
//
// Every path proves containment locally and cryptographically: merging the
// branch into the landing commit must add nothing to it, and merging it into
// the freshly fetched target must add nothing there either. The second proof
// is what refuses a branch whose work landed and was later reverted.
//
// A discovered receipt that does not hold is an ordinary negative answer. An
// explicitly supplied one that does not hold is returned as a rejection
// string, so the operator reads exactly which verification refused it rather
// than a generic awaiting_push verdict.
func absorbedLandingReceipt(
	ctx context.Context,
	worktree, repository, slug, head, base, target, absorbedBy string,
	pullRequests []githubPullRequest,
) (*absorbedReceipt, string, error) {
	if absorbedBy != "" {
		return attestedAbsorbedReceipt(ctx, worktree, repository, slug, head, base, target, absorbedBy)
	}
	pullRequest := absorbingPullRequest(pullRequests, base)
	if pullRequest == nil || !isGitObjectID(pullRequest.MergeSHA) {
		return nil, "", nil
	}
	landed, err := isAncestor(ctx, repository, pullRequest.MergeSHA, target)
	if err != nil || !landed {
		return nil, "", err
	}
	absorbed, err := contentAbsorbed(ctx, repository, head, pullRequest.MergeSHA, target)
	if err != nil || !absorbed {
		return nil, "", err
	}
	return &absorbedReceipt{LandingSHA: pullRequest.MergeSHA, PullRequest: pullRequest}, "", nil
}

// attestedAbsorbedReceipt verifies an operator-supplied pointer. The pointer
// selects which commit to examine; it grants nothing. Beyond the containment
// proofs every receipt needs, the named commit must be exactly where the work
// entered the target: without that test an operator could name the target tip
// itself and silently reduce the flag to an unreceipted content assertion.
func attestedAbsorbedReceipt(
	ctx context.Context,
	worktree, repository, slug, head, base, target, absorbedBy string,
) (*absorbedReceipt, string, error) {
	landingSHA, pullRequest, rejection, err := resolveAbsorbedBy(ctx, worktree, repository, slug, base, absorbedBy)
	if err != nil || rejection != "" {
		return nil, rejection, err
	}
	landed, err := isAncestor(ctx, repository, landingSHA, target)
	if err != nil {
		return nil, "", err
	}
	if !landed {
		return nil, fmt.Sprintf(
			"--absorbed-by %s resolved to %s, which is not contained in the exact fetched origin/%s target %s",
			absorbedBy, landingSHA, base, target,
		), nil
	}
	inLanding, err := contentContained(ctx, repository, head, landingSHA)
	if err != nil {
		return nil, "", err
	}
	if !inLanding {
		return nil, fmt.Sprintf(
			"--absorbed-by %s resolved to %s, which does not contain this branch's content",
			absorbedBy, landingSHA,
		), nil
	}
	inTarget, err := contentContained(ctx, repository, head, target)
	if err != nil {
		return nil, "", err
	}
	if !inTarget {
		return nil, fmt.Sprintf(
			"work absorbed by %s no longer survives in the exact fetched origin/%s target %s",
			landingSHA, base, target,
		), nil
	}
	parent, err := commitFirstParent(ctx, repository, landingSHA)
	if err != nil {
		return nil, "", err
	}
	if parent != "" {
		beforeLanding, err := contentContained(ctx, repository, head, parent)
		if err != nil {
			return nil, "", err
		}
		if beforeLanding {
			return nil, fmt.Sprintf(
				"--absorbed-by %s resolved to %s, which is not where this work entered the target: %s already contained it",
				absorbedBy, landingSHA, parent,
			), nil
		}
	}
	return &absorbedReceipt{LandingSHA: landingSHA, PullRequest: pullRequest}, "", nil
}

// resolveAbsorbedBy turns an operator pointer into one exact landing commit.
// A pull-request number must name a pull request that really merged into this
// exact base; anything else must resolve to a commit already present in the
// canonical object database, which a genuine landing always is because the
// target was just fetched.
func resolveAbsorbedBy(
	ctx context.Context,
	worktree, repository, slug, base, absorbedBy string,
) (string, *PullRequest, string, error) {
	pointer := strings.TrimPrefix(strings.TrimSpace(absorbedBy), "#")
	if pointer == "" {
		return "", nil, "--absorbed-by requires a pull request number or landing commit", nil
	}
	if number, err := strconv.Atoi(pointer); err == nil {
		if number <= 0 {
			return "", nil, fmt.Sprintf("--absorbed-by pull request number %d is not positive", number), nil
		}
		return resolveAbsorbedByPullRequest(ctx, worktree, slug, base, number)
	}
	landingSHA, err := git(ctx, repository, "rev-parse", "--verify", "--end-of-options", pointer+"^{commit}")
	if err != nil {
		return "", nil, fmt.Sprintf("--absorbed-by %s does not resolve to a commit in %s", absorbedBy, repository), nil
	}
	if !isGitObjectID(landingSHA) {
		return "", nil, fmt.Sprintf("--absorbed-by %s resolved to invalid commit %q", absorbedBy, landingSHA), nil
	}
	return landingSHA, nil, "", nil
}

func resolveAbsorbedByPullRequest(
	ctx context.Context,
	worktree, slug, base string,
	number int,
) (string, *PullRequest, string, error) {
	response, err := githubobserver.Get(ctx, githubobserver.GetRequest{
		Dir:         worktree,
		Repository:  slug,
		Target:      base,
		Endpoint:    "repos/" + slug + "/pulls/" + strconv.Itoa(number),
		FreshWindow: 0,
	})
	if err != nil {
		return "", nil, "", fmt.Errorf("read %s pull request %d: %w", slug, number, err)
	}
	var candidate githubPullRequest
	if err := json.Unmarshal(response.Body, &candidate); err != nil {
		return "", nil, "", fmt.Errorf("decode %s pull request %d: %w", slug, number, err)
	}
	if candidate.MergedAt == nil {
		return "", nil, fmt.Sprintf("--absorbed-by pull request %s#%d is not merged", slug, number), nil
	}
	if candidate.Base.Ref != base {
		return "", nil, fmt.Sprintf(
			"--absorbed-by pull request %s#%d merged into %q, not the requested base %q",
			slug, number, candidate.Base.Ref, base,
		), nil
	}
	if !isGitObjectID(candidate.MergeCommitSHA) {
		return "", nil, fmt.Sprintf(
			"--absorbed-by pull request %s#%d has invalid merge commit %q",
			slug, number, candidate.MergeCommitSHA,
		), nil
	}
	return candidate.MergeCommitSHA, &PullRequest{
		Number: candidate.Number, URL: candidate.URL, State: "MERGED",
		Base: candidate.Base.Ref, HeadSHA: candidate.Head.SHA,
		MergeSHA: candidate.MergeCommitSHA, Merged: candidate.MergedAt,
	}, "", nil
}

// absorbingPullRequest selects the newest merged pull request into the exact
// base that GitHub associates with the immutable source commit. Unlike
// matchingPullRequests it deliberately does not require the pull-request head
// to equal that commit: when a merger batches candidates onto one integration
// branch, the branch name is evidence of nothing and the commit association is
// the receipt. An open pull request is never a landing receipt.
func absorbingPullRequest(pullRequests []githubPullRequest, base string) *PullRequest {
	var absorbing *PullRequest
	for _, candidate := range pullRequests {
		if candidate.MergedAt == nil || candidate.Base.Ref != base {
			continue
		}
		if absorbing != nil && !candidate.MergedAt.After(*absorbing.Merged) {
			continue
		}
		absorbing = &PullRequest{
			Number: candidate.Number, URL: candidate.URL, State: "MERGED",
			Base: candidate.Base.Ref, HeadSHA: candidate.Head.SHA,
			MergeSHA: candidate.MergeCommitSHA, Merged: candidate.MergedAt,
		}
	}
	return absorbing
}

// contentAbsorbed requires both containment proofs a landing receipt needs:
// the work is wholly inside the commit that carried it, and it is still wholly
// inside the target that was just fetched. Proving only the first would clean
// up a branch whose landing was later reverted.
func contentAbsorbed(ctx context.Context, repository, head, landingSHA, target string) (bool, error) {
	inLanding, err := contentContained(ctx, repository, head, landingSHA)
	if err != nil || !inLanding {
		return false, err
	}
	return contentContained(ctx, repository, head, target)
}

// contentContained proves that a branch head adds nothing to a commit. The
// three-way merge of the branch into that commit must both succeed and produce
// exactly that commit's own tree; a conflict, or any residual delta, means part
// of the branch is missing from it. A branch containing a revert of work the
// commit still carries therefore fails, because merging it would remove that
// work.
func contentContained(ctx context.Context, repository, head, commit string) (bool, error) {
	merged, clean, err := mergeResultTree(ctx, repository, commit, head)
	if err != nil || !clean {
		return false, err
	}
	existing, err := commitTree(ctx, repository, commit)
	if err != nil {
		return false, err
	}
	return merged == existing, nil
}

// mergeResultTree performs a real three-way merge and reports the resulting
// tree without touching any ref, index, or working tree; only unreferenced
// objects are written. A conflicted merge is a normal negative containment
// answer, not an error.
func mergeResultTree(ctx context.Context, repository, ours, theirs string) (string, bool, error) {
	command := exec.CommandContext(
		ctx, "git", "-C", repository, "merge-tree", "--write-tree", "--no-messages", "--end-of-options", ours, theirs,
	)
	command.Env = console.Env()
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, fmt.Errorf(
			"merge %s into %s in %s: %w: %s", theirs, ours, repository, err, strings.TrimSpace(stderr.String()),
		)
	}
	tree, _, _ := strings.Cut(strings.TrimSpace(stdout.String()), "\n")
	tree = strings.TrimSpace(tree)
	if !isGitObjectID(tree) {
		return "", false, fmt.Errorf("merge %s into %s in %s produced invalid tree %q", theirs, ours, repository, tree)
	}
	return tree, true, nil
}

// commitFirstParent returns the first parent of a commit, or an empty string
// for a root commit.
func commitFirstParent(ctx context.Context, repository, revision string) (string, error) {
	parents, err := git(ctx, repository, "rev-list", "--parents", "-n", "1", "--end-of-options", revision)
	if err != nil {
		return "", fmt.Errorf("resolve parents of %s: %w", revision, err)
	}
	fields := strings.Fields(parents)
	if len(fields) < 2 {
		return "", nil
	}
	if !isGitObjectID(fields[1]) {
		return "", fmt.Errorf("commit %s resolved to invalid first parent %q", revision, fields[1])
	}
	return fields[1], nil
}

func commitTree(ctx context.Context, repository, revision string) (string, error) {
	tree, err := git(ctx, repository, "rev-parse", revision+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("resolve tree for %s: %w", revision, err)
	}
	if !isGitObjectID(tree) {
		return "", fmt.Errorf("revision %s resolved to invalid tree SHA %q", revision, tree)
	}
	return tree, nil
}

// cleanupEligibility answers whether one candidate may be retired now. Safety
// is decided first and independently; the caller's remote-retirement policy is
// applied only to a candidate that is otherwise eligible, so a refusal always
// names the most specific reason WB actually observed.
func cleanupEligibility(entry ListResult, options CleanupOptions, now time.Time) (bool, string) {
	if eligible, reason := cleanupSafetyEligibility(entry, options.OlderThan, now, options.AllowResidue); !eligible {
		return false, reason
	}
	if options.RequireRemoteRetirement && !options.DeleteRemote && entry.RemoteHeadSHA != "" {
		return false, "origin/" + entry.Branch + " still exists at " + shortSHA(entry.RemoteHeadSHA) +
			"; rerun with --remote so the retired source branch cannot remain as backlog"
	}
	return true, ""
}

func cleanupSafetyEligibility(entry ListResult, olderThan time.Duration, now time.Time, allowResidue bool) (bool, string) {
	switch {
	case entry.Locked:
		return false, lockedReason(entry, resumeInterruptedCommand(entry.Task))
	case !entry.Clean:
		return false, "worktree has local changes"
	case entry.OpenPullRequest != nil:
		return false, "branch still has an open pull request: " + entry.OpenPullRequest.URL
	case entry.SupersessionRejection != "":
		return false, "trusted supersession receipt refused: " + entry.SupersessionRejection
	case entry.SupersededAtOrigin && entry.RemoteHeadSHA != "" && entry.RemoteHeadSHA != entry.HeadSHA:
		return false, "remote branch advanced after the supersession receipt"
	case entry.SupersededAtOrigin:
		// A superseded branch is intentionally not integrated as one commit;
		// its eligibility comes only from the independently reviewed receipt.
		return true, ""
	case entry.Detached && !entry.IntegratedAtOrigin:
		return false, detachedRefusal(entry)
	case !entry.IntegratedAtOrigin && entry.HeadUnknownToRemote && !entry.landedWithResidue():
		return false, "head " + shortSHA(entry.HeadSHA) + " was never pushed: GitHub's commit index has never seen it, " +
			"so nothing can prove this work landed and removing the checkout would lose it"
	case !entry.IntegratedAtOrigin && entry.landedWithResidue() && allowResidue:
		// The landing is proved by the same receipt every other eligible
		// candidate needs; only the residual commits are being widened past,
		// and the caller has already been shown exactly which they are.
		return true, ""
	case !entry.IntegratedAtOrigin && entry.landedWithResidue():
		return false, entry.residueReason()
	case !entry.IntegratedAtOrigin && entry.AbsorbedByRejection != "":
		return false, "current branch head is not integrated into the exact origin target (awaiting push): " +
			entry.AbsorbedByRejection
	case !entry.IntegratedAtOrigin:
		return false, "current branch head is not integrated into the exact origin target (awaiting push)"
	case entry.RemoteHeadSHA != "" && entry.RemoteHeadSHA != entry.HeadSHA:
		return false, "remote branch advanced after the merged pull request"
	case entry.MergedPullRequest != nil && olderThan > 0 && entry.MergedPullRequest.Merged.Add(olderThan).After(now):
		return false, "merged pull request is newer than the cleanup safety window"
	default:
		return true, ""
	}
}

// applyMergeReceiptCleanupProof grants no general absorption shortcut. It only
// recognizes a source which exactly matches a worktree-merge receipt and then
// repeats every identity-bearing Git observation needed for the special
// squash-landing shape. Ordinary cleanup continues to use its generic
// containment and --absorbed-by checks unchanged.
func applyMergeReceiptCleanupProof(ctx context.Context, proofs []MergeReceiptCleanupProof, entry *ListResult) error {
	for _, proof := range proofs {
		if filepath.Clean(proof.SourceWorktree) != filepath.Clean(entry.WorktreeDir) {
			continue
		}
		if rejection := mergeReceiptCleanupProofRejection(ctx, proof, *entry); rejection != "" {
			entry.AbsorbedByRejection = "worktree-merge receipt cleanup proof: " + rejection
			return nil
		}
		entry.IntegratedAtOrigin = true
		entry.AbsorbedAtOrigin = true
		entry.AbsorbedBySHA = proof.LandingSHA
		entry.AbsorbedByRejection = ""
		return nil
	}
	return nil
}

func mergeReceiptCleanupProofRejection(ctx context.Context, proof MergeReceiptCleanupProof, entry ListResult) string {
	for label, value := range map[string]string{
		"repository": proof.Repository, "target": proof.Target, "source task": proof.SourceTask,
		"source worktree": proof.SourceWorktree, "source branch": proof.SourceBranch,
	} {
		if strings.TrimSpace(value) == "" {
			return "receipt has no " + label
		}
	}
	for label, value := range map[string]string{
		"source SHA": proof.SourceSHA, "candidate SHA": proof.CandidateSHA, "landing SHA": proof.LandingSHA,
	} {
		if !isGitObjectID(value) {
			return "receipt has invalid " + label
		}
	}
	if entry.Repository != proof.Repository || entry.Base != proof.Target || entry.Task != proof.SourceTask ||
		filepath.Clean(entry.WorktreeDir) != filepath.Clean(proof.SourceWorktree) || entry.Branch != proof.SourceBranch || entry.HeadSHA != proof.SourceSHA {
		return "source identity no longer matches the receipt"
	}
	if !isGitObjectID(entry.RemoteTargetSHA) {
		return "exact fetched target identity is unavailable"
	}
	ancestor, err := isAncestor(ctx, entry.CanonicalDir, proof.SourceSHA, proof.CandidateSHA)
	if err != nil {
		return fmt.Sprintf("verify source %s is an ancestor of candidate %s: %v", proof.SourceSHA, proof.CandidateSHA, err)
	}
	if !ancestor {
		return fmt.Sprintf("source %s is not an ancestor of candidate %s", proof.SourceSHA, proof.CandidateSHA)
	}
	candidateTree, err := git(ctx, entry.CanonicalDir, "rev-parse", "--verify", proof.CandidateSHA+"^{tree}")
	if err != nil {
		return fmt.Sprintf("resolve candidate tree %s: %v", proof.CandidateSHA, err)
	}
	landingTree, err := git(ctx, entry.CanonicalDir, "rev-parse", "--verify", proof.LandingSHA+"^{tree}")
	if err != nil {
		return fmt.Sprintf("resolve landing tree %s: %v", proof.LandingSHA, err)
	}
	if candidateTree != landingTree {
		return fmt.Sprintf("candidate tree %s does not equal landing tree %s", candidateTree, landingTree)
	}
	landed, err := isAncestor(ctx, entry.CanonicalDir, proof.LandingSHA, entry.RemoteTargetSHA)
	if err != nil {
		return fmt.Sprintf("verify landing %s is contained in fetched target %s: %v", proof.LandingSHA, entry.RemoteTargetSHA, err)
	}
	if !landed {
		return fmt.Sprintf("landing %s is not contained in the exact fetched target %s", proof.LandingSHA, entry.RemoteTargetSHA)
	}
	return ""
}

// blockDiagnosedTasks blocks eligibility only for the coordinated task a
// malformed candidate belongs to — the same all-or-nothing unit
// blockUnsafeTasks already applies to an unclean, locked, or unmerged
// sibling. A malformed candidate itself never becomes a CleanupResult (it
// isn't a validated ListResult), so without this it would silently sit
// outside the coordination that is supposed to cover its whole task; every
// other task, and every other candidate within the current --filter
// selection, is unaffected.
// A diagnostic whose path a resumable backlog record already names is excluded:
// it is WB's own residue, and the run holding that record is the one that
// removes it.
func blockDiagnosedTasks(results []CleanupResult, diagnostics []ListDiagnostic, backlogPaths map[string]bool) {
	if len(diagnostics) == 0 {
		return
	}
	reasonByTask := map[string]string{}
	for _, diagnostic := range diagnostics {
		if backlogPaths[filepath.Clean(diagnostic.Path)] {
			continue
		}
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
		if artifact.NonBlocking {
			continue
		}
		// An empty task namespace has no repository under it to coordinate
		// with, so its own ineligibility never blocks a task.
		if artifact.Eligible || artifact.Kind == lifecycleArtifactKindTaskNamespace {
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

// scopeLifecycleArtifacts keeps an exact repository cleanup from being held
// hostage by a non-empty retired stage that is itself a proven Git checkout
// for a different repository. An unclassified stage has no safe identity and
// remains blocking; that is the deliberate fail-closed boundary.
func scopeLifecycleArtifacts(artifacts []LifecycleArtifact, exactRepository, filter string) {
	for index := range artifacts {
		artifact := &artifacts[index]
		if artifact.Repository == "" {
			continue
		}
		if exactRepository != "" && artifact.Repository != exactRepository ||
			filter != "" && !filterMatches(filter, artifact.Repository) {
			artifact.NonBlocking = true
			artifact.Reason = fmt.Sprintf("retired stage belongs to %s, outside this repository-scoped cleanup; recover it separately", artifact.Repository)
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

// blockEffortsWithLiveDescendants refuses to terminalize a feature effort while
// any of its sub-agent task efforts still has a worktree.
//
// Children are deliberately NOT nested inside a parent's directory, precisely so
// removing a parent cannot delete a child's working tree. That layout choice
// only pays off if cleanup also declines to retire the parent's branch out from
// under work that was based on it, so the check lives here rather than relying
// on the filesystem to enforce it.
//
// Descendants are found lexically, by directory name, which costs one readdir
// per worktrees root and stays correct when a child's own manifest is missing —
// the common case for the worktrees that predate manifests entirely.
func blockEffortsWithLiveDescendants(results []CleanupResult, worktreesRoots []string) {
	live := map[string]bool{}
	for _, root := range worktreesRoots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() && !isWorktreeStagingDirectory(entry.Name()) {
				live[entry.Name()] = true
			}
		}
	}
	for index := range results {
		task := results[index].Task
		if !results[index].Eligible || task == "" {
			continue
		}
		var children []string
		for candidate := range live {
			if IsAncestorEffort(task, candidate) {
				children = append(children, candidate)
			}
		}
		if len(children) == 0 {
			continue
		}
		sort.Strings(children)
		results[index].Eligible = false
		results[index].Reason = fmt.Sprintf(
			"effort %q still has live sub-efforts (%s); terminalize them first",
			task, strings.Join(children, ", "),
		)
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
		home,
		wbhome.Layout{WorktreesRoot: entry.WorktreesRoot, Local: entry.Local},
		entry.Task,
		entry.WorktreeDir,
		options.Base,
		options.AbsorbedBy,
		true,
		false,
		entry.External,
		cleanupInspectPolicy(options),
	)
	if err != nil {
		return ListResult{}, fmt.Errorf("preflight cleanup %s: %w", entry.Repository, err)
	}
	if err := applyMergeReceiptCleanupProof(ctx, options.MergeReceiptProofs, &refreshed); err != nil {
		return ListResult{}, fmt.Errorf("preflight cleanup %s receipt proof: %w", entry.Repository, err)
	}
	if err := applySupersessionReceipt(ctx, options.SupersededBy, &refreshed); err != nil {
		return ListResult{}, fmt.Errorf("preflight cleanup %s supersession receipt: %w", entry.Repository, err)
	}
	if refreshed.SupersessionRejection != "" {
		return ListResult{}, fmt.Errorf("preflight cleanup %s supersession receipt refused: %s", entry.Repository, refreshed.SupersessionRejection)
	}
	if err := worktree.validate(); err != nil {
		return ListResult{}, err
	}
	if eligible, reason := cleanupEligibility(refreshed, options, now); !eligible {
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
	return acquireCleanupTaskAtReclaimingInterrupted(worktreesRoot, taskName, false)
}

// acquireCleanupTaskAtOrCreate creates only the WB_HOME coordination shell
// when an older/manual local checkout has no prior lifecycle metadata there.
// The returned descriptors remain held for the full transaction.
func acquireCleanupTaskAtOrCreate(worktreesRoot, taskName string) (*cleanupTaskHandle, error) {
	task, err := acquireCleanupTaskAt(worktreesRoot, taskName)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return task, err
	}
	home := filepath.Dir(filepath.Clean(worktreesRoot))
	op, prepareErr := prepareOperationRoot(home, taskName, nil)
	if prepareErr != nil {
		return nil, prepareErr
	}
	lock, lockErr := acquireLockAt(op.Directory, taskName)
	if lockErr != nil {
		op.close()
		return nil, lockErr
	}
	return &cleanupTaskHandle{worktreesPath: filepath.Clean(worktreesRoot), taskPath: op.Path, worktrees: op.Worktrees, task: op.Directory, lock: lock}, nil
}

// purgeTerminalTaskLockDebris removes every retired operation lock left
// directly under a task directory, immediately after this cleanup
// transaction released its own — but only when the directory now holds
// nothing except retired locks: no owner-namespace directory, no live
// `.lock`, nothing else. That is exactly what a genuinely terminal task
// leaves behind release after release: `.wb-retired-lock-*` is created only
// so a *later* operation on the very same task directory can reclaim it (see
// claimRetiredLock), and a task nobody ever touches again just accumulates
// them forever. removeEmptyParent has by this point already retired every
// owner directory that became empty, so a task whose last repository just
// finished cleanup normally satisfies the all-retired-locks test below.
//
// It is deliberately best-effort and never returns an error: a live `.lock`
// created by a concurrent operation in the narrow window after release
// simply fails the "every entry is a retired lock" test and the directory is
// left untouched, to be reclaimed normally by that operation or swept later
// by `wb worktree cleanup --retire-shells`. It never inspects, let alone
// deletes, anything that is not a plain, single-link `.wb-retired-lock-*`
// entry this package itself could have created (see
// exclusivelyOwnedLockIdentity for the same reasoning).
func purgeTerminalTaskLockDebris(task *cleanupTaskHandle) {
	if task == nil || task.task == nil {
		return
	}
	if _, err := task.task.Seek(0, 0); err != nil {
		return
	}
	entries, err := task.task.ReadDir(-1)
	if err != nil {
		return
	}
	retired := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".wb-retired-lock-") {
			return // an owner directory, a live .lock, or anything else: not terminal.
		}
		retired = append(retired, name)
	}
	for _, name := range retired {
		var stat unix.Stat_t
		if statErr := unix.Fstatat(int(task.task.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); statErr != nil {
			continue
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
			continue // never remove anything that is not an ordinary WB-owned lock retirement.
		}
		_ = unix.Unlinkat(int(task.task.Fd()), name, 0)
	}
}

// acquireCleanupTaskAtReclaimingInterrupted is the resume-only form. See
// acquireLockAtReclaimingInterrupted for why reclaiming an interrupted lock is
// restricted to a caller that can describe and revalidate exactly what the
// interruption left behind.
func acquireCleanupTaskAtReclaimingInterrupted(
	worktreesRoot, taskName string, reclaimInterrupted bool,
) (*cleanupTaskHandle, error) {
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
	lock, err := acquireLockAtReclaimingInterrupted(task, reclaimInterrupted, taskName)
	if err != nil {
		handle.close()
		return nil, fmt.Errorf("lock cleanup task %s: %w", taskName, err)
	}
	handle.lock = lock
	return handle, nil
}

// reclaimNamedInterruptedCleanupTask opens only one exact named task directory
// below a resolver-recognized WB root. It never scans or reclaims any sibling
// task. The retained descriptor is kept through cleanup, which makes a late
// replacement fail closed rather than turning validation into a pathname race.
func reclaimNamedInterruptedCleanupTask(resolution wbhome.Resolution, taskName string) (*cleanupTaskHandle, *InterruptedLockRecovery, error) {
	// A default-local or relocated-shared checkout keeps its task lock in
	// WB_HOME, not below its physical checkout root. Search the distinct logical
	// lock roots so an explicit recovery reaches the same inode normal cleanup
	// and Create serialize through. Legacy layouts retain their physical root.
	roots := make([]string, 0, 1)
	seenRoots := make(map[string]bool)
	for _, layout := range resolution.Read {
		root := filepath.Clean(lifecycleTaskLockRoot(resolution.Write.Home, layout))
		if seenRoots[root] {
			continue
		}
		seenRoots[root] = true
		worktrees, err := openAbsoluteDirectoryNoFollow(root, false)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, nil, fmt.Errorf("open recovery worktrees root %s: %w", root, err)
		}
		fd, openErr := unix.Openat(int(worktrees.Fd()), taskName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		_ = worktrees.Close()
		if openErr != nil {
			if errors.Is(openErr, unix.ENOENT) {
				continue
			}
			return nil, nil, fmt.Errorf("open recovery task %s without following links: %w", taskName, openErr)
		}
		_ = unix.Close(fd)
		roots = append(roots, root)
	}
	if len(roots) != 1 {
		return nil, nil, fmt.Errorf("interrupted recovery for task %q requires exactly one WB task directory, found %d", taskName, len(roots))
	}
	handle, err := acquireCleanupTaskAtReclaimingInterruptedLock(roots[0], taskName)
	if err != nil {
		return nil, nil, err
	}
	pid, validateErr := interruptedTaskLockPID(handle.lock.file, taskName)
	if validateErr != nil {
		handle.preserveLock()
		handle.close()
		return nil, nil, validateErr
	}
	recovery := &InterruptedLockRecovery{
		Task: taskName, WorktreesRoot: roots[0], Path: filepath.Join(roots[0], taskName, ".lock"),
		PID: pid, Disposition: "validated", Reason: "exact interrupted lock has a conclusively dead owner PID",
	}
	return handle, recovery, nil
}

// acquireCleanupTaskAtReclaimingInterruptedLock deliberately bypasses retired
// lock reuse: an explicit recovery may touch only an existing `.lock` proven
// to match the named task, never a similarly named retirement.
func acquireCleanupTaskAtReclaimingInterruptedLock(worktreesRoot, taskName string) (*cleanupTaskHandle, error) {
	worktrees, err := openAbsoluteDirectoryNoFollow(worktreesRoot, false)
	if err != nil {
		return nil, fmt.Errorf("open recovery worktrees root %s: %w", worktreesRoot, err)
	}
	taskFD, err := unix.Openat(int(worktrees.Fd()), taskName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = worktrees.Close()
		return nil, fmt.Errorf("open recovery task %s without following links: %w", taskName, err)
	}
	task := os.NewFile(uintptr(taskFD), "wb-recovery-task")
	if task == nil {
		_ = unix.Close(taskFD)
		_ = worktrees.Close()
		return nil, fmt.Errorf("wrap recovery task %s", taskName)
	}
	handle := &cleanupTaskHandle{worktreesPath: worktreesRoot, taskPath: filepath.Join(worktreesRoot, taskName), worktrees: worktrees, task: task}
	if err := handle.validate(); err != nil {
		handle.close()
		return nil, err
	}
	lock, err := reclaimInterruptedLock(task, true)
	if err != nil {
		handle.close()
		return nil, fmt.Errorf("recover interrupted cleanup task %s: %w", taskName, err)
	}
	handle.lock = lock
	return handle, nil
}

func (handle *cleanupTaskHandle) preserveLock() {
	if handle == nil || handle.lock.file == nil {
		return
	}
	_ = handle.lock.file.Close()
	handle.lock = operationLock{}
}

func (handle *cleanupTaskHandle) validateHeldLock() error {
	if handle == nil || handle.task == nil || handle.lock.file == nil ||
		!lockEntryStillMatches(handle.task, ".lock", handle.lock.identity) {
		return fmt.Errorf("interrupted cleanup lock changed after recovery")
	}
	return nil
}

func validateRecoveredCleanupLock(recovered bool, handle *cleanupTaskHandle) error {
	if !recovered {
		return nil
	}
	return handle.validateHeldLock()
}

func interruptedTaskLockPID(file *os.File, task string) (int, error) {
	if file == nil {
		return 0, fmt.Errorf("interrupted task %q lock descriptor is unavailable", task)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, fmt.Errorf("seek interrupted task %q lock: %w", task, err)
	}
	contents, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(contents) > 4096 {
		return 0, fmt.Errorf("interrupted task %q lock metadata is invalid", task)
	}
	lines := strings.Split(string(contents), "\n")
	if len(lines) != 3 || lines[2] != "" || lines[0] != "operation="+task {
		return 0, fmt.Errorf("interrupted task %q lock metadata is invalid", task)
	}
	pid, err := strconv.Atoi(strings.TrimPrefix(lines[1], "pid="))
	if err != nil || pid <= 0 || lines[1] != fmt.Sprintf("pid=%d", pid) {
		return 0, fmt.Errorf("interrupted task %q lock metadata is invalid", task)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		return 0, fmt.Errorf("interrupted task %q lock owner PID %d is live or ambiguous", task, pid)
	}
	return pid, nil
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

// localOriginDirectoryForSecurePush authorizes the local file remote named by
// the actual push command as an additional descriptor-bound write root.
// Network/SSH remotes need no local filesystem root. This keeps integration
// tests and legitimate local bare-repository workflows inside the same
// sandbox boundary without re-reading mutable remote configuration when the
// caller already supplied an authenticated exact URL.
func localOriginDirectoryForSecurePush(ctx context.Context, canonical *canonicalRepository, gitArgs []string) (*os.File, string, error) {
	if len(gitArgs) == 0 || gitArgs[0] != "push" {
		return nil, "", nil
	}
	remoteURL, err := securePushRepositoryArgument(gitArgs)
	if err != nil {
		return nil, "", err
	}
	if remoteURL == "origin" {
		remoteURL, err = gitCanonical(ctx, canonical, "remote", "get-url", "--push", "origin")
		if err != nil {
			return nil, "", fmt.Errorf("resolve configured push URL: %w", err)
		}
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
		return nil, "", fmt.Errorf("resolve local push remote directory: %w", err)
	}
	directory, err := openAbsoluteDirectoryNoFollow(localPath, false)
	if err != nil {
		return nil, "", fmt.Errorf("open local push remote directory: %w", err)
	}
	return directory, localPath, nil
}

func securePushRepositoryArgument(gitArgs []string) (string, error) {
	for i := 1; i < len(gitArgs); i++ {
		argument := gitArgs[i]
		if argument == "--" {
			if i+1 >= len(gitArgs) || strings.TrimSpace(gitArgs[i+1]) == "" {
				return "", fmt.Errorf("secure push repository argument is missing")
			}
			return gitArgs[i+1], nil
		}
		if strings.HasPrefix(argument, "-") {
			continue
		}
		return argument, nil
	}
	return "", fmt.Errorf("secure push repository argument is missing")
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
	writeRoots, hookRoots, err := appendSecureHookExecutionCapabilityRoots(args[0], writeRoots)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure cleanup helper: prepare hook runtime layout: %v\n", err)
		return 1
	}
	defer closeSecureHookRootHandles(hookRoots)
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
	if result.Local {
		return openCanonicalLocalCleanupWorktree(task, result.WorktreeDir)
	}
	if result.External {
		// An adopted worktree was never relocated under this held task, so it
		// carries no path relationship to task.taskPath to open descriptor-
		// relative to it. It gets the same no-follow, descriptor-anchored
		// discipline anyway — see openAdoptedCleanupWorktree — just anchored
		// independently from the filesystem root instead of from the task
		// directory's own descriptor.
		return openAdoptedCleanupWorktree(result.WorktreeDir)
	}
	relative, err := filepath.Rel(task.taskPath, result.WorktreeDir)
	if err != nil {
		return openRelocatedManagedCleanupWorktree(task, result.WorktreeDir)
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) == 0 || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return openRelocatedManagedCleanupWorktree(task, result.WorktreeDir)
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
		handle.ownParent = true
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

// openRelocatedManagedCleanupWorktree opens a shared-root checkout whose
// coordination lock lives in WB_HOME. Its physical parent is retained for
// Git removal, but is not retired through the logical task descriptor.
func openRelocatedManagedCleanupWorktree(task *cleanupTaskHandle, worktreePath string) (*cleanupWorktreeHandle, error) {
	worktreePath = filepath.Clean(worktreePath)
	parentPath := filepath.Dir(worktreePath)
	leaf := filepath.Base(worktreePath)
	if leaf == "" || leaf == "." || leaf == string(filepath.Separator) {
		return nil, fmt.Errorf("relocated managed worktree path %s has no checkout segment", worktreePath)
	}
	parent, err := openAbsoluteDirectoryNoFollow(parentPath, false)
	if err != nil {
		return nil, fmt.Errorf("open relocated managed worktree parent %s without following links: %w", parentPath, err)
	}
	handle := &cleanupWorktreeHandle{task: task, worktreePath: worktreePath, parent: parent, parentPath: parentPath, ownParent: true}
	worktreeFD, err := unix.Openat(int(parent.Fd()), leaf, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		handle.close()
		return nil, fmt.Errorf("open relocated managed worktree %s without following links: %w", worktreePath, err)
	}
	handle.worktree = os.NewFile(uintptr(worktreeFD), "wb-cleanup-relocated-managed-worktree")
	if handle.worktree == nil {
		_ = unix.Close(worktreeFD)
		handle.close()
		return nil, fmt.Errorf("wrap relocated managed worktree %s", worktreePath)
	}
	if err := handle.validate(); err != nil {
		handle.close()
		return nil, err
	}
	return handle, nil
}

func openCanonicalLocalCleanupWorktree(task *cleanupTaskHandle, worktreePath string) (*cleanupWorktreeHandle, error) {
	worktreePath = filepath.Clean(worktreePath)
	parentPath := filepath.Dir(worktreePath)
	leaf := filepath.Base(worktreePath)
	if leaf == "" || leaf == "." || leaf == string(filepath.Separator) {
		return nil, fmt.Errorf("canonical local worktree path %s has no task segment", worktreePath)
	}
	parent, err := openAbsoluteDirectoryNoFollow(parentPath, false)
	if err != nil {
		return nil, fmt.Errorf("open canonical local worktree parent %s without following links: %w", parentPath, err)
	}
	handle := &cleanupWorktreeHandle{task: task, worktreePath: worktreePath, parent: parent, parentPath: parentPath, closeParent: false, ownParent: true}
	worktreeFD, err := unix.Openat(int(parent.Fd()), leaf, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		handle.close()
		return nil, fmt.Errorf("open canonical local worktree %s without following links: %w", worktreePath, err)
	}
	handle.worktree = os.NewFile(uintptr(worktreeFD), "wb-cleanup-canonical-local-worktree")
	if handle.worktree == nil {
		_ = unix.Close(worktreeFD)
		handle.close()
		return nil, fmt.Errorf("wrap canonical local worktree %s", worktreePath)
	}
	if err := handle.validate(); err != nil {
		handle.close()
		return nil, err
	}
	return handle, nil
}

// openAdoptedCleanupWorktree opens an adopted external worktree's parent and
// leaf directories with the exact same no-follow, descriptor-anchored
// discipline openCleanupWorktree uses for a worktree nested under a held WB
// task. closeParent stays false: this checkout's parent directory belongs to
// whatever created it externally, never to WB, so removeEmptyParent's normal
// "retire the now-empty owner directory" step must never run against it — see
// removeAdoptedRegistration for the WB-owned registration entry that really
// does get retired once the checkout itself is gone.
func openAdoptedCleanupWorktree(worktreePath string) (*cleanupWorktreeHandle, error) {
	worktreePath = filepath.Clean(worktreePath)
	parentPath := filepath.Dir(worktreePath)
	leaf := filepath.Base(worktreePath)
	if leaf == "" || leaf == "." || leaf == string(filepath.Separator) {
		return nil, fmt.Errorf("adopted worktree path %s has no repository segment to open", worktreePath)
	}
	parent, err := openAbsoluteDirectoryNoFollow(parentPath, false)
	if err != nil {
		return nil, fmt.Errorf("open adopted worktree parent %s without following links: %w", parentPath, err)
	}
	handle := &cleanupWorktreeHandle{worktreePath: worktreePath, parent: parent, parentPath: parentPath, closeParent: false, ownParent: true}
	worktreeFD, err := unix.Openat(int(parent.Fd()), leaf, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		handle.close()
		return nil, fmt.Errorf("open adopted worktree %s without following links: %w", worktreePath, err)
	}
	handle.worktree = os.NewFile(uintptr(worktreeFD), "wb-cleanup-adopted-worktree")
	if handle.worktree == nil {
		_ = unix.Close(worktreeFD)
		handle.close()
		return nil, fmt.Errorf("wrap adopted worktree %s", worktreePath)
	}
	if err := handle.validate(); err != nil {
		handle.close()
		return nil, err
	}
	return handle, nil
}

// removeAdoptedRegistration retires the small WB-home registration entry an
// adopted external worktree leaves behind — the <owner>/<repository>
// directory holding its pointer file — once the real checkout it named is
// already gone. It never touches the checkout itself or its own parent
// directory, both of which are outside WB's ownership; only the registration
// this package created is retired here, exactly the counterpart of
// removeEmptyParent for a worktree that was never relocated under the task.
// Best-effort and idempotent: a name already gone (a prior interrupted
// attempt) is success, and a non-empty owner directory (a sibling repository
// still registered under it) is left in place.
func removeAdoptedRegistration(task *cleanupTaskHandle, owner, repository string) error {
	if err := task.validate(); err != nil {
		return err
	}
	ownerFD, err := unix.Openat(int(task.task.Fd()), owner, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open adopted worktree registration owner %s without following links: %w", owner, err)
	}
	ownerDirectory := os.NewFile(uintptr(ownerFD), "wb-adopted-registration-owner")
	defer func() { _ = ownerDirectory.Close() }()
	repositoryFD, err := unix.Openat(ownerFD, repository, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("open adopted worktree registration %s/%s without following links: %w", owner, repository, err)
	}
	if err == nil {
		repositoryDirectory := os.NewFile(uintptr(repositoryFD), "wb-adopted-registration")
		unlinkErr := unix.Unlinkat(repositoryFD, adoptedWorktreePointerName, 0)
		_ = repositoryDirectory.Close()
		if unlinkErr != nil && !errors.Is(unlinkErr, unix.ENOENT) {
			return fmt.Errorf("remove adopted worktree registration pointer for %s/%s: %w", owner, repository, unlinkErr)
		}
		if err := unix.Unlinkat(ownerFD, repository, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("remove adopted worktree registration directory for %s/%s: %w", owner, repository, err)
		}
	}
	// Best-effort, exactly like removeEmptyParent: retire the owner directory
	// once it is empty. ENOTEMPTY (a sibling repository still registered
	// under it) is not an error here.
	_ = unix.Unlinkat(int(task.task.Fd()), owner, unix.AT_REMOVEDIR)
	return nil
}

func writeCleanupReport(
	options CleanupOptions,
	generatedAt time.Time,
	phase string,
	results []CleanupResult,
	diagnostics []ListDiagnostic,
	artifacts []LifecycleArtifact,
	recovery *InterruptedLockRecovery,
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
		Recovery:     recovery,
	}
	if len(options.Tasks) > 1 {
		report.Tasks = append([]string(nil), options.Tasks...)
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
