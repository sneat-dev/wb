package diskprune

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	floorBuildCache       = 24 * time.Hour
	floorBuildCacheNoProc = 72 * time.Hour
)

// cacheFile is one build-cache entry file as the plan saw it.
type cacheFile struct {
	name     string
	modified time.Time
	identity Identity
	bytes    int64
}

// buildFloor is the trim's floor (disk-prune#req:go-build-cache): the larger
// of --older-than (default 24 h) and the age of the oldest running Go build
// tool plus an hour. Every entry a running build has looked up was refreshed
// to within an hour before its lookup, so none is old enough to delete.
// Without trustworthy process evidence the floor is a fixed 72 h.
func buildFloor(table Table, root string, olderThan time.Duration) (time.Duration, int) {
	if processEvidence(table, root) != "" {
		return floorBuildCacheNoProc, 0
	}
	floor, pid := maxDuration(floorBuildCache, olderThan), 0
	for _, process := range table.Processes {
		if process.IsGoBuildTool() && process.Age+time.Hour > floor {
			floor, pid = process.Age+time.Hour, process.PID
		}
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

// readShards lists every shard's entry files, oldest first within a shard,
// and the shards in order of their oldest entry.
func readShards(w *World, root string) (map[string][]cacheFile, []string, bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, true
	}
	shards := map[string][]cacheFile{}
	partial := false
	for _, entry := range entries {
		if !entry.IsDir() || !hexShard.MatchString(entry.Name()) {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			partial = true
			continue
		}
		for _, file := range files {
			if !cacheEntry.MatchString(file.Name()) {
				continue
			}
			path := filepath.Join(dir, file.Name())
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			identity, err := w.Identity(path)
			if err != nil {
				partial = true
				continue
			}
			shards[dir] = append(shards[dir], cacheFile{name: file.Name(), modified: info.ModTime(), identity: identity, bytes: diskBytes(info)})
		}
	}
	order := make([]string, 0, len(shards))
	for dir, files := range shards {
		sort.Slice(files, func(i, j int) bool { return files[i].modified.Before(files[j].modified) })
		order = append(order, dir)
	}
	sort.Slice(order, func(i, j int) bool {
		return shards[order[i]][0].modified.Before(shards[order[j]][0].modified)
	})
	return shards, order, partial
}

type buildCacheCategory struct{}

func (buildCacheCategory) name() string { return CategoryBuild }

func (buildCacheCategory) plan(ctx context.Context, w *World, o Options) *categoryPlan {
	plan := &categoryPlan{applicable: true, asShards: true, locks: []string{w.Go.GOCACHE}}
	if reason := unsupportedCache(w.Go); reason != "" {
		plan.skip(reason)
		return plan
	}
	if hit := insideAny(w.Go.GOCACHE, w.Protected); hit != "" {
		plan.skip("protected: " + hit)
		return plan
	}
	table := w.freshTable()
	floor, pid := buildFloor(table, w.Go.GOCACHE, o.OlderThan)
	plan.EffectiveFloor, plan.FloorSetByPID = floor.String(), pid
	shards, order, partial := readShards(w, w.Go.GOCACHE)
	plan.Partial = partial
	var estimate int64
	for _, dir := range order {
		eligible := olderThan(shards[dir], w.Now(), floor)
		if len(eligible) == 0 {
			continue
		}
		it := shardItem(w, dir, eligible, func(ctx context.Context) itemResult {
			current, _ := buildFloor(w.freshTable(), w.Go.GOCACHE, o.OlderThan)
			return trimShard(w, dir, eligible, current)
		})
		estimate += it.estimate
		plan.items = append(plan.items, it)
	}
	plan.EstimateBytes = &estimate
	return plan
}

func olderThan(files []cacheFile, now time.Time, floor time.Duration) []cacheFile {
	var out []cacheFile
	for _, file := range files {
		if now.Sub(file.modified) >= floor {
			out = append(out, file)
		}
	}
	return out
}

func shardItem(w *World, dir string, files []cacheFile, apply func(context.Context) itemResult) *item {
	var bytes int64
	for _, file := range files {
		bytes += file.bytes
	}
	return &item{
		ItemReport: ItemReport{Path: dir, Bytes: bytes, Status: StatusDelete, Reason: fmt.Sprintf("%d entries", len(files))},
		device:     files[0].identity.Dev, estimate: bytes, known: true, apply: apply,
	}
}

// trimShard removes the shard's eligible entries, oldest first, re-checking
// each against the floor recomputed from a fresh process table and against
// its recorded identity.
func trimShard(w *World, dir string, files []cacheFile, floor time.Duration) itemResult {
	var result itemResult
	for _, file := range files {
		info, err := os.Lstat(filepath.Join(dir, file.name))
		if err != nil || w.Now().Sub(info.ModTime()) < floor {
			continue
		}
		bytes, err := w.RemoveFile(dir, file.name, file.identity)
		if errors.Is(err, ErrChanged) {
			continue
		}
		if err != nil {
			result.err = err
			return result
		}
		result.unlinked += bytes
	}
	return result
}

// fullCleanCategory is --allow-full-clean: the trim again with no age floor,
// still only entry files, still identity-checked, and only while no Go tool
// runs. It never runs go clean, which would delete whatever a shard holds
// and honour a GOFLAGS that adds -modcache.
type fullCleanCategory struct{}

func (fullCleanCategory) name() string { return categoryFullClean }

func (fullCleanCategory) plan(ctx context.Context, w *World, o Options) *categoryPlan {
	plan := &categoryPlan{applicable: o.AllowFullClean && o.selected(CategoryBuild), asShards: true, locks: []string{w.Go.GOCACHE}}
	if !plan.applicable {
		plan.Status = StatusSkipped
		plan.Reason = "requires --allow-full-clean"
		return plan
	}
	switch {
	case o.OlderThan > 0:
		plan.skip("age-filter-unsupported")
		return plan
	case unsupportedCache(w.Go) != "":
		plan.skip(unsupportedCache(w.Go))
		return plan
	case insideAny(w.Go.GOCACHE, w.Protected) != "":
		plan.skip("protected: " + insideAny(w.Go.GOCACHE, w.Protected))
		return plan
	}
	table := w.freshTable()
	if reason := processEvidence(table, w.Go.GOCACHE); reason != "" {
		plan.skip(reason)
		return plan
	}
	if pids := goTools(table); len(pids) > 0 {
		plan.skip("requested-step-skipped: busy")
		plan.PIDs = pids
		return plan
	}
	floor, _ := buildFloor(table, w.Go.GOCACHE, 0)
	shards, order, partial := readShards(w, w.Go.GOCACHE)
	plan.Partial = partial
	var estimate int64
	for _, dir := range order {
		// What the trim already takes is not counted twice.
		var younger []cacheFile
		for _, file := range shards[dir] {
			if w.Now().Sub(file.modified) < floor {
				younger = append(younger, file)
			}
		}
		if len(younger) == 0 {
			continue
		}
		it := shardItem(w, dir, younger, func(ctx context.Context) itemResult {
			if pids := goTools(w.freshTable()); len(pids) > 0 {
				return itemResult{status: StatusSkipped, reason: "requested-step-skipped: busy", pids: pids}
			}
			return trimShard(w, dir, younger, 0)
		})
		estimate += it.estimate
		plan.items = append(plan.items, it)
	}
	plan.EstimateBytes = &estimate
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
