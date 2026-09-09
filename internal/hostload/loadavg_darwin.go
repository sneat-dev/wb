//go:build darwin

package hostload

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// readLoadAvg1 reads the 1-minute load average via `sysctl -n vm.loadavg`,
// which prints e.g. "{ 2.34 2.10 1.98 }". Shelling out avoids parsing the
// raw fixed-point struct.loadavg sysctl layout by hand.
func readLoadAvg1() (float64, error) {
	output, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		return 0, fmt.Errorf("read vm.loadavg: %w", err)
	}
	fields := strings.Fields(strings.TrimSpace(string(output)))
	for _, field := range fields {
		if field == "{" || field == "}" {
			continue
		}
		load, err := strconv.ParseFloat(field, 64)
		if err != nil {
			continue
		}
		return load, nil
	}
	return 0, fmt.Errorf("parse vm.loadavg output %q", string(output))
}
