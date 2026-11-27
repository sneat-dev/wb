package diskprune

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// pruneBuildCache clears the Go build cache with go clean -cache, only while
// no Go process runs: a running build may be reading entries. Go's own
// automatic trim keeps handling the routine case; this is for a full disk.
func pruneBuildCache(ctx context.Context, env Env, o Options) CategoryReport {
	report := CategoryReport{Name: CategoryBuild}
	if env.GoCacheError != "" {
		report.Status, report.Reason = StatusFailed, env.GoCacheError
		return report
	}
	if env.GoCacheProblem != "" {
		report.Status, report.Reason = StatusSkipped, "unsupported: "+env.GoCacheProblem
		return report
	}
	shards, err := cacheShards(env.GoCache)
	if err != nil {
		report.Status, report.Reason = StatusFailed, "the cache cannot be inspected: "+err.Error()
		return report
	}
	if len(shards) == 0 {
		report.Status, report.Reason = StatusKeep, "nothing to clear"
		return report
	}
	if reason := goBusy(env); reason != "" {
		report.Status, report.Reason = StatusSkipped, reason
		return report
	}
	before := measureAll(ctx, env, shards)
	report.Status, report.Bytes = StatusDelete, before
	report.Items = []ItemReport{{Path: env.GoCache, Bytes: before, Status: StatusDelete}}
	if !o.Apply {
		return report
	}
	// Checked again immediately before clearing.
	if reason := goBusy(env); reason != "" {
		report.Status, report.Reason, report.Bytes, report.Items = StatusSkipped, reason, 0, nil
		return report
	}
	if err := env.GoClean(ctx); err != nil {
		report.Status, report.Items[0].Status, report.Items[0].Error = StatusFailed, StatusFailed, err.Error()
		return report
	}
	remaining, _ := cacheShards(env.GoCache)
	freed := max(before-measureAll(ctx, env, remaining), 0)
	report.Status, report.Bytes = StatusDeleted, freed
	report.Items[0].Status, report.Items[0].Bytes = StatusDeleted, freed
	return report
}

// goBusy is why the cache may not be cleared now, or "".
func goBusy(env Env) string {
	processes, err := env.GoProcesses()
	if err != nil {
		return "cannot tell whether Go is running: " + err.Error()
	}
	if len(processes) == 0 {
		return ""
	}
	names := make([]string, 0, len(processes))
	for _, process := range processes {
		names = append(names, fmt.Sprintf("%d %s", process.PID, process.Command))
	}
	return "Go is running (" + strings.Join(names, ", ") + ")"
}

// shardName is a Go build-cache shard: two lowercase hex digits.
var shardName = regexp.MustCompile(`^[0-9a-f]{2}$`)

// cacheShards are the shard directories holding entries: what go clean
// -cache removes. README and Go's bookkeeping files are not counted.
func cacheShards(cache string) ([]string, error) {
	entries, err := os.ReadDir(cache)
	if err != nil {
		return nil, err
	}
	var shards []string
	for _, entry := range entries {
		if !entry.IsDir() || !shardName.MatchString(entry.Name()) {
			continue
		}
		path := filepath.Join(cache, entry.Name())
		files, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		if len(files) > 0 {
			shards = append(shards, path)
		}
	}
	return shards, nil
}

func measureAll(ctx context.Context, env Env, paths []string) int64 {
	var total int64
	for _, path := range paths {
		total += env.Measure(ctx, path)
	}
	return total
}
