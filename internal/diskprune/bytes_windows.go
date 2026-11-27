//go:build windows

package diskprune

import "os"

func diskBytes(info os.FileInfo) int64 { return info.Size() }

func unlinkBytes(info os.FileInfo) int64 { return info.Size() }

func linkCount(info os.FileInfo) uint64 { return 1 }
