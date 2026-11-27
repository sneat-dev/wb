package diskprune

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// pnpmEnv is the environment pnpm runs under: corepack may neither download
// a pnpm nor prompt, so resolving a path can never write a new toolchain.
var pnpmEnv = []string{"COREPACK_ENABLE_NETWORK=0", "COREPACK_ENABLE_DOWNLOAD_PROMPT=0", "COREPACK_ENABLE_AUTO_PIN=0"}

type pnpmCategory struct{}

func (pnpmCategory) name() string { return CategoryPnpm }

// plan never runs pnpm: it has no faithful preview, so the estimate is
// unknown and the plan reports the store's single-link bytes for information.
func (pnpmCategory) plan(ctx context.Context, w *World, o Options) *categoryPlan {
	plan := &categoryPlan{applicable: true}
	var stores []string
	for _, store := range w.PnpmStores {
		if info, err := os.Stat(store); err == nil && info.IsDir() {
			stores = append(stores, store)
		}
	}
	if len(stores) == 0 {
		plan.skip("unavailable: no pnpm store")
		return plan
	}
	plan.locks = stores
	if o.OlderThan > 0 {
		plan.skip("age-filter-unsupported")
		return plan
	}
	table := w.freshTable()
	for _, store := range stores {
		if reason := processEvidence(table, store); reason != "" {
			plan.skip(reason)
			return plan
		}
	}
	var single int64
	identity, err := w.Identity(stores[0])
	if err != nil {
		plan.skip("partial-scan: " + err.Error())
		return plan
	}
	for _, store := range stores {
		scan := w.Scan(store, identity.MountID, nil)
		plan.Partial = plan.Partial || scan.Partial
		for _, inode := range scan.Inodes {
			if inode.links == 1 {
				single += inode.blocks
			}
		}
	}
	it := &item{
		root: stores[0],
		ItemReport: ItemReport{Path: stores[0], Bytes: single, Status: StatusDelete,
			Reason: "pnpm store prune; bytes shown are single-link store files, pnpm decides what goes; a worktree without node_modules downloads pruned packages again"},
		device: identity.Dev,
	}
	it.apply = func(ctx context.Context) itemResult { return applyPnpm(ctx, w, stores) }
	plan.items = []*item{it}
	return plan
}

// applyPnpm resolves what pnpm would prune, refuses anything outside the
// measured stores, a protected root or the target filesystems, then prunes
// and confirms pnpm still names the same store.
func applyPnpm(ctx context.Context, w *World, stores []string) itemResult {
	if result, busy := pnpmBusy(w, stores[0]); busy {
		return result
	}
	version, err := w.Pnpm(ctx, "--version")
	if err != nil {
		return itemResult{status: StatusSkipped, reason: "unavailable: " + err.Error()}
	}
	if major, err := strconv.Atoi(strings.SplitN(strings.TrimSpace(version), ".", 2)[0]); err != nil || major < 8 {
		return itemResult{status: StatusSkipped, reason: "unavailable: pnpm " + strings.TrimSpace(version) + " is older than 8"}
	}
	store, err := w.Pnpm(ctx, "store", "path")
	if err != nil {
		return itemResult{status: StatusSkipped, reason: "unavailable: " + err.Error()}
	}
	cacheDir, err := w.Pnpm(ctx, "config", "get", "cache-dir")
	if err != nil {
		return itemResult{status: StatusSkipped, reason: "unavailable: " + err.Error()}
	}
	store, cacheDir = resolved(strings.TrimSpace(store)), strings.TrimSpace(cacheDir)
	if cacheDir == "undefined" {
		cacheDir = ""
	}
	cacheDir = resolved(cacheDir)
	measured := false
	for _, root := range stores {
		if within(store, root) {
			measured = true
		}
	}
	switch {
	case !measured:
		return itemResult{status: StatusSkipped, reason: "store-not-measured: pnpm prunes " + store}
	case insideAny(store, w.Protected) != "":
		return itemResult{status: StatusSkipped, reason: "protected: " + insideAny(store, w.Protected)}
	case cacheDir != "" && insideAny(cacheDir, w.Protected) != "":
		return itemResult{status: StatusSkipped, reason: "protected: " + insideAny(cacheDir, w.Protected)}
	}
	writeSet := []string{store, filepath.Join(store, "tmp")}
	if cacheDir != "" {
		writeSet = append(writeSet, cacheDir)
	}
	for _, path := range writeSet {
		if !w.belowTarget(path) {
			return itemResult{status: StatusSkipped, reason: "write-set-outside-target: " + path}
		}
	}
	// A cache directory that does not exist yet holds nothing to remove and
	// cannot be locked; only existing ones are.
	var toLock []string
	for _, path := range writeSet[2:] {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			toLock = append(toLock, path)
		}
	}
	lock, err := w.LockDirs(toLock)
	if err != nil {
		return itemResult{status: StatusSkipped, reason: "locked: " + err.Error()}
	}
	defer lock.Release()
	// Checked again under the locks, immediately before pruning: an install
	// that started during the queries above must not be raced.
	if result, busy := pnpmBusy(w, store); busy {
		return result
	}
	if _, err := w.Pnpm(ctx, "store", "prune"); err != nil {
		return itemResult{err: fmt.Errorf("pnpm store prune: %w", err)}
	}
	after, err := w.Pnpm(ctx, "store", "path")
	if err != nil || strings.TrimSpace(after) != store {
		return itemResult{err: fmt.Errorf("pnpm store path changed from %s to %q during prune", store, strings.TrimSpace(after))}
	}
	return itemResult{}
}

// pnpmBusy reads a fresh process table: untrusted evidence or a running pnpm
// skips the prune.
func pnpmBusy(w *World, store string) (itemResult, bool) {
	table := w.freshTable()
	if reason := processEvidence(table, store); reason != "" {
		return itemResult{status: StatusSkipped, reason: reason}, true
	}
	var pids []PIDReport
	for _, process := range table.Processes {
		if process.IsPnpm() {
			pids = append(pids, PIDReport{PID: process.PID, Command: process.Command()})
		}
	}
	if len(pids) > 0 {
		return itemResult{status: StatusSkipped, reason: "busy", pids: pids}, true
	}
	return itemResult{}, false
}

// resolved is path with symlinks resolved when it exists, so it compares
// against the resolved protected roots.
func resolved(path string) string {
	if path == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}
