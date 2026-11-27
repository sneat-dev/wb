package diskprune

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
	"golang.org/x/mod/sumdb/dirhash"
)

const floorModules = 7 * 24 * time.Hour

// probeBudget bounds the total time apply spends proving re-fetchability.
const probeBudget = 120 * time.Second

// sumLine is one go.sum line: <module> <version>[/go.mod] h1:<hash>.
var sumLine = regexp.MustCompile(`^(\S+) (\S+?)(/go\.mod)? h1:[A-Za-z0-9+/]+=*$`)

// alwaysKeptModules are selected by directives or special-cased by Go, not by
// sum files, so the reference set cannot vouch for them.
var alwaysKeptModules = map[string]bool{"golang.org/toolchain": true, "golang.org/fips140": true}

// referenceSet reads every sum file below every checkout. Any directory or
// file it cannot read or parse makes the whole set incomplete: prune then
// deletes no module at all.
func referenceSet(checkouts, ignore []string) (map[string]bool, []string, error) {
	refs := map[string]bool{}
	var ignored []string
	for _, original := range checkouts {
		// WalkDir does not follow a symlinked root; a checkout reached through
		// one would otherwise contribute no references at all.
		checkout, err := filepath.EvalSymlinks(original)
		if err != nil {
			return nil, ignored, fmt.Errorf("%s: %w", original, err)
		}
		if info, err := os.Stat(checkout); err != nil || !info.IsDir() {
			return nil, ignored, fmt.Errorf("%s is not a directory", original)
		}
		err = filepath.WalkDir(checkout, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			if entry.IsDir() {
				if path != checkout && (entry.Name() == "node_modules" || entry.Name() == ".git") {
					return fs.SkipDir
				}
				return nil
			}
			sum, err := isSumFile(path, entry.Name())
			if err != nil {
				return err
			}
			if !sum {
				return nil
			}
			rel, _ := filepath.Rel(checkout, path)
			for _, skip := range ignore {
				if rel == skip {
					ignored = append(ignored, path)
					return nil
				}
			}
			return readSumFile(path, refs)
		})
		if err != nil {
			return nil, ignored, err
		}
	}
	return refs, ignored, nil
}

