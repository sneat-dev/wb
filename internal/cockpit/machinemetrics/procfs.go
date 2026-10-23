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

	cpu cpuMeter
}

var errBadProc = errors.New("unexpected /proc format")

// Read reads /proc/stat, /proc/loadavg and /proc/meminfo and the projects root's
// disk, each independently: what could be read is returned with the errors of
// what could not.
func (p *procSource) Read() (Sample, error) {
	var sample Sample
	var errs []error
	if busy, all, err := p.cpuTimes(); err != nil {
		errs = append(errs, err)
	} else {
		sample.CPUPercent = p.cpu.percent(float64(busy), float64(all))
	}
	if load, err := p.load(); err != nil {
		errs = append(errs, err)
	} else {
		sample.Load1 = &load
	}
	if used, total, err := p.memory(); err != nil {
		errs = append(errs, err)
	} else {
		sample.MemoryUsedBytes, sample.MemoryTotalBytes = &used, &total
	}
	if free, total, err := p.disk(); err != nil {
		errs = append(errs, err)
	} else {
		sample.DiskFreeBytes, sample.DiskTotalBytes = &free, &total
	}
	return sample, errors.Join(errs...)
}

func (p *procSource) cpuTimes() (busy, all uint64, err error) {
	stat, err := p.readFile("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	return parseCPUTimes(string(stat))
}

func (p *procSource) load() (float64, error) {
	loadavg, err := p.readFile("/proc/loadavg")
	if err != nil {
		return 0, err
	}
	return parseLoad1(string(loadavg))
}

func (p *procSource) memory() (used, total uint64, err error) {
	meminfo, err := p.readFile("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	return parseMemInfo(string(meminfo))
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
	if err != nil || load < 0 || !finite(load) {
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
