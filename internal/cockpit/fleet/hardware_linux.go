package fleet

import (
	"os"
	"time"
)

// bootTime is the `btime` of /proc/stat.
func bootTime() time.Time {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}
	}
	return parseBootTime(string(data))
}
