//go:build windows

package diskprune

import "os"

// ownedBy cannot be established on Windows, so scratch removes nothing there.
func ownedBy(info os.FileInfo, uid int) bool { return false }

func deviceOf(info os.FileInfo) uint64 { return 0 }

// chmodDirNoFollow does nothing on Windows, where scratch removes nothing.
func chmodDirNoFollow(path string) {}
