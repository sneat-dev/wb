//go:build !darwin

package daemonhost

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
)

func rebindFixturePortPolicy() (int, []automaticPortRange, error) {
	if runtime.GOOS != "linux" {
		return 0, nil, nil
	}
	rangeText, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return 0, nil, err
	}
	startText, err := os.ReadFile("/proc/sys/net/ipv4/ip_unprivileged_port_start")
	if err != nil {
		return 0, nil, err
	}
	fields := strings.Fields(string(rangeText))
	if len(rangeText) > 128 || len(startText) > 128 || len(fields) != 2 {
		return 0, nil, errors.New("invalid Linux automatic port bounds")
	}
	first, firstErr := strconv.Atoi(fields[0])
	last, lastErr := strconv.Atoi(fields[1])
	unprivileged, startErr := strconv.Atoi(strings.TrimSpace(string(startText)))
	if firstErr != nil || lastErr != nil || startErr != nil || first < 1 || last < first || last > 65535 || unprivileged < 0 || unprivileged > 65535 {
		return 0, nil, errors.New("invalid Linux automatic port bounds")
	}
	return max(1024, unprivileged), []automaticPortRange{{first: first, last: last}}, nil
}
