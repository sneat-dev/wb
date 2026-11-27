package diskprune

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/sneat-dev/wb/internal/disk"
)

// Category names, as --category takes them.
const (
	CategoryScratch   = "scratch"
	CategoryBuild     = "go-build-cache"
	CategoryPnpm      = "node-package-store"
	CategoryModules   = "go-module-cache"
	categoryFullClean = "go-build-cache-full-clean"
)

// SelectableCategories are the names --category accepts.
var SelectableCategories = []string{CategoryScratch, CategoryBuild, CategoryPnpm, CategoryModules}

// ReportOnlyCategories are measured by wb disk but never pruned.
var ReportOnlyCategories = []string{"wb-state", "legacy-wb-home", "worktrees"}

// Options is one invocation's selection.
type Options struct {
	// Categories restricts the run; empty means the default three.
	Categories []string
	Target     Target
	// OlderThan raises every age floor; zero means not given.
	OlderThan      time.Duration
	AllowFullClean bool
	Apply          bool
	// Auto is the daemon's unattended subset; it implies Apply.
	Auto bool
	// Floor is disk.minimum_available, for admission.
	Floor float64
}

func (o Options) explicit(name string) bool {
	for _, category := range o.Categories {
		if category == name {
			return true
		}
	}
	return false
}

// selected reports whether a category runs. Modules run only when named;
// the default run and --auto cover the other three.
func (o Options) selected(name string) bool {
	if o.Auto {
		return name == CategoryScratch || name == CategoryBuild || name == CategoryPnpm
	}
	if len(o.Categories) == 0 {
		return name != CategoryModules
	}
	return o.explicit(name)
}

func (o Options) mode() string {
	switch {
	case o.Auto:
		return "auto"
	case o.Apply:
		return "apply"
	default:
		return "plan"
	}
}

// Event is one progress observation (disk-prune#req:progress).
type Event struct {
	Phase    string
	Category string
	Unlinked int64
}

// World is everything prune touches outside its own logic, so every rule can
// be tested against a fake machine.
type World struct {
	Now          func() time.Time
	UID          int
	Home         string
	ProjectsRoot string
	StateDir     string
	LegacyHome   string
	TempDirs     []string
	// TempAliases maps a resolved temporary directory to the spelling the
	// environment used, when they differ, so processes naming either count.
	TempAliases map[string]string
	Go          GoEnv
	// Protected are roots no category may delete inside or around.
	Protected []string
	// ProtectedGap is non-empty when the protected inventory is incomplete.
	ProtectedGap string
	Processes    func() Table
	Probe        func(path string) (disk.Filesystem, error)
	Identity     func(path string) (Identity, error)
	Scan         func(root string, mount uint64, protected []string) scanResult
	LockDirs     func(paths []string) (*Lock, error)
	Admit        func(ctx context.Context) (status string, wait time.Duration, release func())
	RemoveTree   func(parent, name string, id Identity) (TreeOutcome, error)
	RemoveFile   func(dir, name string, id Identity) (int64, error)
	// Checkouts are every canonical clone and worktree whose sum files form
	// the module reference set; IgnoreSumFiles are checkout-relative paths
	// to skip (disk.prune.ignore_sum_files).
	Checkouts      []string
	IgnoreSumFiles []string
	// HTTP fetches a proxy URL for re-fetch proofs; only apply calls it.
	HTTP func(ctx context.Context, method, url string) (int, []byte, error)
	// LookupHost resolves the proxy host at apply, to refuse a loopback one.
	LookupHost func(ctx context.Context, host string) ([]string, error)
	// LockModuleVersion takes Go's own per-version lock without blocking.
	LockModuleVersion func(path string) (func(), error)
	// PnpmStores are the store directories measured for the plan.
	PnpmStores []string
	// Pnpm runs pnpm from / under pnpmEnv; only apply calls it.
	Pnpm     func(ctx context.Context, args ...string) (string, error)
	Progress func(Event)

	// belowTarget reports whether path's filesystem is still below its
	// target; set by Run before applying.
	belowTarget func(path string) bool
	// targetMet reports whether path's filesystem has reached the target,
	// or cannot be measured; set by Run before applying.
	targetMet func(path string) bool

	categories []category
	table      *Table
}

// table returns the process table, read once per category.
func (w *World) freshTable() Table {
	table := w.Processes()
	w.table = &table
	return table
}

func (w *World) progress(event Event) {
	if w.Progress != nil {
		w.Progress(event)
	}
}

// item is one deletion candidate or kept entry.
type item struct {
	ItemReport
	// root is the directory whose filesystem the item frees: its temporary
	// directory, GOCACHE, the store or GOMODCACHE.
	root     string
	device   uint64
	estimate int64
	known    bool
	apply    func(ctx context.Context) itemResult
}

