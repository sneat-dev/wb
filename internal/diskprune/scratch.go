package diskprune

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Scratch prefix classes (disk-prune#req:scratch). Every prefix WB code
// passes to os.MkdirTemp must be in exactly one; a test pins that.
var (
	// regenerablePrefixes are temporary directories WB creates and removes
	// itself; a leftover is a crash's debris.
	regenerablePrefixes = []string{
		"wb-coverage-baseline-", "wb-coverage-tiers-", "wb-go-test-shards-", "wb-index-",
		"wb-sync-report-validate-", "wb-test-cpu-queue-", "wb-worktree-merge-main-deadcode-",
		"wb-agent-preflight-", "wb-smoke-", "wb-deadcode-tool-", "wb-native-cover-",
	}
	// recoveryPrefixes may hold the only copy of rescued or archived work.
	recoveryPrefixes = []string{
		"wb-rescue-", "wb-retired-prepare-", "wb-retired-tag-metadata-", "wb-retirement-archive-",
		"wb-retirement-history-", "wb-canonical-sync-ignore-",
	}
	// runtimePrefixes belong to live daemons and running operations; a
	// listening socket shows in /proc only as socket:[inode], so process
	// references cannot see it.
	runtimePrefixes = []string{
		"wb-daemon-", "wb-daemon-cli-", "wb-hub-", "wb-sock-", "wb-supervisor-", "wb-shutdown-",
		"wb-peer-", "wb-peer-host-", "wb-poll-", "wb-host-stage-", "wb-pr-land-", "wb-e2e-",
		"wb-e2e-origin-", "wb-cwwt-", "wb-layout-test-home-", "wb-legacy-home-", "wb-nls-", "wb-rawpolicy-",
	}
	// checkoutPrefixes are Git checkouts.
	checkoutPrefixes = []string{"wb-wt-", "wb-orchestrate-git-seeds-"}
)

var (
	goBuildDir   = regexp.MustCompile(`^go-build[0-9]+$`)
	testTempDir  = regexp.MustCompile(`^(Test|Benchmark|Fuzz)[A-Za-z0-9_]*[0-9]+$`)
	numberedDir  = regexp.MustCompile(`^[0-9]+$`)
	agentGoCache = regexp.MustCompile(`^wb-.+-(go-)?cache$`)
)

const (
	floorGoProduced = 24 * time.Hour
	floorWB         = 72 * time.Hour
)

// scratchShape is what an entry's name and shape say it is.
type scratchShape struct {
	candidate bool
	floor     time.Duration
	keep      string // the kept class when not a candidate
}

// classifyScratch decides from name and shape alone; ownership, age and use
// are checked after.
func classifyScratch(path, name string) (scratchShape, bool) {
	switch {
	case goBuildDir.MatchString(name):
		return scratchShape{candidate: true, floor: floorGoProduced}, true
	case testTempDir.MatchString(name) && hasNumberedSubdirs(path):
		return scratchShape{candidate: true, floor: floorGoProduced}, true
	case name == "wb":
		return scratchShape{keep: "owner-unverifiable"}, true
	case !strings.HasPrefix(name, "wb-"):
		// A Test… name without the numbered subdirectories of t.TempDir.
		return scratchShape{keep: "not-a-known-producer"}, true
	}
	for _, class := range []struct {
		prefixes []string
		keep     string
	}{{recoveryPrefixes, "recovery-material"}, {runtimePrefixes, "runtime"}, {checkoutPrefixes, "checkout-in-scratch"}} {
		if longestPrefix(name, class.prefixes) != "" {
			return scratchShape{keep: class.keep}, true
		}
	}
	if prefix := longestPrefix(name, regenerablePrefixes); prefix != "" && numberedDir.MatchString(strings.TrimPrefix(name, prefix)) {
		return scratchShape{candidate: true, floor: floorWB}, true
	}
	if agentGoCache.MatchString(name) && isGoBuildCacheShape(path) {
		return scratchShape{candidate: true, floor: floorWB}, true
	}
	return scratchShape{keep: "not-a-known-producer"}, true
}

func longestPrefix(name string, prefixes []string) string {
	best := ""
	for _, prefix := range prefixes {
		if strings.HasPrefix(name, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	return best
}

func hasNumberedSubdirs(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) == 0 {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() || !numberedDir.MatchString(entry.Name()) {
			return false
		}
	}
	return true
}

var hexShard = regexp.MustCompile(`^[0-9a-f]{2}$`)

// isGoBuildCacheShape accepts exactly a Go build cache: Go's README, shard
// directories of entry files, and Go's bookkeeping files. A fuzz/ tree is
// accepted here and kept later as cache data.
func isGoBuildCacheShape(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	sawReadme := false
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case name == "README" && entry.Type().IsRegular():
			sawReadme = isGoCacheReadme(filepath.Join(path, name))
		case (name == "trim.txt" || name == "testexpire.txt") && entry.Type().IsRegular():
		case name == "fuzz" && entry.IsDir():
		case hexShard.MatchString(name) && entry.IsDir():
			if !onlyEntryFiles(filepath.Join(path, name)) {
				return false
			}
		default:
			return false
		}
	}
	return sawReadme
}

