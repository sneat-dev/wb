//go:build linux

package hostload

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// readLoadAvg1 reads the 1-minute load average from the first field of
// /proc/loadavg (e.g. "2.34 2.10 1.98 3/512 12345").
func readLoadAvg1() (float64, error) {
	return readLoadAvg1FromPath("/proc/loadavg")
}

// readLoadAvg1FromPath reads a Linux load-average file from path.
func readLoadAvg1FromPath(path string) (float64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, fmt.Errorf("parse %s %q", path, string(raw))
	}
	load, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s %q: %w", path, string(raw), err)
	}
	return load, nil
}