type itemResult struct {
	unlinked int64
	status   string
	reason   string
	pids     []PIDReport
	err      error
}

// categoryPlan is a category's plan, filled by its planner and then by the
// engine's projection and apply passes.
type categoryPlan struct {
	CategoryReport
	items []*item
	// locks are the resource directories held while the category applies.
	locks []string
	// applicable is false for a category skipped, report-only or not
	// selected: its items are never applied.
	applicable bool
	// selected is whether the invocation asked for the category; only a
	// selected category's skips and failures shape the outcome.
	selected bool
	// shards, when set, reports a build-cache category per shard.
	shards func() []ShardReport
}

func (p *categoryPlan) skip(reason string) {
	p.Status = StatusSkipped
	p.Reason = reason
	p.applicable = false
}

// category plans one kind of bytes.
type category interface {
	name() string
	plan(ctx context.Context, w *World, o Options) *categoryPlan
}

type filesystemState struct {
	report    FilesystemReport
	fs        disk.Filesystem
	projected int64
	unknown   bool
	stopped   bool
	catStart  int64
	catAvail  int64
}

// Run plans, and with Apply executes, one prune.
func Run(ctx context.Context, w *World, o Options) Report {
	if o.Auto {
		o.Apply = true
		o.Target = Target{Kind: TargetRatio, Ratio: o.Floor}
	}
	report := Report{SchemaVersion: 1, Mode: o.mode(), Admission: AdmissionReport{Status: "not-needed"}}
	reasons := map[string]bool{}
	if o.Apply {
		lock, err := w.LockDirs([]string{w.Home})
		if err != nil {
			var locked *LockedError
			if errors.As(err, &locked) {
				report.Lock.HolderPID = locked.PID
				reasons[OutcomeLocked] = true
			} else {
				// flock unsupported, or a home that cannot be opened: nothing
				// may be deleted, and the reason is not another prune.
				report.Lock.Error = err.Error()
				reasons[OutcomeFailClosed] = true
			}
			return finish(report, reasons)
		}
		defer lock.Release()
		report.Lock.Held = true
	}

	w.progress(Event{Phase: "plan"})
	var plans []*categoryPlan
	for index, c := range w.categories {
		if c.name() == categoryFullClean && !o.AllowFullClean {
			continue
		}
		plan := c.plan(ctx, w, o)
		plan.Name = c.name()
		plan.Order = index + 1
		plan.selected = o.selected(plan.Name) || plan.Name == categoryFullClean
		if !o.selected(plan.Name) && plan.Name != categoryFullClean {
			if plan.Name == CategoryModules && plan.Status != StatusSkipped {
				plan.Status = StatusRequiresCategory
				plan.Reason = "requires --category go-module-cache"
			}
			plan.applicable = false
		}
		plans = append(plans, plan)
	}
	filesystems := map[uint64]*filesystemState{}
	project(w, o, plans, filesystems)

	w.belowTarget = func(path string) bool {
		fs, err := w.Probe(existingAncestor(path))
		return err == nil && !o.Target.Met(fs) && !o.Target.Unreachable(fs)
	}
	// targetMet lets a category that deletes many small files stop as soon
	// as its filesystem reaches the target; a probe that fails stops it too.
	w.targetMet = func(path string) bool {
		fs, err := w.Probe(path)
		return err != nil || o.Target.Met(fs)
	}
	if o.Apply && ctx.Err() == nil {
		admitted := admit(ctx, w, o, filesystems, &report)
		applyPlans(ctx, w, o, plans, filesystems, reasons)
		admitted()
	}
	for _, plan := range plans {
		if plan.selected {
			collectReasons(plan, reasons, o.Apply)
		}
		report.Categories = append(report.Categories, plan.render())
	}
	report.Filesystems = finalFilesystems(w, o, filesystems, reasons)
	report.State = []StateReport{}
	for _, state := range []struct {
		root   string
		legacy bool
	}{{w.StateDir, false}, {w.LegacyHome, true}} {
		if state.root == "" {
			continue
		}
		if entry, ok := stateReport(ctx, state.root, state.legacy); ok {
			report.State = append(report.State, entry)
		}
	}
	if ctx.Err() != nil {
		reasons[OutcomeCancelled] = true
	}
	return finish(report, reasons)
}