// cacheEntry is a Go build-cache entry file: a SHA-256 hex digest and -a
// (action) or -d (output); it lives in the shard named by its first two
// digits.
var cacheEntry = regexp.MustCompile(`^[0-9a-f]{64}-[ad]$`)

// isCacheEntry checks the name and that it sits in its own shard.
func isCacheEntry(shard, name string) bool {
	return cacheEntry.MatchString(name) && strings.HasPrefix(name, shard)
}

// goCacheReadme is how the README cmd/go writes into every build cache begins.
const goCacheReadme = "This directory holds cached build artifacts from the Go build system."

func isGoCacheReadme(path string) bool {
	raw, err := readSmallRegular(path, len(goCacheReadme))
	return err == nil && strings.HasPrefix(string(raw), goCacheReadme)
}

func onlyEntryFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !isCacheEntry(filepath.Base(dir), entry.Name()) {
			return false
		}
	}
	return true
}

// processEvidence is the category-level verdict on the process table.
func processEvidence(table Table, root string) string {
	switch {
	case table.Evidence.Unsupported:
		return "unsupported-platform"
	case table.Evidence.Unknown != "":
		return "unknown: " + table.Evidence.Unknown
	case table.Evidence.HidePID && accessibleToOthers(root):
		return "unknown: /proc hides other users' processes and " + root + " is reachable by them"
	}
	return ""
}

// accessibleToOthers reports whether any other user (by group or other
// permission bits, or an ACL) can reach path; hidden PIDs then hide them.
func accessibleToOthers(path string) bool {
	info, err := os.Stat(path)
	return err != nil || info.Mode().Perm()&0o077 != 0 || hasACL(path)
}

// references lists the processes using path, under any of its spellings
// (a temporary directory reached through a symlink is named both ways). An
// unreadable process counts when its name marks it as a tool that may use
// anything prune manages.
func references(table Table, path string, aliases ...string) (pids []PIDReport, unverified bool) {
	for _, process := range table.Processes {
		named := process.References(path)
		for _, alias := range aliases {
			named = named || (alias != "" && process.References(alias))
		}
		switch {
		case named:
			pids = append(pids, PIDReport{PID: process.PID, Command: process.Command()})
		case !process.Readable && (process.IsGoTool() || process.IsPnpm()):
			pids = append(pids, PIDReport{PID: process.PID, Command: process.Command()})
			unverified = true
		}
	}
	return pids, unverified
}

type scratchCategory struct{}

func (scratchCategory) name() string { return CategoryScratch }

