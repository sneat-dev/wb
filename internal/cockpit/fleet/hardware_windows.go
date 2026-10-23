package fleet

import (
	"syscall"
	"time"
)

var getTickCount64 = syscall.NewLazyDLL("kernel32.dll").NewProc("GetTickCount64")

// bootTime is now less the milliseconds since the system started.
func bootTime() time.Time {
	milliseconds, _, _ := getTickCount64.Call()
	return time.Now().Add(-time.Duration(milliseconds) * time.Millisecond).UTC().Truncate(time.Second)
}