// project assigns each candidate its filesystem and, in pruning order, its
// projected free space; items past the point where the target is met are
// beyond-target. An item without a faithful estimate makes every later
// projection on its filesystem unknown, and never ends a run early.
func project(w *World, o Options, plans []*categoryPlan, filesystems map[uint64]*filesystemState) {
	for _, plan := range plans {
		if !plan.applicable {
			continue
		}
		for _, it := range plan.items {
			if it.Status != StatusDelete {
				continue
			}
			state := filesystemFor(w, o, it, it.root, filesystems)
			if state == nil {
				continue
			}
			it.Filesystem = state.report.Path
			if state.report.Note == "target-unreachable" {
				it.Status, it.Reason = StatusSkipped, "target-unreachable"
				continue
			}
			if !state.unknown && o.Target.Met(disk.Filesystem{TotalBytes: state.fs.TotalBytes, AvailableBytes: state.projected}) {
				it.Status = StatusBeyondTarget
				continue
			}
			if !it.known {
				state.unknown = true
				continue
			}
			state.report.EstimatedBytes += it.estimate
			if !state.unknown {
				state.projected += it.estimate
				projected := state.projected
				it.ProjectedAvailableBytes = &projected
			}
		}
	}
}

func filesystemFor(w *World, o Options, it *item, label string, filesystems map[uint64]*filesystemState) *filesystemState {
	if state, ok := filesystems[it.device]; ok {
		return state
	}
	if label == "" {
		label = it.Path
	}
	fs, err := w.Probe(label)
	if err != nil {
		it.Status, it.Error = StatusFailed, err.Error()
		return nil
	}
	state := &filesystemState{fs: fs, projected: fs.AvailableBytes, report: FilesystemReport{
		Device: strconv.FormatUint(it.device, 10), Path: label,
		Target: TargetReport{Kind: string(o.Target.Kind), Value: targetValue(o.Target)},
		Before: SpaceReport{TotalBytes: fs.TotalBytes, AvailableBytes: fs.AvailableBytes},
	}}
	if o.Target.Unreachable(fs) {
		state.report.Note = "target-unreachable"
	}
	filesystems[it.device] = state
	return state
}

func targetValue(t Target) float64 {
	switch t.Kind {
	case TargetBytes:
		return float64(t.Bytes)
	case TargetRatio:
		return t.Ratio
	}
	return 0
}

// admit goes through WB's admission queue unless a filesystem is already
// below the floor, where queueing behind the builds failing for lack of
// space would defeat the purpose.
func admit(ctx context.Context, w *World, o Options, filesystems map[uint64]*filesystemState, report *Report) func() {
	if len(filesystems) == 0 {
		return func() {}
	}
	for _, state := range filesystems {
		if state.fs.TotalBytes > 0 && state.fs.AvailableRatio() < o.Floor {
			report.Admission.Status = "bypassed"
			return func() {}
		}
	}
	status, wait, release := w.Admit(ctx)
	report.Admission = AdmissionReport{Status: status, WaitMS: wait.Milliseconds()}
	return release
}

const spaceNotReturnedThreshold = 256 << 20

func applyPlans(ctx context.Context, w *World, o Options, plans []*categoryPlan, filesystems map[uint64]*filesystemState, reasons map[string]bool) {
	for _, plan := range plans {
		if !plan.applicable || ctx.Err() != nil {
			continue
		}
		lock, err := w.LockDirs(plan.locks)
		if err != nil {
			reason := "lock-unavailable"
			if errors.Is(err, ErrLocked) {
				reason = "locked"
				reasons[OutcomeLocked] = true
			}
			plan.skip(reason + ": " + err.Error())
			for _, it := range plan.items {
				if it.Status == StatusDelete || it.Status == StatusBeyondTarget {
					it.Status, it.Reason = StatusSkipped, reason
				}
			}
			continue
		}
		w.progress(Event{Phase: "apply", Category: plan.Name})
		for _, state := range filesystems {
			state.catStart = state.report.UnlinkedBytes
			state.catAvail, _ = currentAvailable(w, state)
		}
		applied := false
		for _, it := range plan.items {
			// An item the plan placed beyond the target is reconsidered: an
			// earlier item may have been skipped at apply, leaving the
			// filesystem short.
			if (it.Status != StatusDelete && it.Status != StatusBeyondTarget) || it.apply == nil || ctx.Err() != nil {
				continue
			}
			// Every item still marked delete or beyond-target was given its
			// filesystem by project; a failed probe there marks it failed.
			state := filesystems[it.device]
			if state.stopped {
				it.Status, it.Reason = StatusSkipped, "space-not-returned"
				continue
			}
			current, probed := currentAvailable(w, state)
			if !probed {
				it.Status, it.Reason = StatusSkipped, "probe-failed"
				continue
			}
			if o.Target.Met(disk.Filesystem{TotalBytes: state.fs.TotalBytes, AvailableBytes: current}) {
				it.Status = StatusBeyondTarget
				continue
			}
			result := it.apply(ctx)
			applied = true
			applyResult(it, result)
			state.report.UnlinkedBytes += result.unlinked
			plan.UnlinkedBytes += result.unlinked
			w.progress(Event{Phase: "apply", Category: plan.Name, Unlinked: plan.UnlinkedBytes})
		}
		lock.Release()
		if applied {
			plan.Status = StatusApplied
		}
		for _, state := range filesystems {
			unlinked := state.report.UnlinkedBytes - state.catStart
			now, _ := currentAvailable(w, state)
			gain := now - state.catAvail
			if unlinked > spaceNotReturnedThreshold && gain < unlinked/10 {
				state.stopped = true
				state.report.Note = "space-not-returned"
				reasons[OutcomeSpaceNotReturned] = true
			}
		}
	}
}

