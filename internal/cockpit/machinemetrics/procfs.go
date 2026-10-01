package machinemetrics

import (
	"errors"
	"strconv"
	"strings"
)

// procSource reads Linux's /proc through injected functions, so it is tested on
// every platform. It derives the CPU percent from two readings of /proc/stat, so
// the first sample has none.
type procSource struct {
	readFile func(path string) ([]byte, error)
	disk     func() (free, total uint64, err error)

	havePrevious              bool
	previousBusy, previousAll uint64
}

var errBadProc = errors.New("unexpected /proc format")

// Read reads /proc/stat, /proc/loadavg and /proc/meminfo and the projects root's disk.
func (p *procSource) Read() (Sample, error) {
	var sample Sample
	stat, err := p.readFile("/proc/stat")
	if err != nil {
		return sample, err
	}
	busy, all, err := parseCPUTimes(string(stat))
	if err != nil {
		return sample, err
	}
	loadavg, err := p.readFile("/proc/loadavg")
	if err != nil {
		return sample, err
	}
	if sample.Load1, err = parseLoad1(string(loadavg)); err != nil {
		return sample, err
	}
	meminfo, err := p.readFile("/proc/meminfo")
	if err != nil {
		return sample, err
	}
	if sample.MemoryUsedBytes, sample.MemoryTotalBytes, err = parseMemInfo(string(meminfo)); err != nil {
		return sample, err
	}
	if sample.DiskFreeBytes, sample.DiskTotalBytes, err = p.disk(); err != nil {
		return sample, err
	}
	if p.havePrevious && all > p.previousAll && busy >= p.previousBusy {
		percent := 100 * float64(busy-p.previousBusy) / float64(all-p.previousAll)
		sample.CPUPercent = &percent
	}
	p.havePrevious, p.previousBusy, p.previousAll = true, busy, all
	return sample, nil
}

// parseCPUTimes reads the aggregate `cpu` line of /proc/stat: the busy and the
// total jiffies, where idle and iowait are not busy and guest time is already
// counted in user time.
func parseCPUTimes(stat string) (busy, all uint64, err error) {
	for _, line := range strings.Split(stat, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		var values [8]uint64
		for i := range min(len(fields)-1, len(values)) {
			if values[i], err = strconv.ParseUint(fields[i+1], 10, 64); err != nil {
				return 0, 0, errBadProc
			}
		}
		for _, value := range values {
			all += value
		}
		return all - values[3] - values[4], all, nil
	}
	return 0, 0, errBadProc
}

// parseLoad1 reads the one-minute load, the first field of /proc/loadavg.
func parseLoad1(loadavg string) (float64, error) {
	fields := strings.Fields(loadavg)
	if len(fields) == 0 {
		return 0, errBadProc
	}
	load, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || load < 0 {
		return 0, errBadProc
	}
	return load, nil
}

// parseMemInfo reads MemTotal and MemAvailable (kibibytes) of /proc/meminfo and
// returns used (total less available) and total in bytes.
func parseMemInfo(meminfo string) (used, total uint64, err error) {
	var available uint64
	var haveTotal, haveAvailable bool
	for _, line := range strings.Split(meminfo, "\n") {
		name, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 || (name != "MemTotal" && name != "MemAvailable") {
			continue
		}
		kibibytes, parseErr := strconv.ParseUint(fields[0], 10, 64)
		if parseErr != nil {
			return 0, 0, errBadProc
		}
		if name == "MemTotal" {
			total, haveTotal = kibibytes*1024, true
		} else {
			available, haveAvailable = kibibytes*1024, true
		}
	}
	if !haveTotal || !haveAvailable || available > total {
		return 0, 0, errBadProc
	}
	return total - available, total, nil
}