// isSumFile accepts go.sum, go.work.sum, and <name>.sum beside <name>.mod,
// which is what -modfile=<name>.mod reads.
func isSumFile(path, name string) (bool, error) {
	if name == "go.sum" || name == "go.work.sum" {
		return true, nil
	}
	base, ok := strings.CutSuffix(name, ".sum")
	if !ok {
		return false, nil
	}
	_, err := os.Stat(filepath.Join(filepath.Dir(path), base+".mod"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return true, nil
}

func readSumFile(path string, refs map[string]bool) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		match := sumLine.FindStringSubmatch(text)
		if match == nil {
			return fmt.Errorf("%s:%d: not a go.sum line", path, line)
		}
		refs[match[1]+"@"+match[2]] = true
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// fileState is a download file as the plan saw it: an in-place rewrite keeps
// the inode but not the size and times, so all are compared.
type fileState struct {
	Identity
	size   int64
	mtime  time.Time
	ctime  time.Time
	links  uint64
	blocks int64
}

func stateOf(identity Identity, info os.FileInfo) fileState {
	return fileState{Identity: identity, size: info.Size(), mtime: info.ModTime(), ctime: changeTime(info), links: linkCount(info), blocks: diskBytes(info)}
}

func (a fileState) same(b fileState) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.size == b.size && a.mtime.Equal(b.mtime) && a.ctime.Equal(b.ctime)
}

// downloadExts are the download files apply removes; anything else beside
// them makes a version unknown, never silently counted or left.
var downloadExts = map[string]bool{".info": true, ".mod": true, ".zip": true, ".ziphash": true}

// moduleVersion is one version in the module cache.
type moduleVersion struct {
	path, version         string
	escPath, escVersion   string
	downloadDir           string
	extracted             string
	newest                time.Time
	files                 map[string]fileState // download files by extension
	hasZip, hasExtraction bool
	// extractedID and ziphash are what the plan checked, so apply deletes
	// only that tree and only while its recorded hash is unchanged.
	extractedID Identity
	ziphash     string
	// foreign is a file beside the version that Go does not write there.
	foreign bool
}

func (v moduleVersion) key() string { return v.path + "@" + v.version }

// readModuleCache lists the cache's versions from cache/download. A path or
// version that does not unescape is skipped: it is not a version Go wrote.
func readModuleCache(w *World, root string) ([]moduleVersion, error) {
	download := filepath.Join(root, "cache", "download")
	rootMount, err := mountIDOf(root)
	if err != nil {
		return nil, err
	}
	// The walk must stay inside GOMODCACHE on its own mount: a symlinked
	// cache/ or download/, or an @v directory on another mount, could make
	// prune unlink files elsewhere.
	for _, dir := range []string{filepath.Join(root, "cache"), download} {
		info, err := os.Lstat(dir)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%s is not a real directory", dir)
		}
	}
	versions := map[string]*moduleVersion{}
	var strays []string
	err = filepath.WalkDir(download, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == filepath.Join(download, "sumdb") {
				return fs.SkipDir
			}
			mount, err := mountIDOf(path)
			if err != nil {
				return err
			}
			if mount != rootMount {
				return fmt.Errorf("%s: %w", path, ErrContainsMount)
			}
			return nil
		}
		dir := filepath.Dir(path)
		if filepath.Base(dir) != "@v" {
			return nil
		}
		name := entry.Name()
		ext := filepath.Ext(name)
		escVersion := strings.TrimSuffix(name, ext)
		if name == "list" || ext == ".lock" || ext == ".partial" || escVersion == "" {
			return nil
		}
		escPath, _ := filepath.Rel(download, filepath.Dir(dir))
		escPath = filepath.ToSlash(escPath)
		modPath, errPath := module.UnescapePath(escPath)
		version, errVersion := module.UnescapeVersion(escVersion)
		if errPath != nil || errVersion != nil || !semver.IsValid(version) {
			// Not a version Go wrote (v1.0.0.zip.bak): it is attached to the
			// version whose name it extends, making that version unknown.
			strays = append(strays, path)
			return nil
		}
		identity, err := w.Identity(path)
		info, infoErr := entry.Info()
		if err == nil {
			err = infoErr
		}
		if err != nil {
			return err
		}
		key := modPath + "@" + version
		v := versions[key]
		if v == nil {
			v = &moduleVersion{path: modPath, version: version, escPath: escPath, escVersion: escVersion,
				downloadDir: dir, extracted: filepath.Join(root, filepath.FromSlash(escPath)+"@"+escVersion), files: map[string]fileState{}}
			versions[key] = v
		}
		if !downloadExts[ext] || !info.Mode().IsRegular() {
			v.foreign = true
		}
		v.files[ext] = stateOf(identity, info)
		v.newest = latest(v.newest, changeTime(info))
		v.hasZip = v.hasZip || ext == ".zip"
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, stray := range strays {
		for _, v := range versions {
			if filepath.Dir(stray) == v.downloadDir && strings.HasPrefix(filepath.Base(stray), v.escVersion+".") {
				v.foreign = true
			}
		}
	}
	out := make([]moduleVersion, 0, len(versions))
	for _, v := range versions {
		if info, err := os.Lstat(v.extracted); err == nil && info.IsDir() {
			v.hasExtraction = true
			identity, err := w.Identity(v.extracted)
			if err != nil {
				return nil, err
			}
			v.extractedID = identity
		}
		if raw, err := os.ReadFile(filepath.Join(v.downloadDir, v.escVersion+".ziphash")); err == nil {
			v.ziphash = strings.TrimSpace(string(raw))
		}
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].newest.Before(out[j].newest) })
	return out, nil
}