func (scratchCategory) plan(ctx context.Context, w *World, o Options) *categoryPlan {
	plan := &categoryPlan{applicable: true, locks: append([]string{}, w.TempDirs...)}
	plan.EffectiveFloor = maxDuration(floorGoProduced, o.OlderThan).String()
	table := w.freshTable()
	for _, root := range w.TempDirs {
		if reason := processEvidence(table, root); reason != "" {
			plan.skip(reason)
			return plan
		}
	}
	if w.ProtectedGap != "" {
		plan.skip("unknown: " + w.ProtectedGap)
		return plan
	}
	if reason := w.bindProblem(w.TempDirs...); reason != "" {
		plan.skip(reason)
		return plan
	}
	accumulator := newReclaimAccumulator()
	var estimate int64
	for _, root := range w.TempDirs {
		entries, err := os.ReadDir(root)
		if err != nil {
			plan.Partial = true
			plan.items = append(plan.items, &item{ItemReport: ItemReport{Path: root, Status: StatusKeep, Reason: "partial-scan: " + err.Error()}})
			continue
		}
		for _, entry := range entries {
			if ctx.Err() != nil {
				return plan
			}
			it := planScratchEntry(w, o, table, root, entry.Name(), accumulator)
			if it == nil {
				continue
			}
			if it.Status == StatusDelete {
				estimate += it.estimate
			}
			plan.items = append(plan.items, it)
		}
	}
	plan.EstimateBytes = &estimate
	return plan
}

// planScratchEntry applies every scratch rule to one entry, in the order the
// spec gives them, and returns nil for an entry prune has no opinion about.
func planScratchEntry(w *World, o Options, table Table, root, name string, accumulator *reclaimAccumulator) *item {
	path := filepath.Join(root, name)
	// The name alone decides whether prune has an opinion; nothing inside
	// the entry is read until it is known to be this user's real directory,
	// so a hostile FIFO or symlink cannot hang or redirect the plan.
	if !scratchName(name) {
		return nil
	}
	it := &item{ItemReport: ItemReport{Path: path, Status: StatusKeep}, root: root}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Gone between the listing and now: routine for go-build*.
		return nil
	case err != nil:
		it.Reason = "partial-scan: " + err.Error()
		return it
	case !info.IsDir():
		it.Reason = "not-a-directory"
		return it
	case !ownedBy(info, w.UID):
		it.Reason = "foreign-owner"
		return it
	}
	shape, _ := classifyScratch(path, name)
	if !shape.candidate {
		it.Reason = shape.keep
		return it
	}
	identity, err := w.Identity(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		it.Reason = "partial-scan: " + err.Error()
		return it
	}
	// An entry that is itself a mount belongs to another filesystem: it is
	// never planned against the temporary directory's free space (nor
	// removed: removal refuses a mount too).
	if parent, err := w.Identity(root); err != nil || parent.MountID != identity.MountID {
		it.Reason = "contains-mount"
		return it
	}
	it.device = identity.Dev
	scan := w.Scan(path, identity.MountID, w.Protected)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		// It went away during the scan: routine for go-build*, and nothing
		// is left to decide about.
		return nil
	}
	if reason := scanVerdict(path, w.Protected, w.cacheRoots(), scan); reason != "" {
		it.Reason = reason
		return it
	}
	floor := maxDuration(shape.floor, o.OlderThan)
	if age := w.Now().Sub(scan.Newest); age < floor {
		it.Reason = "too-young: " + age.Round(time.Minute).String() + " < " + floor.String()
		return it
	}
	if pids, unverified := references(table, path, w.aliases(path)...); len(pids) > 0 {
		it.PIDs = pids
		it.Reason = "held-open"
		if unverified {
			it.Reason = "busy-unverified"
		}
		return it
	}
	it.Status = StatusDelete
	it.estimate = accumulator.add(scan.Inodes)
	it.Bytes = it.estimate
	it.Reason = "unused " + w.Now().Sub(scan.Newest).Round(time.Hour).String() + ", nothing references it"
	it.apply = func(ctx context.Context) itemResult {
		return applyScratch(ctx, w, floor, root, name, identity)
	}
	return it
}

// scanVerdict is the never-removed list.
func scanVerdict(path string, protected, caches []string, scan scanResult) string {
	switch {
	case insideAny(path, protected) != "":
		return "protected: " + insideAny(path, protected)
	case insideAny(path, caches) != "":
		// The resolved GOCACHE or GOMODCACHE, or a directory holding one:
		// scratch never deletes what another category manages.
		return "contains-cache-data: " + insideAny(path, caches)
	case scan.Protected != "":
		return "protected: " + scan.Protected
	case scan.Partial:
		return "partial-scan"
	case scan.ContainsMount:
		return "contains-mount"
	case scan.HasGit:
		return "checkout-in-scratch"
	case scan.HasSpecial:
		return "runtime"
	case scan.CacheData:
		return "contains-cache-data"
	}
	return ""
}

