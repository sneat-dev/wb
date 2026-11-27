//go:build windows

package diskprune

import "os"

func diskBytes(info os.FileInfo) int64 { return info.Size() }