// refetchProblem says why Go's configuration cannot re-download the module,
// or "" when it can: an independent HTTPS proxy, verification on, the module
// not private and not vendored.
func refetchProblem(env GoEnv, modulePath string) string {
	proxy := firstProxy(env.GOPROXY)
	switch {
	case proxy == "":
		return "not-refetchable: GOPROXY names no proxy"
	case env.GOSUMDB == "off":
		return "not-refetchable: GOSUMDB=off, so a wrong re-download would be trusted"
	case strings.Contains(env.GOFLAGS, "-mod=vendor"):
		return "not-refetchable: GOFLAGS uses -mod=vendor"
	case module.MatchPrefixPatterns(env.GOPRIVATE, modulePath) || module.MatchPrefixPatterns(env.GONOPROXY, modulePath) || module.MatchPrefixPatterns(env.GONOSUMDB, modulePath):
		return "not-refetchable: private module"
	}
	parsed, err := url.Parse(proxy)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return "not-refetchable: proxy " + proxy + " is not https"
	}
	if host := strings.TrimSuffix(parsed.Hostname(), "."); host == "localhost" || strings.HasSuffix(host, ".localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
		return "not-refetchable: proxy " + proxy + " is loopback and may serve this cache"
	}
	return ""
}

// firstProxy is the first GOPROXY entry before any off or direct.
func firstProxy(goproxy string) string {
	for _, entry := range strings.FieldsFunc(goproxy, func(r rune) bool { return r == ',' || r == '|' }) {
		entry = strings.TrimSpace(entry)
		if entry == "off" || entry == "direct" {
			return ""
		}
		if entry != "" {
			return entry
		}
	}
	return ""
}

type moduleCategory struct{}

func (moduleCategory) name() string { return CategoryModules }

func (moduleCategory) plan(ctx context.Context, w *World, o Options) *categoryPlan {
	root := w.Go.GOMODCACHE
	plan := &categoryPlan{applicable: true, EstimateIsUpperBound: true, locks: []string{root}}
	floor := maxDuration(floorModules, o.OlderThan)
	plan.EffectiveFloor = floor.String()
	w.progress(Event{Phase: "references", Category: CategoryModules})
	if info, err := os.Stat(root); err != nil || !info.IsDir() || !filepath.IsAbs(root) {
		plan.skip("unsupported-cache: GOMODCACHE " + root + " is not a directory")
		return plan
	}
	if hit := insideAny(root, w.Protected); hit != "" {
		plan.skip("protected: " + hit)
		return plan
	}
	if reason := w.bindProblem(root); reason != "" {
		plan.skip(reason)
		return plan
	}
	table := w.freshTable()
	if reason := processEvidence(table, root); reason != "" {
		plan.skip(reason)
		return plan
	}
	if w.ProtectedGap != "" {
		plan.skip("incomplete-reference-set: " + w.ProtectedGap)
		return plan
	}
	if reason, pids := moduleBusy(table); reason != "" {
		plan.skip(reason)
		plan.PIDs = pids
		return plan
	}
	refs, ignored, err := referenceSet(w.Checkouts, w.IgnoreSumFiles)
	for _, path := range ignored {
		plan.items = append(plan.items, &item{ItemReport: ItemReport{Path: path, Status: StatusKeep, Reason: "ignored-sum-file"}})
	}
	if err != nil {
		plan.skip("incomplete-reference-set: " + err.Error())
		return plan
	}
	versions, err := readModuleCache(w, root)
	if err != nil {
		plan.skip("partial-scan: " + err.Error())
		return plan
	}
	rootIdentity, err := w.Identity(root)
	if err != nil {
		plan.skip("partial-scan: " + err.Error())
		return plan
	}
	accumulator := newReclaimAccumulator()
	budget := &probeClock{remaining: probeBudget}
	// The reference set is read again once at apply, so a sum file changed
	// between plan and apply is still honoured.
	// Before each version, the sum files are re-checked by size and
	// modification time, and the set is read again when any changed, so a
	// branch checked out while prune works is honoured.
	var mu sync.Mutex
	var freshRefs map[string]bool
	var freshErr error
	var stamp string
	current := func() (map[string]bool, error) {
		mu.Lock()
		defer mu.Unlock()
		now, err := sumStamp(w.Checkouts, w.IgnoreSumFiles)
		if err != nil {
			return nil, err
		}
		if freshRefs == nil || now != stamp {
			freshRefs, _, freshErr = referenceSet(w.Checkouts, w.IgnoreSumFiles)
			stamp = now
		}
		return freshRefs, freshErr
	}
	var estimate int64
	for _, v := range versions {
		if alwaysKeptModules[v.path] || refs[v.key()] || w.Now().Sub(v.newest) < floor {
			continue
		}
		it := &item{ItemReport: ItemReport{Path: v.extracted, Status: StatusKeep}, root: root, device: rootIdentity.Dev}
		plan.items = append(plan.items, it)
		if reason := refetchProblem(w.Go, v.path); reason != "" {
			it.Reason = reason
			continue
		}
		if v.foreign {
			it.Reason = "unknown: a file Go does not write sits beside " + v.key()
			continue
		}
		var scan scanResult
		if v.hasExtraction {
			scan = w.Scan(v.extracted, rootIdentity.MountID, w.Protected)
			if reason := extractedVerdict(scan); reason != "" {
				it.Reason = reason
				continue
			}
		}
		if _, hashed := v.files[".ziphash"]; v.hasExtraction && hashed {
			if reason := locallyModified(v); reason != "" {
				it.Reason = reason
				continue
			}
		}
		released := accumulator.add(moduleInodes(v, scan))
		it.Status, it.estimate, it.known, it.Bytes = StatusDelete, released, true, released
		it.Reason = "no sum file in the fleet names " + v.key() + "; downloaded " + w.Now().Sub(v.newest).Round(time.Hour).String() + " ago"
		version := v
		it.apply = func(ctx context.Context) itemResult {
			refs, err := current()
			if err != nil {
				return itemResult{status: StatusSkipped, reason: "incomplete-reference-set: " + err.Error()}
			}
			if refs[version.key()] {
				return itemResult{status: StatusSkipped, reason: "referenced: a sum file now names " + version.key()}
			}
			return applyModule(ctx, w, version, rootIdentity.MountID, budget)
		}
		estimate += released
	}
	plan.EstimateBytes = &estimate
	return plan
}