// applyScratch re-checks the entry immediately before removing it: a fresh
// process table, a fresh scan, then deletion bound to the checked identity.
func applyScratch(ctx context.Context, w *World, floor time.Duration, root, name string, identity Identity) itemResult {
	path := filepath.Join(root, name)
	// Every check below must be of the planned directory: its identity,
	// birth time included, is compared before the checks and again after
	// the scan, and removal compares it once more.
	if !w.stillIdentical(path, identity) {
		return itemResult{status: StatusSkipped, reason: "changed: not the directory the plan checked"}
	}
	// Still this user's directory, still the producer's shape: an entry that
	// changed owner, or a test directory that gained a non-numbered child,
	// no longer qualifies.
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || !ownedBy(info, w.UID) {
		return itemResult{status: StatusSkipped, reason: "changed"}
	}
	if shape, _ := classifyScratch(path, name); !shape.candidate {
		return itemResult{status: StatusSkipped, reason: "changed: " + shape.keep}
	}
	table := w.freshTable()
	if reason := processEvidence(table, root); reason != "" {
		return itemResult{status: StatusSkipped, reason: reason}
	}
	if pids, unverified := references(table, path, w.aliases(path)...); len(pids) > 0 {
		reason := "held-open"
		if unverified {
			reason = "busy-unverified"
		}
		return itemResult{status: StatusSkipped, reason: reason, pids: pids}
	}
	scan := w.Scan(path, identity.MountID, w.Protected)
	if reason := scanVerdict(path, w.Protected, w.cacheRoots(), scan); reason != "" {
		return itemResult{status: StatusSkipped, reason: reason}
	}
	if w.Now().Sub(scan.Newest) < floor {
		return itemResult{status: StatusSkipped, reason: "too-young"}
	}
	if !w.stillIdentical(path, identity) {
		return itemResult{status: StatusSkipped, reason: "changed: not the directory the plan checked"}
	}
	if ctx.Err() != nil {
		return itemResult{status: StatusSkipped, reason: "cancelled"}
	}
	outcome, err := w.RemoveTree(root, name, identity)
	return treeResult(outcome, err)
}

// treeResult maps a tree removal's sentinel errors to statuses.
func treeResult(outcome TreeOutcome, err error) itemResult {
	result := itemResult{unlinked: outcome.Bytes}
	switch {
	case err == nil:
	case errors.Is(err, ErrReplaced):
		result.status, result.reason = StatusReplaced, "replaced"
	case errors.Is(err, ErrChanged):
		result.status, result.reason = StatusSkipped, "changed"
	case errors.Is(err, ErrContainsMount):
		result.status, result.reason = StatusSkipped, "contains-mount"
	case errors.Is(err, ErrMoved):
		result.status, result.reason = StatusSkipped, "moved"
	case errors.Is(err, ErrUnsupported):
		result.status, result.reason = StatusSkipped, "unsupported-platform"
	default:
		result.err = err
	}
	return result
}

func maxDuration(a, b time.Duration) time.Duration {
	if b > a {
		return b
	}
	return a
}

// cacheRoots are the resolved caches other categories manage.
func (w *World) cacheRoots() []string {
	return []string{w.Go.GOCACHE, w.Go.GOMODCACHE}
}

// aliases are path under every unresolved spelling of its temporary
// directory; none when only the resolved spelling is in use.
func (w *World) aliases(path string) []string {
	var out []string
	for resolved, spellings := range w.TempAliases {
		if within(path, resolved) {
			for _, spelling := range spellings {
				out = append(out, spelling+strings.TrimPrefix(path, resolved))
			}
		}
	}
	return out
}

// scratchName reports whether a temporary-directory entry's name is one prune
// has an opinion about, without reading anything.
func scratchName(name string) bool {
	return goBuildDir.MatchString(name) || testTempDir.MatchString(name) || name == "wb" || strings.HasPrefix(name, "wb-")
}

// stillIdentical reports whether path still names the object identity
// recorded, birth time and mount included.
func (w *World) stillIdentical(path string, identity Identity) bool {
	now, err := w.Identity(path)
	return err == nil && identity.sameInode(now.Dev, now.Ino, now.Birth) && now.MountID == identity.MountID
}
