package diskprune

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	if !hasEntries(env.GoCache) {
		report.Status, report.Reason = StatusKeep, "nothing to clear"
		return report
	}
	if reason := goBusy(env); reason != "" {
		report.Status, report.Reason = StatusSkipped, reason
		return report
	}
	before := env.Measure(ctx, env.GoCache)
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
	freed := max(before-env.Measure(ctx, env.GoCache), 0)
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

// hasEntries reports whether the cache holds anything go clean -cache
// removes: a shard directory with a file in it.
func hasEntries(cache string) bool {
	shards, err := os.ReadDir(cache)
	if err != nil {
		return true
	}
	for _, shard := range shards {
		if !shard.IsDir() || len(shard.Name()) != 2 {
			continue
		}
		if files, err := os.ReadDir(filepath.Join(cache, shard.Name())); err != nil || len(files) > 0 {
			return true
		}
	}
	return false
}
