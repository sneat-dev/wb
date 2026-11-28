package diskprune

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	// goBuildDir is a go build/test work directory; testTempDir is what
	// t.TempDir, b.TempDir and f.TempDir create.
	goBuildDir  = regexp.MustCompile(`^go-build[0-9]+$`)
	testTempDir = regexp.MustCompile(`^(Test|Benchmark|Fuzz)[A-Za-z0-9_]*[0-9]+$`)
	// wbScratch is WB's own regenerable temporary directories: a known
	// prefix and the numeric suffix os.MkdirTemp adds, or a per-task Go
	// cache (wb-<task>-cache, wb-<task>-go-cache).
	wbScratch = regexp.MustCompile(`^wb-(coverage-baseline|coverage-tiers|go-test-shards|index|sync-report-validate|test-cpu-queue|worktree-merge-main-deadcode|agent-preflight|smoke|deadcode-tool|native-cover)-[0-9]+$|^wb-[A-Za-z0-9][A-Za-z0-9._-]*-(go-)?cache$`)
)

// scratchName reports whether a temporary-directory entry's name is one of
// the producers prune knows; nothing else in the temporary directory is
// ever considered.
func scratchName(name string) bool {
	return goBuildDir.MatchString(name) || testTempDir.MatchString(name) || wbScratch.MatchString(name)
}

func pruneScratch(ctx context.Context, env Env, o Options) CategoryReport {
	report := CategoryReport{Name: CategoryScratch, Status: StatusKeep, Kept: map[string]int{}}
	if env.GoCachesUnknown != "" {
		// Without the Go caches' locations a cache in TMPDIR could be taken
		// for scratch.
		report.Status, report.Reason, report.Kept = StatusFailed, env.GoCachesUnknown, nil
		return report
	}
	mounts, err := env.MountPoints()
	if err != nil {
		report.Status, report.Reason, report.Kept = StatusFailed, "mount points cannot be read: "+err.Error(), nil
		return report
	}
	failure := ""
roots:
	for _, root := range env.TempDirs {
		rootInfo, err := os.Stat(root)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		var entries []os.DirEntry
		if err == nil {
			entries, err = os.ReadDir(root)
		}
		if err != nil {
			// A temporary directory that exists but cannot be listed is a
			// failure, never an empty one.
			failure = "temporary directory " + root + " cannot be read: " + err.Error()
			continue
		}
		if checkout := checkoutAround(root); checkout != "" {
			// TMPDIR inside (or being) a checkout: its entries are that
			// checkout's files, never scratch.
			report.Kept["temporary directory inside the checkout "+checkout]++
			continue
		}
		for _, entry := range entries {
			if ctx.Err() != nil {
				failure = "cancelled"
				break roots
			}
			if !scratchName(entry.Name()) {
				continue
			}
			path := filepath.Join(root, entry.Name())
			// The identity is taken first, then the size, then the checks,
			// so a directory renamed into place at any point is caught by
			// the comparison before removal.
			checked, err := lstatEntry(path)
			if err != nil {
				report.Kept["unreadable"]++
				continue
			}
			bytes := env.Measure(ctx, path)
			if keep := scratchCheck(env, o, rootInfo, path, mounts); keep != "" {
				report.Kept[keep]++
				continue
			}
			item := ItemReport{Path: path, Bytes: bytes, Status: StatusDelete}
			if o.Apply {
				if ctx.Err() != nil {
					failure = "cancelled"
					break roots
				}
				// The name must still be the directory just checked: one
				// replaced meanwhile (a producer recreating it) is kept.
				if now, err := os.Lstat(path); err != nil || !os.SameFile(now, checked) {
					report.Kept["replaced while checked"]++
					continue
				}
				// Mount points are read again for each removal: one added
				// inside an old tree meanwhile is still seen.
				if fresh, err := env.MountPoints(); err != nil || containsMount(path, fresh) {
					report.Kept["contains a mount"]++
					continue
				}
				if err := env.RemoveAll(path); err != nil {
					item.Status, item.Error = StatusFailed, err.Error()
				} else {
					item.Status = StatusDeleted
				}
			}
			report.Items = append(report.Items, item)
		}
	}
	for _, item := range report.Items {
		switch item.Status {
		case StatusFailed:
			report.Status = StatusFailed
		default:
			report.Bytes += item.Bytes
			if report.Status != StatusFailed {
				report.Status = item.Status
			}
		}
	}
	if failure == "" && ctx.Err() != nil {
		failure = "cancelled"
	}
	if failure != "" {
		report.Status, report.Reason = StatusFailed, failure
	}
	if len(report.Kept) == 0 {
		report.Kept = nil
	}
	return report
}

