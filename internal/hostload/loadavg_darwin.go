//go:build darwin

package hostload

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/sneat-dev/wb/internal/runner"
)

// readLoadAvg1 reads the 1-minute load average via `sysctl -n vm.loadavg`,
// which prints e.g. "{ 2.34 2.10 1.98 }". Shelling out avoids parsing the
// raw fixed-point struct.loadavg sysctl layout by hand.
func readLoadAvg1() (float64, error) {
	return readLoadAvg1WithRunner(nil)
}

// readLoadAvg1WithRunner is readLoadAvg1's testable core; see hostload.go's
// resolveRunner doc on r. System's Reader type takes no context, so this
// call (like the exec.Command it replaces) has none of its own to honor —
// context.Background() matches the original's unbounded call exactly.
func readLoadAvg1WithRunner(r runner.Runner) (float64, error) {
	result, err := resolveRunner(r).Run(context.Background(), "", "sysctl", "-n", "vm.loadavg")
	if err != nil {
		return 0, fmt.Errorf("read vm.loadavg: %w", err)
	}
	output := result.Stdout
	fields := strings.Fields(strings.TrimSpace(output))
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
	return 0, fmt.Errorf("parse vm.loadavg output %q", output)
}
