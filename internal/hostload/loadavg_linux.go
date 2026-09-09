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
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, fmt.Errorf("read /proc/loadavg: %w", err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, fmt.Errorf("parse /proc/loadavg %q", string(raw))
	}
	load, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("parse /proc/loadavg %q: %w", string(raw), err)
	}
	return load, nil
}
