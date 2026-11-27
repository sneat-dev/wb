package diskprune

import (
	"path/filepath"
	"time"
)

// scanResult is everything the safety rules need to know about one candidate
// tree, gathered in a single read-only walk.
type scanResult struct {
	// Newest is the newest modification or status-change time of every
	// non-directory entry; directory times count only for a tree that holds
	// no non-directory entry, because removing children updates them.
	Newest time.Time
	// HasGit is a .git file or directory at any depth: a checkout.
	HasGit bool
	// HasSpecial is a socket or FIFO at any depth: a live runtime directory.
	HasSpecial bool
	// Partial is an unreadable entry anywhere: the rules cannot be checked.
	Partial bool
	// ContainsMount is a directory on another mount anywhere below the root.
	ContainsMount bool
	// CacheData is a module cache (cache/download) or a non-empty fuzz/ tree.
	CacheData bool
	// Protected is a protected root found inside the tree, if any.
	Protected string
	// Inodes are the regular files and symlinks, for exact reclaim
	// accounting across the selected set.
	Inodes []inodeRef
	Files  int64
}

type inodeRef struct {
	dev, ino uint64
	links    uint64
	blocks   int64
}

// reclaimAccumulator adds candidates in pruning order and reports, for each,
// the blocks that come back once it and every candidate before it are gone:
// a file whose last links are split across two candidates comes back with
// the second, not with neither.
type reclaimAccumulator struct {
	seen    map[[2]uint64]uint64
	counted map[[2]uint64]bool
}

func newReclaimAccumulator() *reclaimAccumulator {
	return &reclaimAccumulator{seen: map[[2]uint64]uint64{}, counted: map[[2]uint64]bool{}}
}

// add records a candidate's inodes and returns the bytes it newly releases.
func (a *reclaimAccumulator) add(inodes []inodeRef) int64 {
	var released int64
	for _, inode := range inodes {
		key := [2]uint64{inode.dev, inode.ino}
		a.seen[key]++
		if !a.counted[key] && a.seen[key] >= inode.links {
			a.counted[key] = true
			released += inode.blocks
		}
	}
	return released
}

// insideAny reports the first protected root that path lies inside or that
// lies inside path, so a candidate overlapping protected data in either
// direction is refused.
func insideAny(path string, protected []string) string {
	path = filepath.Clean(path)
	for _, root := range protected {
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if within(path, root) || within(root, path) {
			return root
		}
	}
	return ""
}

// latest is the later of two times.
func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
