package diskprune

import "errors"

// Identity is what a safety check recorded about an object, so deletion can
// refuse anything that is no longer that object (disk-prune#req:deletion).
type Identity struct {
	Dev     uint64
	Ino     uint64
	IsDir   bool
	MountID uint64
	// Birth is the inode's creation time in nanoseconds where the filesystem
	// records one (0 otherwise). A freed inode number is often handed straight
	// to the next file; its birth time tells the two apart.
	Birth int64
	// Ctime is a regular file's status-change time in nanoseconds; Go's
	// marking an entry as used since the plan changes it.
	Ctime int64
}

// sameInode reports whether dev, ino and birth name the checked inode.
func (want Identity) sameInode(dev, ino uint64, birth int64) bool {
	return dev == want.Dev && ino == want.Ino && (want.Birth == 0 || birth == want.Birth)
}

// Deletion outcomes a caller reports instead of treating them as failures.
var (
	// ErrChanged: the name no longer names the checked object.
	ErrChanged = errors.New("changed since it was checked")
	// ErrContainsMount: the candidate is, or contains, another mount.
	ErrContainsMount = errors.New("contains a mount")
	// ErrMoved: the candidate left its parent while it was being emptied.
	ErrMoved = errors.New("moved while it was being removed")
	// ErrReplaced: the contents were removed through the checked object, but
	// its name now names something else, which was left alone.
	ErrReplaced = errors.New("replaced after its contents were removed")
	// ErrUnsupported: this platform has no safe way to do it.
	ErrUnsupported = errors.New("unsupported on this platform")
)

// TreeOutcome counts what a tree removal actually unlinked.
type TreeOutcome struct {
	Files int64
	Bytes int64
}
