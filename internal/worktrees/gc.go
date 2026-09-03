package worktrees

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
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
	// SkipDetached leaves detached checkouts out of the sweep entirely. The
	// default is to include them, because excluding them is the defect.
	SkipDetached bool
	// OlderThan is the merged-pull-request grace window. Zero disables it: gc
	// is evidence-based, and a landing is not more true an hour later.
	OlderThan time.Duration
	// TTL marks checkouts older than this expired in the report. Reporting only.
	TTL time.Duration
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
	Task              string           `json:"task"`
	Repository        string           `json:"repository"`
	WorktreeDir       string           `json:"worktree_dir"`
	WorktreesRoot     string           `json:"worktrees_root"`
	Branch            string           `json:"branch,omitempty"`
	Detached          bool             `json:"detached,omitempty"`
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
		Totals:        map[string]int{},
	}
	for _, result := range listed.Results {
		entry := classifyForGC(result, options, now())
		if entry.Eligible && !options.SkipSizes {
			if usage, measureErr := diskusage.Measure(ctx, result.WorktreeDir); measureErr == nil {
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
	for index := range outcome.Entries {
		if !outcome.Entries[index].Eligible {
			continue
		}
		outcome.Reclaimable = outcome.Reclaimable.Add(outcome.Entries[index].Size)
	}
	if options.Apply {
		if err := applyGC(ctx, options, &outcome); err != nil {
			return outcome, err
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
	switch {
	case !result.Clean:
		entry.Class = GCClassDirty
		entry.Reason = "worktree has local changes"
		entry.SanctionedCommand = "wb worktree abort " + result.Task + " --apply"
	case result.Locked:
		entry.Class = GCClassClaimedLive
		entry.Reason = lockedReason(result, resumeInterruptedCommand(result.Task))
		entry.SanctionedCommand = resumeInterruptedCommand(result.Task)
	case result.OwnerState == "active" && !result.IntegratedAtOrigin:
		entry.Class = GCClassClaimedLive
		entry.Reason = "a live session (" + result.Owner + ") still holds this checkout"
		entry.SanctionedCommand = "wb worktree end " + result.Task
	case result.OpenPullRequest != nil:
		entry.Class = GCClassOpenPR
		entry.Reason = "pull request is still open: " + result.OpenPullRequest.URL
		entry.SanctionedCommand = "wb worktree merge " + result.Task + " --route auto"
	case result.Detached && result.IntegratedAtOrigin:
		entry.Class = GCClassDetachedReview
		entry.Eligible = true
		entry.Reason = "detached review checkout of a landed pull request"
	case result.Detached:
		entry.Class = GCClassDetachedUnknown
		entry.Reason = detachedRefusal(result)
		entry.SanctionedCommand = "wb worktree rescue " + result.WorktreeDir
	case result.IntegratedAtOrigin && result.AbsorbedAtOrigin:
		entry.Class = GCClassLandedClean
		entry.Eligible = true
		entry.Reason = "work landed at " + shortSHA(result.AbsorbedBySHA) + " by receipt"
	case result.IntegratedAtOrigin:
		entry.Class = GCClassContained
		entry.Eligible = true
		entry.Reason = "head is contained in the fetched origin target"
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
			if entry.Class != GCClassContained && entry.Class != GCClassLandedClean {
				left[entry.Task] = append(left[entry.Task], entry.Repository)
			}
			continue
		}
		cleanupOutcome, err := Cleanup(ctx, CleanupOptions{
			ProjectsRoot:    options.ProjectsRoot,
			Tasks:           []string{entry.Task},
			ExactRepository: entry.Repository,
			Base:            options.Base,
			Apply:           true,
			AllowResidue:    options.AllowResidue,
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
		outcome.Reclaimed = outcome.Reclaimed.Add(entry.Size)
	}
	for task, repositories := range retired {
		if len(left[task]) == 0 {
			continue
		}
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
