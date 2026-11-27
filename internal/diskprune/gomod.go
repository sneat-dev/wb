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
	"time"

	"golang.org/x/mod/module"
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
	for _, checkout := range checkouts {
		err := filepath.WalkDir(checkout, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			if entry.IsDir() {
				if path != checkout && (entry.Name() == "node_modules" || entry.Name() == ".git") {
					return fs.SkipDir
				}
				return nil
			}
			if !isSumFile(path, entry.Name()) {
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
func isSumFile(path, name string) bool {
	if name == "go.sum" || name == "go.work.sum" {
		return true
	}
	base, ok := strings.CutSuffix(name, ".sum")
	if !ok {
		return false
	}
	_, err := os.Stat(filepath.Join(filepath.Dir(path), base+".mod"))
	return err == nil
}

func readSumFile(path string, refs map[string]bool) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
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
	return nil
}

// moduleVersion is one version in the module cache.
type moduleVersion struct {
	path, version         string
	escPath, escVersion   string
	downloadDir           string
	extracted             string
	newest                time.Time
	files                 map[string]Identity // download files by extension
	hasZip, hasExtraction bool
}

func (v moduleVersion) key() string { return v.path + "@" + v.version }

// readModuleCache lists the cache's versions from cache/download. A path or
// version that does not unescape is skipped: it is not a version Go wrote.
func readModuleCache(w *World, root string) ([]moduleVersion, error) {
	download := filepath.Join(root, "cache", "download")
	versions := map[string]*moduleVersion{}
	err := filepath.WalkDir(download, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == filepath.Join(download, "sumdb") {
				return fs.SkipDir
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
		if errPath != nil || errVersion != nil {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		identity, err := w.Identity(path)
		if err != nil {
			return err
		}
		key := modPath + "@" + version
		v := versions[key]
		if v == nil {
			v = &moduleVersion{path: modPath, version: version, escPath: escPath, escVersion: escVersion,
				downloadDir: dir, extracted: filepath.Join(root, filepath.FromSlash(escPath)+"@"+escVersion), files: map[string]Identity{}}
			versions[key] = v
		}
		v.files[ext] = identity
		v.newest = latest(v.newest, changeTime(info))
		v.hasZip = v.hasZip || ext == ".zip"
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]moduleVersion, 0, len(versions))
	for _, v := range versions {
		if info, err := os.Lstat(v.extracted); err == nil && info.IsDir() {
			v.hasExtraction = true
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
	if host := parsed.Hostname(); host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
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
	if info, err := os.Stat(root); err != nil || !info.IsDir() || !filepath.IsAbs(root) {
		plan.skip("unsupported-cache: GOMODCACHE " + root + " is not a directory")
		return plan
	}
	if hit := insideAny(root, w.Protected); hit != "" {
		plan.skip("protected: " + hit)
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
	var estimate int64
	for _, v := range versions {
		if alwaysKeptModules[v.path] || refs[v.key()] || w.Now().Sub(v.newest) < floor {
			continue
		}
		it := &item{ItemReport: ItemReport{Path: v.extracted, Status: StatusKeep}, device: rootIdentity.Dev}
		plan.items = append(plan.items, it)
		if reason := refetchProblem(w.Go, v.path); reason != "" {
			it.Reason = reason
			continue
		}
		if v.hasExtraction && v.files[".ziphash"] != (Identity{}) {
			if reason := locallyModified(v); reason != "" {
				it.Reason = reason
				continue
			}
		}
		released := accumulator.add(moduleInodes(w, v, rootIdentity.MountID))
		it.Status, it.estimate, it.known, it.Bytes = StatusDelete, released, true, released
		it.Reason = "no sum file in the fleet names " + v.key() + "; downloaded " + w.Now().Sub(v.newest).Round(time.Hour).String() + " ago"
		version := v
		it.apply = func(ctx context.Context) itemResult {
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

func moduleInodes(w *World, v moduleVersion, mount uint64) []inodeRef {
	var inodes []inodeRef
	if v.hasExtraction {
		inodes = w.Scan(v.extracted, mount, nil).Inodes
	}
	for ext := range v.files {
		path := filepath.Join(v.downloadDir, v.escVersion+ext)
		if info, err := os.Lstat(path); err == nil {
			inodes = append(inodes, inodeRef{dev: v.files[ext].Dev, ino: v.files[ext].Ino, links: 1, blocks: diskBytes(info)})
		}
	}
	return inodes
}

// probeClock is the run's remaining time for re-fetch probes.
type probeClock struct{ remaining time.Duration }

// applyModule proves the version can come back, then deletes it under Go's
// own lock in an order Go recovers from if prune is killed at any step.
func applyModule(ctx context.Context, w *World, v moduleVersion, mount uint64, budget *probeClock) itemResult {
	if reason, pids := moduleBusy(w.freshTable()); reason != "" {
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
	var result itemResult
	remove := func(ext string) error {
		identity, ok := v.files[ext]
		if !ok {
			return nil
		}
		bytes, err := w.RemoveFile(v.downloadDir, v.escVersion+ext, identity)
		result.unlinked += bytes
		return err
	}
	// From here Go treats the version as not downloaded.
	if err := remove(".ziphash"); err != nil {
		return changedOr(result, err)
	}
	partial := filepath.Join(v.downloadDir, v.escVersion+".partial")
	_ = os.WriteFile(partial, nil, 0o644)
	if v.hasExtraction {
		identity, err := w.Identity(v.extracted)
		if err != nil {
			return itemResult{unlinked: result.unlinked, err: err}
		}
		if identity.MountID != mount {
			return itemResult{unlinked: result.unlinked, status: StatusSkipped, reason: "contains-mount"}
		}
		outcome, err := w.RemoveTree(filepath.Dir(v.extracted), filepath.Base(v.extracted), identity)
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
	local, err := os.ReadFile(filepath.Join(v.downloadDir, v.escVersion+".mod"))
	if err == nil {
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
