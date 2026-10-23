package fleet

import (
	"time"

	"golang.org/x/sys/unix"
)

// sysctlTimeval reads a time-valued sysctl; a test replaces it.
var sysctlTimeval = unix.SysctlTimeval

// bootTime is the kernel's kern.boottime.
func bootTime() time.Time {
	value, err := sysctlTimeval("kern.boottime")
	if err != nil {
		return time.Time{}
	}
	return time.Unix(value.Sec, int64(value.Usec)*int64(time.Microsecond)).UTC()
}
