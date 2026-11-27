package diskprune

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	floorBuildCache       = 24 * time.Hour
	floorBuildCacheNoProc = 72 * time.Hour
	// recheckEvery is how many entries the trim deletes between fresh
	// process-table reads, floor recomputations and target probes.
	recheckEvery = 256
)

// cacheFile is one build-cache entry file as the plan saw it.
type cacheFile struct {
	// dir is the shard's path for reports; shard is its name beneath the
	// anchor, which removal walks.
	dir      string
	shard    string
	name     string
	modified time.Time
	identity Identity
	bytes    int64
}

// buildFloor is the trim's floor (disk-prune#req:go-build-cache): the larger
// of --older-than (default 24 h) and the age of the oldest running Go build
// tool plus an hour. Every entry a running build has looked up was refreshed
// to within an hour before its lookup, so none is old enough to delete.
// Without trustworthy process evidence, or when a build tool's age cannot be
// read, the floor is a fixed 72 h.
func buildFloor(table Table, root string, olderThan time.Duration) (time.Duration, int) {
	if processEvidence(table, root) != "" {
		return maxDuration(floorBuildCacheNoProc, olderThan), 0
	}
	floor, pid := maxDuration(floorBuildCache, olderThan), 0
	unknownPID := 0
	for _, process := range table.Processes {
		if !process.IsGoBuildTool() {
			continue
		}
		if process.Age == AgeUnknown {
			unknownPID = process.PID
			continue
		}
		if process.Age+time.Hour > floor {
			floor, pid = process.Age+time.Hour, process.PID
		}
	}
	// A build tool of unknown age raises the floor to at least 72 h; an
	// older known build still raises it further.
	if unknownPID != 0 && floor < floorBuildCacheNoProc {
		floor, pid = floorBuildCacheNoProc, unknownPID
	}
	return floor, pid
}

// unsupportedCache names a GOCACHE prune cannot reason about.
func unsupportedCache(env GoEnv) string {
	switch {
	case env.GOCACHEPROG != "":
		return "unsupported-cache: GOCACHEPROG is set, so entries are not files prune can age"
	case env.GOCACHE == "off" || env.GOCACHE == "" || !filepath.IsAbs(env.GOCACHE):
		return "unsupported-cache: GOCACHE is " + fmt.Sprintf("%q", env.GOCACHE)
	}
	if info, err := os.Stat(env.GOCACHE); err != nil || !info.IsDir() {
		return "unsupported-cache: GOCACHE " + env.GOCACHE + " is not a directory"
	}
	return ""
}

// readCacheFiles lists every shard's entry files, globally oldest first.
func readCacheFiles(w *World, root string) ([]cacheFile, Anchor, bool) {
	anchor, err := AnchorOf(root)
	if err != nil {
		return nil, Anchor{}, true
	}
	entries, err := os.ReadDir(anchor.Root)
	if err != nil {
		return nil, anchor, true
	}
	var files []cacheFile
	partial := false
	rootMount := anchor.ID.MountID
	for _, entry := range entries {
		if !entry.IsDir() || !hexShard.MatchString(entry.Name()) {
			continue
		}
		dir := filepath.Join(anchor.Root, entry.Name())
		// A shard that is a mount (say, a bind of a checkout), or that is a
		// protected root under another path (the directory a bind-mounted
		// checkout shows), is not the cache: none of its files are entries
		// prune may unlink.
		shardID, idErr := w.Identity(dir)
		if mount, err := mountIDOf(dir); err != nil || mount != rootMount || idErr != nil {
			partial = true
			continue
		}
		if _, hit := w.ProtectedIDs[[2]uint64{shardID.Dev, shardID.Ino}]; hit {
			partial = true
			continue
		}
		shard, err := os.ReadDir(dir)
		if err != nil {
			partial = true
			continue
		}
		for _, file := range shard {
			if !isCacheEntry(entry.Name(), file.Name()) {
				continue
			}
			path := filepath.Join(dir, file.Name())
			info, err := lstatEntry(path)
			if err != nil {
				// An entry that cannot be inspected is not known to be
				// absent: the enumeration is incomplete.
				partial = partial || !errors.Is(err, os.ErrNotExist)
				continue
			}
			if !info.Mode().IsRegular() {
				continue
			}
			identity, err := w.Identity(path)
			if err != nil {
				partial = true
				continue
			}
			files = append(files, cacheFile{dir: dir, shard: entry.Name(), name: file.Name(), modified: info.ModTime(), identity: identity, bytes: unlinkBytes(info)})
		}
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].modified.Before(files[j].modified) })
	return files, anchor, partial
}

// shardTracker aggregates a trim per shard for reports, so a six-gigabyte
// cache is a few hundred lines rather than hundreds of thousands.
type shardTracker struct {
	mu      sync.Mutex
	shards  map[string]*ShardReport
	deleted map[string]int64
	applied bool
}

