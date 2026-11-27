//go:build windows

package diskprune

import (
	"os"
	"path/filepath"
)

// ownedBy cannot be established on Windows, where prune deletes nothing.
func ownedBy(info os.FileInfo, uid int) bool { return false }

// ownedByUserPath cannot be established on Windows.
func ownedByUserPath(path string) bool { return true }

// traversable cannot be established on Windows.
func traversable(path string) bool { return false }

// dirKey falls back to the cleaned path on Windows.
func dirKey(path string) (string, bool) { return filepath.Clean(path), true }
