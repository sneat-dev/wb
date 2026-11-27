package diskprune

import (
	"context"
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
	wbScratch = regexp.MustCompile(`^wb-(coverage-baseline|coverage-tiers|go-test-shards|index|sync-report-validate|test-cpu-queue|worktree-merge-main-deadcode|agent-preflight|smoke|deadcode-tool|native-cover)-[0-9]+$|^wb-.+-(go-)?cache$`)
)

// scratchName reports whether a temporary-directory entry's name is one of
// the producers prune knows; nothing else in the temporary directory is
// ever considered.
func scratchName(name string) bool {
	return goBuildDir.MatchString(name) || testTempDir.MatchString(name) || wbScratch.MatchString(name)
}

func pruneScratch(ctx context.Context, env Env, o Options) CategoryReport {
	report := CategoryReport{Name: CategoryScratch, Status: StatusKeep, Kept: map[string]int{}}
	for _, root := range env.TempDirs {
		rootInfo, err := os.Stat(root)
		if err != nil {
			report.Kept["temporary directory unreadable"]++
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			report.Kept["temporary directory unreadable"]++
			continue
		}
		for _, entry := range entries {
			if ctx.Err() != nil {
				return report
			}
			if !scratchName(entry.Name()) {
				continue
			}
			path := filepath.Join(root, entry.Name())
			if keep := scratchKeepReason(env, o, rootInfo, path); keep != "" {
				report.Kept[keep]++
				continue
			}
			item := ItemReport{Path: path, Bytes: env.Measure(ctx, path), Status: StatusDelete}
			if o.Apply {
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
		case StatusDelete, StatusDeleted:
			report.Bytes += item.Bytes
			if report.Status != StatusFailed {
				report.Status = item.Status
			}
		}
	}
	if len(report.Kept) == 0 {
		report.Kept = nil
	}
	return report
}

// scratchKeepReason is why a scratch-named entry stays, or "" when it may
// go: it must be this user's own real directory on the temporary
// directory's filesystem, hold no other mount, no checkout and no socket or
// FIFO, and nothing in it may have changed for the scratch age.
func scratchKeepReason(env Env, o Options, rootInfo os.FileInfo, path string) string {
	return scratchKeepReasonWith(env, o, rootInfo, path, func(_ string, info os.FileInfo) uint64 { return deviceOf(info) })
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
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	return os.RemoveAll(path)
}