// moduleBusy: any Go tool at all makes the module cache busy, because a build
// can use sum files outside any checkout, an external GOWORK, or -mod=mod.
func moduleBusy(table Table) (string, []PIDReport) {
	var pids []PIDReport
	unverified := false
	for _, process := range table.Processes {
		if process.IsGoTool() {
			pids = append(pids, PIDReport{PID: process.PID, Command: process.Command()})
			unverified = unverified || !process.Readable
		}
	}
	switch {
	case unverified:
		return "busy-unverified", pids
	case len(pids) > 0:
		return "busy", pids
	}
	return "", nil
}

// locallyModified compares the extracted tree with the recorded zip hash; a
// mismatch is local edits a re-download would not restore.
func locallyModified(v moduleVersion) string {
	recorded, err := os.ReadFile(filepath.Join(v.downloadDir, v.escVersion+".ziphash"))
	if err != nil {
		return "partial-scan: " + err.Error()
	}
	actual, err := dirhash.HashDir(v.extracted, v.path+"@"+v.version, dirhash.Hash1)
	if err != nil {
		return "partial-scan: " + err.Error()
	}
	if strings.TrimSpace(string(recorded)) != actual {
		return "locally-modified"
	}
	return ""
}

// moduleInodes is exactly what apply unlinks, with real link counts.
func moduleInodes(v moduleVersion, scan scanResult) []inodeRef {
	inodes := append([]inodeRef{}, scan.Inodes...)
	for _, file := range v.files {
		inodes = append(inodes, inodeRef{dev: file.Dev, ino: file.Ino, links: file.links, blocks: file.blocks})
	}
	return inodes
}

// extractedVerdict refuses an extracted tree prune cannot safely remove.
func extractedVerdict(scan scanResult) string {
	switch {
	case scan.Protected != "":
		return "protected: " + scan.Protected
	case scan.Partial:
		return "partial-scan"
	case scan.ContainsMount:
		return "contains-mount"
	case scan.HasGit:
		return "contains-checkout"
	case scan.HasSpecial:
		return "runtime"
	}
	return ""
}

// probeClock is the run's remaining time for re-fetch probes.
type probeClock struct{ remaining time.Duration }