func newShardTracker(files []cacheFile) *shardTracker {
	tracker := &shardTracker{shards: map[string]*ShardReport{}, deleted: map[string]int64{}}
	for _, file := range files {
		shard := tracker.shards[file.dir]
		if shard == nil {
			shard = &ShardReport{Path: file.dir}
			tracker.shards[file.dir] = shard
		}
		shard.Entries++
		shard.Bytes += file.bytes
	}
	return tracker
}

// reset re-counts the tracker over the entries apply found.
func (t *shardTracker) reset(files []cacheFile) {
	fresh := newShardTracker(files)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.shards, t.deleted = fresh.shards, fresh.deleted
}

func (t *shardTracker) remove(dir string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.deleted[dir]++
}

func (t *shardTracker) report() []ShardReport {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]ShardReport, 0, len(t.shards))
	for dir, shard := range t.shards {
		entry := *shard
		switch deleted := t.deleted[dir]; {
		case !t.applied:
			entry.Status = StatusDelete
		case deleted == entry.Entries:
			entry.Status = StatusDeleted
		case deleted > 0:
			entry.Status = StatusPartial
		default:
			entry.Status = StatusKeep
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// trimEntries removes files oldest first. Every recheckEvery entries it
// reads the process table afresh, recomputes the floor (or, for the full
// clean, refuses while any Go tool runs or evidence is untrusted) and stops
// once the filesystem reaches its target.
func trimEntries(ctx context.Context, w *World, root string, anchor Anchor, files []cacheFile, olderThan time.Duration, tracker *shardTracker, full bool) itemResult {
	tracker.mu.Lock()
	tracker.applied = true
	tracker.mu.Unlock()
	var result itemResult
	// cutoff is absolute: entries modified after it are kept. It is fixed
	// when the process table is read (the clock is read first, so ages
	// measured a moment later only move it earlier), never recomputed from a
	// later clock against an earlier age.
	var cutoff time.Time
	shard := ""
	for index, file := range files {
		if ctx.Err() != nil {
			return result
		}
		// The target is probed before every entry (a statfs), so the trim
		// stops at the first entry that suffices.
		met, measured := w.targetMet(root)
		if !measured {
			return itemResult{unlinked: result.unlinked, status: StatusSkipped, reason: "probe-failed"}
		}
		if met {
			return result
		}
		if index%recheckEvery == 0 || file.dir != shard {
			shard = file.dir
			read := w.Now()
			table := w.freshTable()
			if full {
				if reason := processEvidence(table, root); reason != "" {
					return itemResult{unlinked: result.unlinked, status: StatusSkipped, reason: reason}
				}
				if pids := goTools(table); len(pids) > 0 {
					return itemResult{unlinked: result.unlinked, status: StatusSkipped, reason: "requested-step-skipped: busy", pids: pids}
				}
			} else {
				floor, _ := buildFloor(table, root, olderThan)
				cutoff = read.Add(-floor)
			}
		}
		info, err := os.Lstat(filepath.Join(file.dir, file.name))
		if err != nil {
			// Gone (Go trimmed it) is fine; anything else leaves the trim's
			// evidence incomplete.
			result.partial = result.partial || !errors.Is(err, os.ErrNotExist)
			continue
		}
		if !full && info.ModTime().After(cutoff) {
			continue
		}
		bytes, err := w.RemoveFile(anchor, file.shard, file.name, file.identity)
		if errors.Is(err, ErrChanged) {
			continue
		}
		if err != nil {
			result.err = err
			return result
		}
		result.unlinked += bytes
		tracker.remove(file.dir)
	}
	return result
}

// notAfter is the files modified no later than the cutoff.
func notAfter(files []cacheFile, cutoff time.Time) []cacheFile {
	var out []cacheFile
	for _, file := range files {
		if !file.modified.After(cutoff) {
			out = append(out, file)
		}
	}
	return out
}

func sumBytes(files []cacheFile) int64 {
	var total int64
	for _, file := range files {
		total += file.bytes
	}
	return total
}

type buildCacheCategory struct{}

func (buildCacheCategory) name() string { return CategoryBuild }

func (buildCacheCategory) plan(ctx context.Context, w *World, o Options) *categoryPlan {
	root := w.Go.GOCACHE
	plan := &categoryPlan{applicable: true, locks: []string{root}}
	if reason := unsupportedCache(w.Go); reason != "" {
		plan.skip(reason)
		return plan
	}
	if hit := insideAny(root, w.Protected); hit != "" {
		plan.skip("protected: " + hit)
		return plan
	}
	if w.ProtectedGap != "" {
		// GOCACHE could lie inside a checkout the incomplete inventory missed.
		plan.skip("unknown: " + w.ProtectedGap)
		return plan
	}
	if reason := w.bindProblem(root); reason != "" {
		plan.skip(reason)
		return plan
	}
	read := w.Now()
	table := w.freshTable()
	floor, pid := buildFloor(table, root, o.OlderThan)
	plan.EffectiveFloor, plan.FloorSetByPID = floor.String(), pid
	files, anchor, partial := readCacheFiles(w, root)
	plan.Partial = partial
	eligible := notAfter(files, read.Add(-floor))
	tracker := newShardTracker(eligible)
	plan.shards = tracker.report
	estimate := sumBytes(eligible)
	plan.EstimateBytes = &estimate
	if len(eligible) == 0 {
		return plan
	}
	it := &item{
		ItemReport: ItemReport{Path: root, Bytes: estimate, Status: StatusDelete, Reason: fmt.Sprintf("%d entries older than %s, oldest first", len(eligible), floor)},
		root:       root, device: eligible[0].identity.Dev, estimate: estimate,
	}
	it.apply = func(ctx context.Context) itemResult {
		return trimEntries(ctx, w, root, anchor, eligible, o.OlderThan, tracker, false)
	}
	plan.items = []*item{it}
	return plan
}

// fullCleanCategory is --allow-full-clean: every entry file, still
// identity-checked, only while no Go tool runs. It never runs go clean,
// which would delete whatever a shard holds and honour a GOFLAGS that adds
// -modcache.
type fullCleanCategory struct{}

func (fullCleanCategory) name() string { return categoryFullClean }

func (fullCleanCategory) plan(ctx context.Context, w *World, o Options) *categoryPlan {
	root := w.Go.GOCACHE
	plan := &categoryPlan{applicable: o.AllowFullClean && o.selected(CategoryBuild), locks: []string{root}}
	if !plan.applicable {
		plan.Status = StatusSkipped
		plan.Reason = "requires --allow-full-clean"
		return plan
	}
	switch {
	case o.Auto:
		plan.skip("never under --auto")
		return plan
	case o.OlderThan > 0:
		plan.skip("requested-step-skipped: age-filter-unsupported")
		return plan
	case unsupportedCache(w.Go) != "":
		// Asked for explicitly, and impossible: a finding, with the reason.
		plan.skip("requested-step-skipped: " + unsupportedCache(w.Go))
		return plan
	case insideAny(root, w.Protected) != "":
		plan.skip("protected: " + insideAny(root, w.Protected))
		return plan
	case w.ProtectedGap != "":
		plan.skip("unknown: " + w.ProtectedGap)
		return plan
	case w.bindProblem(root) != "":
		plan.skip(w.bindProblem(root))
		return plan
	}
	table := w.freshTable()
	if reason := processEvidence(table, root); reason != "" {
		plan.skip(reason)
		return plan
	}
	if pids := goTools(table); len(pids) > 0 {
		plan.skip("requested-step-skipped: busy")
		plan.PIDs = pids
		return plan
	}
	floor, _ := buildFloor(table, root, 0)
	cutoff := w.Now().Add(-floor)
	files, anchor, partial := readCacheFiles(w, root)
	plan.Partial = partial
	// The estimate is what the trim leaves; apply re-reads the cache and
	// takes every entry that remains.
	var younger []cacheFile
	for _, file := range files {
		if file.modified.After(cutoff) {
			younger = append(younger, file)
		}
	}
	estimate := sumBytes(younger)
	plan.EstimateBytes = &estimate
	tracker := newShardTracker(younger)
	plan.shards = tracker.report
	if len(files) == 0 {
		return plan
	}
	it := &item{
		ItemReport: ItemReport{Path: root, Bytes: estimate, Status: StatusDelete, Reason: "every remaining entry, while no Go tool runs"},
		root:       root, device: files[0].identity.Dev, estimate: estimate,
	}
	it.apply = func(ctx context.Context) itemResult {
		// The fresh enumeration must be of the very cache the plan checked:
		// a GOCACHE path now leading to another directory (say, inside a
		// checkout) is refused before any candidate is taken from it.
		remaining, now, partial := readCacheFiles(w, root)
		if now.Root != anchor.Root || !anchor.ID.sameInode(now.ID.Dev, now.ID.Ino, now.ID.Birth) || now.ID.MountID != anchor.ID.MountID {
			return itemResult{status: StatusSkipped, reason: "changed: GOCACHE no longer names the directory the plan checked"}
		}
		tracker.reset(remaining)
		result := trimEntries(ctx, w, root, anchor, remaining, 0, tracker, true)
		result.partial = result.partial || partial
		return result
	}
	plan.items = []*item{it}
	return plan
}

func goTools(table Table) []PIDReport {
	var pids []PIDReport
	for _, process := range table.Processes {
		if process.IsGoTool() {
			pids = append(pids, PIDReport{PID: process.PID, Command: process.Command()})
		}
	}
	return pids
}

// lstatEntry inspects a cache entry; a seam, because an entry that cannot be
// inspected needs privileges a test does not have.
var lstatEntry = os.Lstat