// scratchCheck is why a scratch-named entry stays, or "" when it may go,
// with the entry as checked: it must be this user's own real directory on
// the temporary directory's filesystem, not a Go cache, contain no mount
// point (bind mounts on the same filesystem included), no checkout and no
// socket or FIFO, and nothing in it may have changed for the scratch age.
func scratchCheck(env Env, o Options, rootInfo os.FileInfo, path string, mounts []string) string {
	for _, cache := range env.GoCaches {
		if within(cache, path) || within(path, cache) {
			return "is or holds a Go cache"
		}
	}
	if containsMount(path, mounts) {
		return "contains a mount"
	}
	return scratchKeepReasonWith(env, o, rootInfo, path, func(_ string, info os.FileInfo) uint64 { return deviceOf(info) })
}

func containsMount(path string, mounts []string) bool {
	for _, mount := range mounts {
		if within(mount, path) {
			return true
		}
	}
	return false
}

// checkoutAround is the git checkout dir lies in (or is), found by a .git
// entry in dir or an ancestor; "" when there is none.
func checkoutAround(dir string) string {
	for current := filepath.Clean(dir); ; current = filepath.Dir(current) {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		if current == filepath.Dir(current) {
			return ""
		}
	}
}

// within reports whether candidate is path or lies below it.
func within(candidate, path string) bool {
	candidate, path = filepath.Clean(candidate), filepath.Clean(path)
	return candidate == path || strings.HasPrefix(candidate, path+string(filepath.Separator))
}

// scratchKeepReasonWith takes the device lookup as a seam: a test cannot
// create a mount.
func scratchKeepReasonWith(env Env, o Options, rootInfo os.FileInfo, path string, device func(string, os.FileInfo) uint64) string {
	info, err := os.Lstat(path)
	switch {
	case err != nil:
		return "unreadable"
	case !info.IsDir():
		return "not a directory"
	case !ownedBy(info, env.UID):
		return "owned by another user"
	case device(path, info) != deviceOf(rootInfo):
		return "another filesystem"
	}
	own := device(path, info)
	var newest time.Time
	keep := ""
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		var entryInfo os.FileInfo
		if err == nil {
			entryInfo, err = d.Info()
		}
		if err != nil {
			keep = "unreadable"
			return fs.SkipAll
		}
		switch {
		case d.Name() == ".git":
			keep = "contains a checkout"
		case d.IsDir() && device(p, entryInfo) != own:
			keep = "contains a mount"
		case entryInfo.Mode()&(fs.ModeSocket|fs.ModeNamedPipe) != 0:
			keep = "in use (socket or FIFO)"
		}
		if keep != "" {
			return fs.SkipAll
		}
		if modified := entryInfo.ModTime(); modified.After(newest) {
			newest = modified
		}
		return nil
	})
	switch {
	case keep != "":
		return keep
	case env.Now().Sub(newest) < o.ScratchAge:
		return "changed within " + strings.TrimSuffix(o.ScratchAge.String(), "0m0s")
	}
	return ""
}

// removeAll removes a tree without following symlinks. Go writes extracted
// module trees read-only, so when the first attempt fails, this user's own
// directories in the tree are made writable and the removal is retried.
func removeAll(path string) error {
	if err := os.RemoveAll(path); err == nil {
		return nil
	}
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		// Checked again just before, so a directory replaced by a symlink
		// since the walk listed it is never followed.
		if err == nil && d.IsDir() {
			chmodDirNoFollow(p)
		}
		return nil
	})
	return os.RemoveAll(path)
}

// lstatEntry is os.Lstat; a seam for an entry that vanishes between the
// listing and its inspection.
var lstatEntry = os.Lstat
