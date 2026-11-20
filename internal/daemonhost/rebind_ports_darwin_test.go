package daemonhost

import (
	"errors"

	"golang.org/x/sys/unix"
)

func rebindFixturePortPolicy() (int, []automaticPortRange, error) {
	const key = "net.inet.ip.portrange."
	var ranges []automaticPortRange
	for _, prefix := range []string{"", "hi", "low"} {
		first, err := unix.SysctlUint32(key + prefix + "first")
		if err != nil {
			return 0, nil, err
		}
		last, err := unix.SysctlUint32(key + prefix + "last")
		if err != nil {
			return 0, nil, err
		}
		// Darwin permits descending automatic ranges.
		if min(first, last) < 1 || max(first, last) > 65535 {
			return 0, nil, errors.New("invalid Darwin automatic port bounds")
		}
		ranges = append(ranges, automaticPortRange{first: int(min(first, last)), last: int(max(first, last))})
	}
	return 1024, ranges, nil
}