// applyModule proves the version can come back, then deletes it under Go's
// own lock in an order Go recovers from if prune is killed at any step.
func applyModule(ctx context.Context, w *World, v moduleVersion, mount uint64, budget *probeClock) itemResult {
	table := w.freshTable()
	if reason := processEvidence(table, w.Go.GOMODCACHE); reason != "" {
		return itemResult{status: StatusSkipped, reason: reason}
	}
	if reason, pids := moduleBusy(table); reason != "" {
		return itemResult{status: StatusSkipped, reason: reason, pids: pids}
	}
	if reason := proveRefetch(ctx, w, v, budget); reason != "" {
		return itemResult{status: StatusSkipped, reason: reason}
	}
	release, err := w.LockModuleVersion(filepath.Join(v.downloadDir, v.escVersion+".lock"))
	if errors.Is(err, ErrLocked) {
		return itemResult{status: StatusSkipped, reason: "busy: Go holds " + v.key() + "'s lock"}
	}
	if err != nil {
		return itemResult{err: err}
	}
	defer release()
	// Under Go's lock: the version must still be exactly what the plan
	// checked, unedited, or Go has touched it since.
	if reason := unchangedSincePlan(w, v, mount); reason != "" {
		return itemResult{status: StatusSkipped, reason: reason}
	}
	// The probes and the hash above can take seconds; the final process
	// check comes immediately before the first mutation.
	final := w.freshTable()
	if reason := processEvidence(final, w.Go.GOMODCACHE); reason != "" {
		return itemResult{status: StatusSkipped, reason: reason}
	}
	if reason, pids := moduleBusy(final); reason != "" {
		return itemResult{status: StatusSkipped, reason: reason, pids: pids}
	}
	var result itemResult
	remove := func(ext string) error {
		file, ok := v.files[ext]
		if !ok {
			return nil
		}
		bytes, err := w.RemoveFile(v.downloadDir, v.escVersion+ext, file.Identity)
		result.unlinked += bytes
		return err
	}
	// From here Go treats the version as not downloaded.
	if err := remove(".ziphash"); err != nil {
		return changedOr(result, err)
	}
	partial := filepath.Join(v.downloadDir, v.escVersion+".partial")
	writeMarker(partial)
	if v.hasExtraction {
		outcome, err := w.RemoveTree(filepath.Dir(v.extracted), filepath.Base(v.extracted), v.extractedID)
		tree := treeResult(outcome, err)
		result.unlinked += tree.unlinked
		if tree.err != nil || tree.status != "" {
			tree.unlinked = result.unlinked
			return tree
		}
	}
	for _, ext := range []string{".zip", ".mod", ".info"} {
		if err := remove(ext); err != nil {
			return changedOr(result, err)
		}
	}
	_ = os.Remove(partial)
	return result
}

func changedOr(result itemResult, err error) itemResult {
	if errors.Is(err, ErrChanged) {
		result.status, result.reason = StatusSkipped, "changed"
		return result
	}
	result.err = err
	return result
}

// proveRefetch asks the proxy for the version: its .mod must match the local
// one byte for byte, and its .zip, when the cache holds one, must exist.
func proveRefetch(ctx context.Context, w *World, v moduleVersion, budget *probeClock) string {
	if budget.remaining <= 0 {
		return "not-refetchable: probe-budget"
	}
	base := strings.TrimSuffix(firstProxy(w.Go.GOPROXY), "/") + "/" + v.escPath + "/@v/" + v.escVersion
	started := time.Now()
	defer func() { budget.remaining -= time.Since(started) }()
	// The whole proof, every request included, fits in what remains of the
	// run's probe budget.
	ctx, cancel := context.WithTimeout(ctx, budget.remaining)
	defer cancel()
	if reason := loopbackProxy(ctx, w); reason != "" {
		return reason
	}
	local, err := os.ReadFile(filepath.Join(v.downloadDir, v.escVersion+".mod"))
	if err != nil {
		// Without the cached go.mod there is nothing to prove the proxy
		// serves the same version against.
		return "not-refetchable: no readable cached go.mod to compare with the proxy"
	}
	{
		status, body, err := w.HTTP(ctx, "GET", base+".mod")
		switch {
		case err != nil:
			return "not-refetchable: " + err.Error()
		case status == 401 || status == 403:
			return "not-refetchable: proxy-auth"
		case status != 200:
			return fmt.Sprintf("not-refetchable: missing-at-proxy (%d)", status)
		case !bytes.Equal(body, local):
			return "not-refetchable: the proxy's go.mod differs from the cached one"
		}
	}
	if v.hasZip {
		status, _, err := w.HTTP(ctx, "HEAD", base+".zip")
		if err != nil {
			return "not-refetchable: " + err.Error()
		}
		if status != 200 {
			return fmt.Sprintf("not-refetchable: missing-at-proxy (%d)", status)
		}
	}
	return ""
}

