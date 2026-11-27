//go:build windows

package diskprune

import "os"

// ownedBy cannot be established on Windows, where prune deletes nothing.
func ownedBy(info os.FileInfo, uid int) bool { return false }

// ownedByUserPath cannot be established on Windows.
func ownedByUserPath(path string) bool { return true }
