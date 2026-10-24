//go:build windows

package agents

import "os"

// lockFile reports that Windows has no inherited advisory lock in this build, so
// shared-checkout execution is refused rather than serialized by a pretend lock.
var lockFile = func(*os.File) error { return ErrLeaseUnsupported }

func unlockFile(*os.File) error { return nil }

func isLockBusy(error) bool { return false }
