//go:build linux

package diskprune

import (
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// scanSeams lets tests stand in for a mount or an unreadable entry that an
// unprivileged test cannot create.
type scanSeams struct {
	lstat   func(path string, stat *unix.Stat_t) error
	mountID func(path string) (uint64, error)
}

func realScanSeams() scanSeams { return scanSeams{lstat: unix.Lstat, mountID: mountIDOf} }

// scanTree walks root read-only. mount is the root's own mount ID; protected
// roots inside the tree are reported, not descended.
func scanTree(root string, mount uint64, protected []string) scanResult {
	return realScanSeams().scan(root, mount, protected)
}

func (s scanSeams) scan(root string, mount uint64, protected []string) scanResult {
	var result scanResult
	var newestDir time.Time
	sawFile := false
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			result.Partial = true
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path != root {
			if hit := insideAny(path, protected); hit != "" && within(hit, path) {
				result.Protected = hit
				return fs.SkipDir
			}
		}
		var stat unix.Stat_t
		if err := s.lstat(path, &stat); err != nil {
			result.Partial = true
			return nil
		}
		changed := latest(time.Unix(stat.Mtim.Unix()), time.Unix(stat.Ctim.Unix()))
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			newestDir = latest(newestDir, changed)
			name := entry.Name()
			if name == ".git" {
				result.HasGit = true
			}
			if path != root {
				id, err := s.mountID(path)
				if err != nil {
					result.Partial = true
					return fs.SkipDir
				}
				if id != mount {
					result.ContainsMount = true
					return fs.SkipDir
				}
			}
			if name == "download" && filepath.Base(filepath.Dir(path)) == "cache" {
				result.CacheData = true
			}
			if name == "fuzz" && nonEmptyDir(path) {
				result.CacheData = true
			}
			return nil
		case unix.S_IFSOCK, unix.S_IFIFO:
			result.HasSpecial = true
		}
		if entry.Name() == ".git" {
			result.HasGit = true
		}
		sawFile = true
		result.Newest = latest(result.Newest, changed)
		result.Files++
		result.Inodes = append(result.Inodes, inodeRef{dev: uint64(stat.Dev), ino: stat.Ino, links: uint64(stat.Nlink), blocks: stat.Blocks * 512})
		return nil
	})
	if !sawFile {
		result.Newest = newestDir
	}
	return result
}

func nonEmptyDir(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) > 0
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
