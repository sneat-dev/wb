//go:build windows

package agents

import "os"

// fileLockingSupported is false because Windows has no inherited advisory lock
// in this build. Shared-checkout execution is refused rather than serialized by
// a pretend lock; unique-worktree modes stay available.
const fileLockingSupported = false

func lockFile(*os.File) error { return ErrLeaseUnsupported }

func unlockFile(*os.File) error { return nil }

func isLockBusy(error) bool { return false }
