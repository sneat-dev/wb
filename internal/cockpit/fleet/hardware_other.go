//go:build !linux && !darwin && !windows

package fleet

import "time"

// bootTime is not read on this operating system.
func bootTime() time.Time { return time.Time{} }