// unchangedSincePlan re-verifies, under Go's lock and before anything is
// changed, what the plan checked: every download file, the absence of a
// .ziphash the plan did not see, the extracted tree and its hash, and its
// mount.
func unchangedSincePlan(w *World, v moduleVersion, mount uint64) string {
	// The complete file set is re-enumerated: a file added since the plan,
	// such as the .ziphash of a download Go completed, changes the version.
	entries, err := os.ReadDir(v.downloadDir)
	if err != nil {
		return "changed: " + err.Error()
	}
	present := map[string]bool{}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), v.escVersion+".") {
			continue
		}
		ext := entry.Name()[len(v.escVersion):]
		if ext == ".lock" || ext == ".partial" {
			continue
		}
		present[ext] = true
		planned, known := v.files[ext]
		if !known {
			if ext == ".ziphash" {
				return "changed: Go completed the download since the plan"
			}
			return "changed: " + entry.Name() + " appeared since the plan"
		}
		info, err := os.Lstat(filepath.Join(v.downloadDir, entry.Name()))
		identity, idErr := w.Identity(filepath.Join(v.downloadDir, entry.Name()))
		if err != nil || idErr != nil || !planned.same(stateOf(identity, info)) {
			return "changed: " + entry.Name() + " is not the file the plan checked"
		}
	}
	for ext := range v.files {
		if !present[ext] {
			return "changed: " + v.escVersion + ext + " is gone"
		}
	}
	if v.hasExtraction && v.extractedID.MountID != mount {
		return "contains-mount"
	}
	// Every download file, the .ziphash included, already compared equal in
	// identity, size and times; any write changes ctime.
	if v.hasExtraction {
		identity, err := w.Identity(v.extracted)
		if err != nil || identity.Dev != v.extractedID.Dev || identity.Ino != v.extractedID.Ino {
			return "changed: the extracted tree is not the one the plan checked"
		}
		if reason := extractedVerdict(w.Scan(v.extracted, mount, w.Protected)); reason != "" {
			return reason
		}
		if v.ziphash != "" {
			if reason := locallyModified(v); reason != "" {
				return reason
			}
		}
	}
	return ""
}

// loopbackProxy resolves the proxy's host at apply: a name that resolves to
// a loopback address may be serving the very cache being pruned.
func loopbackProxy(ctx context.Context, w *World) string {
	host := strings.TrimSuffix(mustHost(firstProxy(w.Go.GOPROXY)), ".")
	addresses, err := w.LookupHost(ctx, host)
	if err != nil {
		return "not-refetchable: " + err.Error()
	}
	for _, address := range addresses {
		if ip := net.ParseIP(address); ip != nil && ip.IsLoopback() {
			return "not-refetchable: proxy " + host + " resolves to loopback " + address
		}
	}
	return ""
}

// mustHost is a proxy URL's host; refetchProblem already rejected any URL
// that does not parse, so a parse failure here yields "".
func mustHost(raw string) string {
	parsed, _ := url.Parse(raw)
	if parsed == nil {
		return ""
	}
	return parsed.Hostname()
}

// sumStamp fingerprints every sum file below the checkouts by path, size and
// modification time; any difference means the reference set must be read
// again.
func sumStamp(checkouts, ignore []string) (string, error) {
	var stamp strings.Builder
	for _, original := range checkouts {
		checkout, err := filepath.EvalSymlinks(original)
		if err != nil {
			return "", err
		}
		err = filepath.WalkDir(checkout, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if path != checkout && (entry.Name() == "node_modules" || entry.Name() == ".git") {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(entry.Name(), ".sum") {
				return nil
			}
			// A file that vanished mid-walk stamps as gone, which differs
			// from any earlier stamp all the same.
			size, modified := int64(-1), int64(0)
			if info, err := entry.Info(); err == nil {
				size, modified = info.Size(), info.ModTime().UnixNano()
			}
			fmt.Fprintf(&stamp, "%s %d %d\n", path, size, modified)
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return stamp.String(), nil
}
