//go:build windows

package diskprune

import (
	"fmt"
	"os"
)

// Windows has no descriptor-relative unlink WB relies on, so prune deletes
// nothing there; every category reports unsupported-platform.

func RemoveFileBeneath(anchor Anchor, rel, name string, expect Identity) (int64, error) {
	return 0, ErrUnsupported
}

func RemoveTree(parent, name string, expect Identity) (TreeOutcome, error) {
	return TreeOutcome{}, ErrUnsupported
}

func StatIdentity(path string) (Identity, error) { return Identity{}, ErrUnsupported }

// writeNewFile creates path exclusively.
func writeNewFile(path string, raw []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func readSmallRegular(path string, n int) ([]byte, error) { return nil, ErrUnsupported }

func hasACL(path string) bool { return true }

func bindOfSubdirectory(path string) (bool, error) { return false, nil }

func mountIDOf(path string) (uint64, error) { return 0, ErrUnsupported }

// readRegularFile reads a regular file of at most limit bytes.
func readRegularFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("%s is not a regular file of at most %d bytes", path, limit)
	}
	return os.ReadFile(path)
}
