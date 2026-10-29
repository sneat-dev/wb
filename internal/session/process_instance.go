package session

import "time"

// state compares a recorded session with the process now occupying its PID.
// StartedAt is caller-supplied semantic time and supplies no process identity.
func state(record Record) string {
	return recordProcessState(record, processAlive, processStartTime)
}

func recordProcessState(record Record, alive func(int) bool, start func(int) (time.Time, bool)) string {
	if !alive(record.PID) {
		return StateGone
	}
	// A live-PID legacy declaration cannot prove which process registered it.
	if record.ProcessStartedAt.IsZero() {
		return StateUnknown
	}
	observed, known := start(record.PID)
	if !known || observed.IsZero() {
		return StateUnknown
	}
	if observed.Equal(record.ProcessStartedAt) {
		return StateLive
	}
	return StateGone
}

func processStartTimeWith(pid int, read func(int32) (int64, error)) (time.Time, bool) {
	if pid <= 0 || int(int32(pid)) != pid {
		return time.Time{}, false
	}
	milliseconds, err := read(int32(pid))
	if err != nil || milliseconds <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(milliseconds).UTC(), true
}
