package daemon

import (
	"strconv"
	"strings"
	"time"
)

// processStartTolerance absorbs the rounding between how a platform reports a
// process start time and how WB records it, without being wide enough to
// accept a different process.
const processStartTolerance = 2 * time.Second

// ProcessGenerationMatches reports whether the process now holding the recorded
// PID is the generation this record was written for.
//
// The second result is whether the comparison was possible at all. A record
// with no recorded start time, or a platform that cannot observe one, yields
// known=false and leaves the caller to fall back to PID liveness and to say
// that it did. A recycled PID that *is* observable and differs is reported as
// a mismatch, because a confident "still running" about someone else's process
// is worse than an admitted unknown.
func (s State) ProcessGenerationMatches(observedStart time.Time, observed bool) (match bool, known bool) {
	if s.PID <= 0 {
		return false, true
	}
	if s.ProcessStartedAt.IsZero() || !observed {
		return false, false
	}
	delta := observedStart.Sub(s.ProcessStartedAt)
	if delta < 0 {
		delta = -delta
	}
	return delta <= processStartTolerance, true
}

// ParseProcStatStartTicks extracts the process start time from the contents of
// a Linux /proc/<pid>/stat file, in clock ticks since boot.
//
// The comm field is wrapped in parentheses and may itself contain spaces and
// parentheses, so the fields are counted from the last ')' rather than by
// splitting the whole line: a process named "my (odd) name" must not shift
// every field after it. Start time is field 22 of the record, which is the
// twentieth field after the state field that follows comm.
func ParseProcStatStartTicks(contents string) (uint64, bool) {
	close := strings.LastIndex(contents, ")")
	if close < 0 || close+1 >= len(contents) {
		return 0, false
	}
	fields := strings.Fields(contents[close+1:])
	// fields[0] is the state character (field 3). Start time is field 22, so it
	// sits at index 22-3 = 19.
	const startTimeIndex = 19
	if len(fields) <= startTimeIndex {
		return 0, false
	}
	ticks, err := strconv.ParseUint(fields[startTimeIndex], 10, 64)
	if err != nil {
		return 0, false
	}
	return ticks, true
}

// procStatClockTicksPerSecond is the unit /proc reports start times in. It is
// USER_HZ, which is fixed at 100 on Linux independently of the kernel's own HZ.
const procStatClockTicksPerSecond = 100

// ProcessStartFromProcStat converts a /proc start-tick count and the system
// boot time into an absolute start time.
func ProcessStartFromProcStat(ticks uint64, bootTime time.Time) time.Time {
	if bootTime.IsZero() {
		return time.Time{}
	}
	seconds := int64(ticks / procStatClockTicksPerSecond)
	nanos := int64(ticks%procStatClockTicksPerSecond) * (int64(time.Second) / procStatClockTicksPerSecond)
	return bootTime.Add(time.Duration(seconds)*time.Second + time.Duration(nanos))
}

// ParseProcStatBootTime extracts the btime (boot time, in Unix seconds) from
// the contents of /proc/stat.
func ParseProcStatBootTime(contents string) (time.Time, bool) {
	for _, line := range strings.Split(contents, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "btime" {
			continue
		}
		seconds, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(seconds, 0).UTC(), true
	}
	return time.Time{}, false
}
