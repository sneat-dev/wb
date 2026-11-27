//go:build windows

package diskprune

// Windows has no descriptor-relative unlink WB relies on, so prune deletes
// nothing there; every category reports unsupported-platform.

func RemoveFileAt(dir, name string, expect Identity) (int64, error) { return 0, ErrUnsupported }

func RemoveTree(parent, name string, expect Identity) (TreeOutcome, error) {
	return TreeOutcome{}, ErrUnsupported
}

func StatIdentity(path string) (Identity, error) { return Identity{}, ErrUnsupported }
