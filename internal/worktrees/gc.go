package worktrees

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/diskusage"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// GC classes. Every checkout the inventory can see falls into exactly one, and
// the class is decided by evidence rather than by how the checkout looks: a
// squash merge leaves no ancestry, so `git` reports every landed branch as
// unmerged and the human heuristic degrades to "looks unmerged, better keep
// it" — which is how one workstation accumulated 60 checkouts.
const (
	// GCClassDirty holds uncommitted changes. Never removable.
	GCClassDirty = "dirty"
	// GCClassClaimedLive is held by a live operation or a live claim.
	GCClassClaimedLive = "claimed-live"
	// GCClassOpenPR still has a pull request awaiting a decision.
	GCClassOpenPR = "open-pr"
	// GCClassContained has its head in the fetched target: ordinary merged work.
	GCClassContained = "contained"
	// GCClassLandedClean landed by receipt — squash, rebase, or absorbed into a
	// differently named integration branch — with nothing left over.
	GCClassLandedClean = "landed-clean"
	// GCClassLandedResidue landed, and holds local commits past the landed head.
	GCClassLandedResidue = "landed-residue"
	// GCClassDetachedReview is a detached checkout at a merged pull request's
	// head: what every review creates, and what nothing in WB could retire.
	GCClassDetachedReview = "detached-review"
	// GCClassDetachedUnknown is detached with no landing association.
	GCClassDetachedUnknown = "detached-unknown"
	// GCClassUnpushed holds a head GitHub has never seen. It is the only class
	// that can lose work, so no widening may ever retire it.
	GCClassUnpushed = "unpushed"
	// GCClassUnmerged is pushed, not landed, and has no open pull request.
	GCClassUnmerged = "unmerged"
)

// GCOptions controls one garbage-collection pass. Dry run is the default:
// Apply is the only thing that removes anything, and there is deliberately no
// force flag anywhere in this type. AllowResidue is the single widening, and it
// widens past evidence it prints first.
type GCOptions struct {
	ProjectsRoot string
	Tasks        []string
	Filter       string
	Base         string
	Apply        bool
	// AllowResidue retires a checkout whose work landed but which holds local
	// commits past the landed head, discarding exactly those commits.
	AllowResidue bool
	// SupersededBy is a path to an explicit trusted-reviewer supersession
	// receipt, for an intentionally split branch whose original head never
	// landed as one unit. It is the second of the two widenings the Feature
	// names, and like the first it skips no proof: the receipt must bind the
	// exact source and target heads, classify every residual, and carry a
	// trusted approving actor, all of which the cleanup transaction verifies.
	// It is named-task only and never participates in a fleet sweep.
	SupersededBy string
	// SkipDetached leaves detached checkouts out of the sweep entirely. The
	// default is to include them, because excluding them is the defect.
	SkipDetached bool
	// OlderThan is the merged-pull-request grace window. Zero disables it: gc
	// is evidence-based, and a landing is not more true an hour later.
	OlderThan time.Duration
	// TTL marks checkouts older than this expired in the report. Reporting only.
	TTL time.Duration
	// SessionFreshness is how recently an owning session must have touched a
	// checkout for its live process id to still mean "in use". Beyond it the
	// process id is presumed recycled and the checkout is classified on its own
	// evidence, with the stale owner named in a warning. Zero uses
	// DefaultSessionFreshness.
	SessionFreshness time.Duration
	// ResidueDepth bounds the commit-to-pull-request walk.
	ResidueDepth int
	Workers      int
	// SkipSizes omits the disk measurement. Sizes are measured only for
	// eligible checkouts, because the reclaim footer is the only thing that
	// needs them and walking every refused 1.4 GB node_modules to print a
	// number nobody can act on is exactly the redundant work verbs must not do.
	SkipSizes bool
	Now       func() time.Time
	// DeleteRemote additionally retires an unchanged source branch on origin.
	DeleteRemote bool
	// Progress is passed to the inventory walk.
	Progress func(ListProgress)
}

