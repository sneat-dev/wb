package diskprune

import "errors"

// Identity is what a safety check recorded about an object, so deletion can
// refuse anything that is no longer that object (disk-prune#req:deletion).
type Identity struct {
	Dev     uint64
	Ino     uint64
	IsDir   bool
	MountID uint64
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
