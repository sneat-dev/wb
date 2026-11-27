//go:build !linux

package diskprune

import (
	"os"
	"time"
)

// changeTime falls back to the modification time where module pruning does
// not run anyway.
func changeTime(info os.FileInfo) time.Time { return info.ModTime() }