// GCEntry is one classified checkout with the evidence behind its class.
type GCEntry struct {
	Task          string `json:"task"`
	Repository    string `json:"repository"`
	WorktreeDir   string `json:"worktree_dir"`
	WorktreesRoot string `json:"worktrees_root"`
	Branch        string `json:"branch,omitempty"`
	Detached      bool   `json:"detached,omitempty"`
	// Management is managed, unmanaged, or unknown. It decides which command a
	// refusal can honestly name, and it is deliberately three-valued: a
	// checkout with no manifest is *unknown*, not unmanaged, because failing
	// open into a destructive suggestion on missing evidence is exactly the
	// mistake this field exists to prevent.
	Management        string           `json:"management"`
	HeadSHA           string           `json:"head_sha"`
	RemoteHeadSHA     string           `json:"remote_head_sha,omitempty"`
	Class             string           `json:"class"`
	Eligible          bool             `json:"eligible"`
	Applied           bool             `json:"applied"`
	Reason            string           `json:"reason,omitempty"`
	Evidence          []string         `json:"evidence,omitempty"`
	SanctionedCommand string           `json:"sanctioned_command,omitempty"`
	Owner             string           `json:"owner,omitempty"`
	OwnerState        string           `json:"owner_state,omitempty"`
	CreatedAt         time.Time        `json:"created_at,omitempty"`
	AgeSeconds        int64            `json:"age_seconds,omitempty"`
	TTLSeconds        int64            `json:"ttl_seconds,omitempty"`
	Expired           bool             `json:"expired,omitempty"`
	PullRequest       *PullRequest     `json:"pull_request,omitempty"`
	Landing           *LandingEvidence `json:"landing,omitempty"`
	// Warnings carry facts that used to be refusals. A branch renamed since its
	// claim is the one that mattered: refusing on a name check while the same
	// output admits landing evidence is commit-based asked an operator to
	// rename a branch that no longer exists on origin, purely as ceremony.
	Warnings []string        `json:"warnings,omitempty"`
	Size     diskusage.Usage `json:"size,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// GCOutcome is one whole pass, and the receipt for it.
type GCOutcome struct {
	SchemaVersion int              `json:"schema_version"`
	Apply         bool             `json:"apply"`
	Entries       []GCEntry        `json:"entries"`
	Purged        []PurgedArtefact `json:"purged,omitempty"`
	Diagnostics   []ListDiagnostic `json:"diagnostics,omitempty"`
	// Artifacts is WB's own control-plane residue that is not a checkout: a
	// non-empty quarantined stage, or the empty <task>/<owner>/<repository>
	// husk a removal outside WB leaves behind. Omitting it made gc blind to
	// exactly the debris its own advice creates.
	Artifacts []LifecycleArtifact `json:"artifacts,omitempty"`
	// Shells records the empty task shells this sweep found, planned in a dry
	// run and retired under --apply.
	Shells []RetiredShell `json:"shells,omitempty"`
	// Reclaimable and Reclaimed are always both figures. An apparent size
	// counts hard-linked bytes a deletion will not return; over one measured
	// sweep that was 11.7 GB apparent against 5.9 GB unshared.
	Reclaimable diskusage.Usage `json:"reclaimable"`
	Reclaimed   diskusage.Usage `json:"reclaimed"`
	// PartialTasks names tasks where some repositories retired and others did
	// not, with the repositories left behind. Blocking every repository because
	// one holds residue is correct for a merge and wrong for a cleanup.
	PartialTasks []GCPartialTask `json:"partial_tasks,omitempty"`
	Totals       map[string]int  `json:"totals"`
}

// GCPartialTask records a coordinated task that retired per repository.
type GCPartialTask struct {
	Task      string   `json:"task"`
	Retired   []string `json:"retired"`
	LeftAlone []string `json:"left_alone"`
}

// GC classifies every WB-managed checkout by evidence and, with Apply, retires
// the ones that are provably finished.
//
// It is the safety net rather than the primary mechanism: a rising count here
// means a landing verb stopped cleaning up after itself, and that is the thing
// to fix rather than to sweep. Removal is delegated to the existing cleanup
// transaction — one deletion path, one set of descriptor-anchored guards, one
// durable receipt — with this pass supplying only the classification and the
// per-repository scope.
func GC(ctx context.Context, options GCOptions) (GCOutcome, error) {
	if strings.TrimSpace(options.Base) == "" {
		options.Base = "main"
	}
	if options.Workers < 1 {
		options.Workers = DefaultInspectWorkers
	}
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	if strings.TrimSpace(options.SupersededBy) != "" && len(options.Tasks) != 1 {
		return GCOutcome{}, fmt.Errorf("--superseded-by names one trusted receipt for one task; supply exactly one task")
	}
	listed, err := ListWithDiagnostics(ctx, ListOptions{
		ProjectsRoot:    options.ProjectsRoot,
		Tasks:           options.Tasks,
		Base:            options.Base,
		Filter:          options.Filter,
		GitHub:          true,
		Workers:         options.Workers,
		Progress:        options.Progress,
		IncludeDetached: !options.SkipDetached,
		TTL:             options.TTL,
		ResidueEvidence: true,
		ResidueDepth:    options.ResidueDepth,
		Now:             options.Now,
	})
	if err != nil {
		return GCOutcome{}, err
	}
	outcome := GCOutcome{
		SchemaVersion: 1,
		Apply:         options.Apply,
		Entries:       make([]GCEntry, 0, len(listed.Results)),
		Purged:        listed.Purged,
		Diagnostics:   listed.Diagnostics,
		Artifacts:     listed.Artifacts,
		Totals:        map[string]int{},
	}
	// One accounting unit for the whole sweep. Summing per-worktree figures
	// double-counts every pnpm store file two worktrees both link to, and
	// under-reports the blocks that come back when both are removed.
	walk := diskusage.NewWalk()
	for index := range listed.Results {
		// The receipt is verified, never trusted: applySupersessionReceipt
		// re-reads the bound source and target heads and records a rejection
		// the classification below turns into a refusal.
		if err := applySupersessionReceipt(ctx, options.SupersededBy, &listed.Results[index]); err != nil {
			return GCOutcome{}, err
		}
	}
	for _, result := range listed.Results {
		entry := classifyForGC(result, options, now())
		if entry.Eligible && !options.SkipSizes {
			if usage, measureErr := walk.Measure(ctx, result.WorktreeDir); measureErr == nil {
				entry.Size = usage
			}
		}
		outcome.Entries = append(outcome.Entries, entry)
	}
	sort.Slice(outcome.Entries, func(i, j int) bool {
		if outcome.Entries[i].Task == outcome.Entries[j].Task {
			return outcome.Entries[i].WorktreeDir < outcome.Entries[j].WorktreeDir
		}
		return outcome.Entries[i].Task < outcome.Entries[j].Task
	})
	eligibleRoots := make([]string, 0, len(outcome.Entries))
	for index := range outcome.Entries {
		if !outcome.Entries[index].Eligible {
			continue
		}
		eligibleRoots = append(eligibleRoots, outcome.Entries[index].WorktreeDir)
	}
	if len(eligibleRoots) > 0 {
		outcome.Reclaimable = walk.Total(eligibleRoots...)
	}
	// The husk a removal leaves — an empty <task>/<owner>/<repository> namespace
	// with no checkout under it — is invisible to the inventory and would
	// otherwise accumulate one directory per sweep, including the sweeps gc's
	// own advice produces. It is reported in a dry run for the same reason
	// everything else is: a plan that omits work the apply will do is not a plan.
	if err := sweepTaskShells(ctx, options, &outcome); err != nil {
		return outcome, err
	}
	if options.Apply {
		if err := applyGC(ctx, options, &outcome); err != nil {
			return outcome, err
		}
		appliedRoots := make([]string, 0, len(outcome.Entries))
		for index := range outcome.Entries {
			if outcome.Entries[index].Applied {
				appliedRoots = append(appliedRoots, outcome.Entries[index].WorktreeDir)
			}
		}
		if len(appliedRoots) > 0 {
			outcome.Reclaimed = walk.Total(appliedRoots...)
		}
	}
	summarizeGC(&outcome)
	return outcome, nil
}

// classifyForGC decides one checkout's class from the evidence the inventory
// already gathered. It never contacts the network itself: every fact it reads
// was established once, during the walk.
func classifyForGC(result ListResult, options GCOptions, now time.Time) GCEntry {
	entry := GCEntry{
		Task: result.Task, Repository: result.Repository, WorktreeDir: result.WorktreeDir,
		WorktreesRoot: result.WorktreesRoot, Branch: result.Branch, Detached: result.Detached,
		HeadSHA: result.HeadSHA, RemoteHeadSHA: result.RemoteHeadSHA,
		Owner: result.Owner, OwnerState: result.OwnerState,
		CreatedAt: result.CreatedAt, AgeSeconds: result.AgeSeconds,
		TTLSeconds: result.TTLSeconds, Expired: result.Expired,
		Landing: result.Landing,
	}
	if result.OpenPullRequest != nil {
		entry.PullRequest = result.OpenPullRequest
	} else if result.MergedPullRequest != nil {
		entry.PullRequest = result.MergedPullRequest
	}
	if warning := renamedBranchWarning(result); warning != "" {
		entry.Warnings = append(entry.Warnings, warning)
	}
	if warning := staleOwnerWarning(result, options, now); warning != "" {
		entry.Warnings = append(entry.Warnings, warning)
	}
	entry.Management = worktreeManagement(result.WorktreeDir)
	switch {
	case !result.Clean:
		entry.Class = GCClassDirty
		entry.Reason, entry.SanctionedCommand, entry.Warnings =
			dirtyResolution(result, entry.Management, entry.Warnings)
	case result.Locked:
		entry.Class = GCClassClaimedLive
		entry.Reason = lockedReason(result, resumeInterruptedCommand(result.Task))
		entry.SanctionedCommand = resumeInterruptedCommand(result.Task)
	case sessionIsFresh(result, options, now):
		// Deliberately before every landed class: a checkout whose owning
		// session is alive is being used right now, and the fact that its work
		// already landed makes it more likely to be mid-next-round, not less.
		// The session ends its own worktree; a sweep must not do it underneath.
		//
		// "Alive" is a live PID *and* a recent registration. A PID alone is not
		// a heartbeat: process ids are recycled, and the first real sweep this
		// verb ran found ten finished review checkouts — 4 to 17 hours old —
		// every one of them reporting owner=active and therefore refusing.
		// A stale owner must not be able to pin a checkout forever.
		entry.Class = GCClassClaimedLive
		entry.Reason = "a live session (" + result.Owner + ") still holds this checkout, " +
			"last seen " + humanAge(sessionAgeSeconds(result, now)) + " ago"
		entry.SanctionedCommand = "wb worktree end " + result.Task
	case result.OpenPullRequest != nil:
		entry.Class = GCClassOpenPR
		entry.Reason = "pull request is still open: " + result.OpenPullRequest.URL
		entry.SanctionedCommand = "wb worktree merge " + result.Task + " --route auto"
	case result.SupersessionRejection != "":
		entry.Class = GCClassUnmerged
		entry.Reason = "trusted supersession receipt refused: " + result.SupersessionRejection
		entry.SanctionedCommand = "wb worktree gc " + result.Task + " --superseded-by <receipt.json>"
	case result.SupersededAtOrigin:
		entry.Class = GCClassLandedClean
		entry.Eligible = true
		entry.Reason = "retired on the trusted supersession receipt " + result.SupersessionReceipt +
			" approved by " + result.SupersessionReviewer
	case result.Detached && result.IntegratedAtOrigin:
		entry.Class = GCClassDetachedReview
		entry.Eligible = true
		entry.Reason = detachedReviewReason(result)
	case result.Detached:
		entry.Class = GCClassDetachedUnknown
		entry.Reason = detachedRefusal(result)
		// abort is the audited alternative to deleting an unfinished checkout:
		// it seals the Work Log and keeps a bounded private capture of the
		// bytes before anything is removed. It is named here rather than
		// rescue, which exists for the shared canonical clone and refuses a
		// linked worktree by design.
		entry.SanctionedCommand, entry.Warnings = detachedUnknownResolution(result, entry.Management, entry.Warnings)
	case result.IntegratedAtOrigin && result.AbsorbedAtOrigin:
		entry.Class = GCClassLandedClean
		entry.Eligible = true
		entry.Reason = "work landed at " + shortSHA(result.AbsorbedBySHA) + " by receipt"
	case result.IntegratedAtOrigin:
		entry.Class = GCClassContained
		entry.Eligible = true
		entry.Reason = "head is contained in the fetched origin target"
	case result.Landing != nil && result.Landing.Truncated:
		entry.Class = GCClassUnmerged
		entry.Reason = "the landing walk reached its --residue-depth bound without finding a landed ancestor, " +
			"so this checkout is unclassified rather than proved unlanded"
		entry.SanctionedCommand = "wb worktree gc " + result.Task + " --residue-depth " +
			fmt.Sprint(2*residueDepthOrDefault(options.ResidueDepth))
	case result.landedWithResidue():
		entry.Class = GCClassLandedResidue
		entry.Eligible = options.AllowResidue
		entry.Reason = result.residueReason()
		entry.SanctionedCommand = "wb worktree gc " + result.Task + " --allow-residue --apply"
	case result.HeadUnknownToRemote:
		entry.Class = GCClassUnpushed
		entry.Reason = "head " + shortSHA(result.HeadSHA) + " was never pushed; removing this checkout would lose it"
		entry.SanctionedCommand = "wb worktree merge " + result.Task + " --route auto"
	default:
		entry.Class = GCClassUnmerged
		entry.Reason = "head is not integrated into the exact origin target"
		entry.SanctionedCommand = "wb worktree merge " + result.Task + " --route auto"
	}
	if options.OlderThan > 0 && entry.Eligible && result.MergedPullRequest != nil &&
		result.MergedPullRequest.Merged != nil &&
		result.MergedPullRequest.Merged.Add(options.OlderThan).After(now) {
		entry.Eligible = false
		entry.Reason = "merged pull request is newer than the safety window"
		entry.SanctionedCommand = "wb worktree gc " + result.Task + " --older-than 0 --apply"
	}
	entry.Evidence = gcEvidence(result)
	return entry
}

// renamedBranchWarning records a live branch that no longer matches the name
// recorded when the task was claimed. It is a warning and never a refusal:
// landing evidence is commit-based, so a name mismatch cannot make landed work
// unlanded, and demanding the rename asks for a branch that origin no longer has.
func renamedBranchWarning(result ListResult) string {
	if result.Branch == "" {
		return ""
	}
	manifest, err := ReadManifest(result.WorktreeDir)
	if err != nil || strings.TrimSpace(manifest.Branch) == "" || manifest.Branch == result.Branch {
		return ""
	}
	return "live branch " + result.Branch + " does not match the recorded claim " + manifest.Branch +
		"; landing evidence is commit-based, so this is a note rather than a refusal"
}

func gcEvidence(result ListResult) []string {
	evidence := make([]string, 0, 6)
	evidence = append(evidence, "head="+shortSHA(result.HeadSHA))
	if result.RemoteTargetSHA != "" {
		evidence = append(evidence, "target="+shortSHA(result.RemoteTargetSHA))
	}
	if result.RemoteHeadSHA != "" {
		evidence = append(evidence, "origin/"+result.Branch+"="+shortSHA(result.RemoteHeadSHA))
	}
	if result.MergedPullRequest != nil {
		evidence = append(evidence, "merged-pr="+result.MergedPullRequest.URL)
	}
	if result.OpenPullRequest != nil {
		evidence = append(evidence, "open-pr="+result.OpenPullRequest.URL)
	}
	if result.RebaseMergedAtOrigin {
		evidence = append(evidence, "rebase-merged")
	}
	if result.AbsorbedAtOrigin {
		evidence = append(evidence, "absorbed-by="+shortSHA(result.AbsorbedBySHA))
	}
	if result.HeadUnknownToRemote {
		evidence = append(evidence, "head-never-pushed")
	}
	return evidence
}

// applyGC retires each eligible checkout through the existing cleanup
// transaction, scoped to one repository at a time so a coordinated task retires
// per repository and names what it left behind.
func applyGC(ctx context.Context, options GCOptions, outcome *GCOutcome) error {
	retired := map[string][]string{}
	left := map[string][]string{}
	sweepRoot, err := gcSweepReportDir(options)
	if err != nil {
		return err
	}
	for index := range outcome.Entries {
		entry := &outcome.Entries[index]
		if !entry.Eligible {
			// Every repository this pass did not retire is one the operator
			// must be told about, whatever kept it: a coordinated task that
			// retires two of three repositories has to name the third, and
			// keying that on the class silently dropped the ones held back by
			// the merge-grace window.
			left[entry.Task] = append(left[entry.Task], entry.Repository)
			continue
		}
		cleanupOutcome, err := Cleanup(ctx, CleanupOptions{
			ProjectsRoot:    options.ProjectsRoot,
			Tasks:           []string{entry.Task},
			ExactRepository: entry.Repository,
			Base:            options.Base,
			Apply:           true,
			AllowResidue:    options.AllowResidue,
			SupersededBy:    options.SupersededBy,
			IncludeDetached: !options.SkipDetached,
			OlderThan:       options.OlderThan,
			TTL:             options.TTL,
			ResidueDepth:    options.ResidueDepth,
			// A remote branch is only retired when it still points exactly at
			// this head. A landed branch carrying residue has a remote head at
			// the landing instead, and force-with-lease would refuse — correctly,
			// and after the local removal already happened.
			DeleteRemote: options.DeleteRemote && entry.RemoteHeadSHA != "" && entry.RemoteHeadSHA == entry.HeadSHA,
			Workers:      1,
			Now:          options.Now,
			// One sweep writes one receipt tree, with a directory per retired
			// checkout. Letting each delegated cleanup pick its own timestamped
			// default would make two repositories retired in the same instant
			// overwrite each other's audit record.
			ReportDir: filepath.Join(sweepRoot, entry.Task, strings.ReplaceAll(entry.Repository, "/", "-")),
		})
		if err != nil {
			entry.Error = err.Error()
			left[entry.Task] = append(left[entry.Task], entry.Repository)
			continue
		}
		for _, result := range cleanupOutcome.Results {
			if result.WorktreeDir == entry.WorktreeDir && result.Applied {
				entry.Applied = true
			}
		}
		if !entry.Applied {
			entry.Error = "cleanup did not retire this checkout; rerun with --format json for its plan"
			left[entry.Task] = append(left[entry.Task], entry.Repository)
			continue
		}
		retired[entry.Task] = append(retired[entry.Task], entry.Repository)
	}
	for task, repositories := range retired {
		if len(left[task]) == 0 {
			continue
		}
		sort.Strings(left[task])
		outcome.PartialTasks = append(outcome.PartialTasks, GCPartialTask{
			Task: task, Retired: repositories, LeftAlone: left[task],
		})
	}
	sort.Slice(outcome.PartialTasks, func(i, j int) bool { return outcome.PartialTasks[i].Task < outcome.PartialTasks[j].Task })
	return nil
}

// gcSweepReportDir is the one audit root for a whole sweep.
func gcSweepReportDir(options GCOptions) (string, error) {
	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		return "", err
	}
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	return filepath.Join(home, "reports", "worktree-gc", now().UTC().Format("20060102T150405.000000000Z")), nil
}

func summarizeGC(outcome *GCOutcome) {
	for _, entry := range outcome.Entries {
		outcome.Totals[entry.Class]++
		switch {
		case entry.Applied:
			outcome.Totals["retired"]++
		case entry.Eligible:
			outcome.Totals["eligible"]++
		default:
			outcome.Totals["refused"]++
		}
	}
	outcome.Totals["purged_artefacts"] = len(outcome.Purged)
	for _, shell := range outcome.Shells {
		if shell.Applied {
			outcome.Totals["retired_shells"]++
			continue
		}
		outcome.Totals["eligible_shells"]++
	}
	outcome.Totals["artifacts"] = len(outcome.Artifacts)
}

// Refused reports how many checkouts this pass declined to touch. It is the
// number that decides the exit code: nothing refused is exit 0.
func (outcome GCOutcome) Refused() int { return outcome.Totals["refused"] }

// String renders one entry as a single inventory row.
func (entry GCEntry) String() string {
	branch := entry.Branch
	if branch == "" {
		branch = "DETACHED " + shortSHA(entry.HeadSHA)
	}
	disposition := "keep"
	switch {
	case entry.Applied:
		disposition = "retired"
	case entry.Eligible:
		disposition = "would retire"
	}
	return fmt.Sprintf("%-14s %-28s %-34s %-16s %-12s owner=%s age=%s",
		entry.Task, entry.Repository, branch, entry.Class, disposition, entry.Owner, humanAge(entry.AgeSeconds))
}

func humanAge(seconds int64) string {
	switch {
	case seconds <= 0:
		return "-"
	case seconds < 3600:
		return fmt.Sprintf("%dm", seconds/60)
	case seconds < 86400:
		return fmt.Sprintf("%dh", seconds/3600)
	default:
		return fmt.Sprintf("%dd", seconds/86400)
	}
}

// detachedReviewReason states what decided the class. "Review checkout" is an
// inference about intent; containment is the fact, and a reader deciding
// whether to trust a removal needs the fact.
func detachedReviewReason(result ListResult) string {
	switch {
	case result.MergedPullRequest != nil:
		return "detached checkout at the head of merged pull request " + result.MergedPullRequest.URL
	case result.AbsorbedAtOrigin:
		return "detached checkout whose head landed at " + shortSHA(result.AbsorbedBySHA) + " by receipt"
	default:
		return "detached checkout whose head is contained in the fetched origin target"
	}
}

func residueDepthOrDefault(depth int) int {
	if depth <= 0 {
		return DefaultResidueDepth
	}
	return depth
}

// Management values.
const (
	// ManagementManaged: WB created this checkout and holds a Work Log for it.
	ManagementManaged = "managed"
	// ManagementUnmanaged: the checkout carries a WB manifest that does not
	// validate. Only a positively wrong marker earns this.
	ManagementUnmanaged = "unmanaged"
	// ManagementUnknown: no manifest at all. WB does not know what this is, and
	// not knowing must never widen what it is willing to suggest.
	ManagementUnknown = "unknown"
)

// worktreeManagement classifies a checkout by the immutable manifest WB writes
// once at creation. The three-valued answer is the point: absence of evidence
// is "unknown", and an unknown checkout gets the same care as one WB owns.
func worktreeManagement(worktree string) string {
	manifest, err := ReadManifest(worktree)
	switch {
	case errors.Is(err, errManifestNotFound):
		// Nothing there at all: WB has no claim about this checkout, and the
		// absence of a claim is not a claim that it is foreign.
		return ManagementUnknown
	case err != nil:
		// A manifest that exists and will not read is positively wrong — a
		// truncated write, a hand-edit, a replaced file — and that is a
		// different fact from its absence.
		return ManagementUnmanaged
	case strings.TrimSpace(manifest.Repository) == "" || strings.TrimSpace(manifest.Worktree) == "":
		return ManagementUnmanaged
	default:
		return ManagementManaged
	}
}

// dirtyResolution names the command that resolves a dirty checkout.
//
// For a WB-created one that is `wb worktree abort`, which seals the Work Log
// and captures the uncommitted bytes into a private archive before deleting
// anything. For every other one — WB never recorded it, or WB cannot tell —
// the named command MUST be the capture, and only the capture: the changes are
// uncommitted, so what it writes is their only copy. Naming a removal here,
// even alongside a capture, is how uncommitted work gets destroyed by someone
// doing exactly what the tool printed.
//
// Once the capture has run the checkout is clean, and the next sweep classifies
// it on its own evidence. That is the whole follow-up.
func dirtyResolution(result ListResult, management string, warnings []string) (reason, command string, updated []string) {
	if management == ManagementManaged {
		return "worktree has local changes", "wb worktree abort " + result.Task + " --apply", warnings
	}
	warnings = append(warnings,
		"these changes are uncommitted and WB holds no Work Log for this checkout, so the stash the command above "+
			"writes is their only copy; removing the checkout without it destroys them, and nothing in WB can bring "+
			"them back. Rerun gc once the tree is clean and it will classify the checkout on its own evidence")
	return "worktree has local changes and WB has no Work Log for it (" + management + "): capture them first",
		"git -C " + result.WorktreeDir + " stash push --include-untracked -m " + strconv.Quote("wb gc "+result.Task),
		warnings
}

// detachedUnknownResolution names the command for a detached checkout with no
// landing association. A WB-created one is sealed and discarded by abort; one
// WB never created has no Work Log to seal, and `wb worktree adopt` cannot
// reconstruct a manifest for a detached HEAD, so naming either would repeat the
// defect this rule exists to prevent. Until `wb worktree review` creates review
// checkouts as tracked, claimed ones — dependency-streams
// REQ reviews-use-a-tracked-review-checkout — the honest command is Git's own,
// named exactly, and deliberately without --force so Git itself refuses if the
// tree turns out to hold changes after all.
func detachedUnknownResolution(result ListResult, management string, warnings []string) (string, []string) {
	if management == ManagementManaged {
		return "wb worktree abort " + result.Task + " --disposition discarded --apply", warnings
	}
	warnings = append(warnings,
		"this checkout sits at a commit no branch points at, so removing it leaves that commit unreferenced; "+
			"if it holds work worth keeping, give it a branch first with git -C "+result.CanonicalDir+
			" branch <name> "+shortSHA(result.HeadSHA))
	if !result.Detached && result.Branch != "" {
		return "wb worktree adopt " + result.WorktreeDir, warnings
	}
	return "git -C " + result.CanonicalDir + " worktree remove " + result.WorktreeDir, warnings
}

// sweepTaskShells reports, and under --apply retires, the empty task
// namespaces a removal leaves behind. It is scoped to the same tasks the sweep
// itself selected: a caller acting on one named task must not have shells
// retired across the fleet on its behalf, and an error here is a finding rather
// than something to swallow.
func sweepTaskShells(ctx context.Context, options GCOptions, outcome *GCOutcome) error {
	shells, err := RetireTaskShells(ctx, RetireShellsOptions{
		ProjectsRoot: options.ProjectsRoot,
		Filter:       options.Filter,
		Tasks:        options.Tasks,
		Apply:        options.Apply,
	})
	if err != nil {
		return fmt.Errorf("sweep empty task shells: %w", err)
	}
	for _, shell := range shells.Results {
		if !shell.Eligible && shell.Error == "" {
			continue
		}
		outcome.Shells = append(outcome.Shells, shell)
	}
	return nil
}

// DefaultSessionFreshness is how long an owner registration keeps meaning "this
// session is using the checkout". It is deliberately generous relative to a
// lane's working rhythm and deliberately finite: an owner that has not touched
// a worktree within it cannot be distinguished from a recycled process id, and
// treating the two the same is how ten finished review checkouts pinned
// themselves open for seventeen hours.
const DefaultSessionFreshness = 90 * time.Minute

// sessionIsFresh reports whether an owning session is both alive and recent.
func sessionIsFresh(result ListResult, options GCOptions, now time.Time) bool {
	if result.OwnerState != "active" {
		return false
	}
	window := options.SessionFreshness
	if window <= 0 {
		window = DefaultSessionFreshness
	}
	seen := lastOwnerActivity(result)
	if seen.IsZero() {
		// No timestamp to judge by: keep the checkout. An unknown age is not a
		// licence to remove someone's work.
		return true
	}
	return now.Sub(seen) <= window
}

// lastOwnerActivity is when a live owner last recorded custody.
func lastOwnerActivity(result ListResult) time.Time {
	latest := time.Time{}
	for _, owner := range result.Owners {
		if owner.PIDStatus != "active" {
			continue
		}
		if owner.At.After(latest) {
			latest = owner.At
		}
	}
	return latest
}

func sessionAgeSeconds(result ListResult, now time.Time) int64 {
	seen := lastOwnerActivity(result)
	if seen.IsZero() {
		return 0
	}
	if age := now.Sub(seen); age > 0 {
		return int64(age / time.Second)
	}
	return 0
}

// staleOwnerWarning names an owner whose process is alive but which has not
// touched the checkout inside the freshness window, so a real session is never
// silently ignored.
func staleOwnerWarning(result ListResult, options GCOptions, now time.Time) string {
	if result.OwnerState != "active" || sessionIsFresh(result, options, now) {
		return ""
	}
	return "owner " + result.Owner + " has a live process id but has not touched this checkout for " +
		humanAge(sessionAgeSeconds(result, now)) + "; treating the process id as recycled rather than as a heartbeat"
}
