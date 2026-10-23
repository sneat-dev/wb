package fleet

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// readStat reads /proc/stat; a test replaces it.
var readStat = func() ([]byte, error) { return os.ReadFile("/proc/stat") }

// bootTime is the `btime` of /proc/stat.
func bootTime() time.Time {
	data, err := readStat()
	if err != nil {
		return time.Time{}
	}
	return parseBootTime(string(data))
}

// parseBootTime reads the `btime` line (seconds since the epoch) of the text of
// /proc/stat; it returns the zero time when there is none.
func parseBootTime(stat string) time.Time {
	for _, line := range strings.Split(stat, "\n") {
		if value, found := strings.CutPrefix(line, "btime "); found {
			if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && seconds > 0 {
				return time.Unix(seconds, 0).UTC()
			}
		}
	}
	return time.Time{}
}