// currentAvailable re-probes a filesystem; ok is false when it cannot be
// measured, which stops deletion on it.
func currentAvailable(w *World, state *filesystemState) (int64, bool) {
	fs, err := w.Probe(state.report.Path)
	if err != nil {
		return state.fs.AvailableBytes, false
	}
	return fs.AvailableBytes, true
}

// existingAncestor is path, or its nearest ancestor that exists, so a
// directory a tool has yet to create is judged by the filesystem it will be
// created on.
func existingAncestor(path string) string {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		if _, err := os.Stat(current); err == nil || current == filepath.Dir(current) {
			return current
		}
	}
}

func applyResult(it *item, result itemResult) {
	it.PIDs = result.pids
	if result.err != nil {
		// The plan's reason ("nothing references it") no longer describes
		// the item; the error does.
		it.Status, it.Reason, it.Error = StatusFailed, "", result.err.Error()
		return
	}
	if result.status != "" {
		it.Status, it.Reason = result.status, result.reason
		return
	}
	it.Status = StatusDeleted
}

// collectReasons turns statuses into outcome reasons: failures, fail-closed
// skips, and, in a plan, the existence of work.
func collectReasons(plan *categoryPlan, reasons map[string]bool, applied bool) {
	if failClosedReasons[reasonCode(plan.Reason)] {
		reasons[OutcomeFailClosed] = true
	}
	for _, it := range plan.items {
		switch {
		case it.Status == StatusFailed:
			reasons[OutcomeFailed] = true
		case failClosedReasons[reasonCode(it.Reason)]:
			reasons[OutcomeFailClosed] = true
		case it.Status == StatusDelete && plan.applicable && !applied:
			reasons[OutcomePlanned] = true
		}
	}
}

// reasonCode is the leading token of a reason, before any ": detail".
func reasonCode(reason string) string {
	for i := 0; i < len(reason); i++ {
		if reason[i] == ':' {
			return reason[:i]
		}
	}
	return reason
}

func finalFilesystems(w *World, o Options, filesystems map[uint64]*filesystemState, reasons map[string]bool) []FilesystemReport {
	var out []FilesystemReport
	for _, state := range filesystems {
		after := state.fs
		if o.Apply {
			if fs, err := w.Probe(state.report.Path); err == nil {
				after = fs
			}
		}
		state.report.After = SpaceReport{TotalBytes: after.TotalBytes, AvailableBytes: after.AvailableBytes}
		state.report.ObservedGainBytes = after.AvailableBytes - state.fs.AvailableBytes
		state.report.TargetMet = o.Target.Met(after)
		if state.report.Note == "target-unreachable" {
			reasons[OutcomeFailClosed] = true
		} else if o.Apply && o.Target.Kind != TargetMax && !state.report.TargetMet {
			reasons[OutcomeTargetUnmet] = true
		}
		out = append(out, state.report)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func finish(report Report, reasons map[string]bool) Report {
	report.Outcome = OutcomeDone
	report.Reasons = []string{}
	for _, outcome := range outcomePrecedence {
		if reasons[outcome] {
			if report.Outcome == OutcomeDone {
				report.Outcome = outcome
			}
			report.Reasons = append(report.Reasons, outcome)
		}
	}
	return report
}

// render converts the plan into its report, aggregating shards.
func (p *categoryPlan) render() CategoryReport {
	report := p.CategoryReport
	if report.Status == "" {
		report.Status = StatusPlanned
	}
	if p.shards != nil {
		report.Shards = p.shards()
		// A trim that never ran (skipped, beyond the target, failed before
		// starting) reports that status on its shards, not "delete".
		if len(p.items) == 1 && p.items[0].Status != StatusDelete && p.items[0].Status != StatusDeleted {
			for i := range report.Shards {
				if report.Shards[i].Status == StatusDelete {
					report.Shards[i].Status = p.items[0].Status
				}
			}
		}
		for _, it := range p.items {
			if it.Status == StatusFailed || it.Status == StatusSkipped {
				report.Items = append(report.Items, it.ItemReport)
			}
		}
		return report
	}
	for _, it := range p.items {
		report.Items = append(report.Items, it.ItemReport)
	}
	return report
}
