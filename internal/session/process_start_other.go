//go:build !linux

package session

import (
	"github.com/shirou/gopsutil/v4/process"
	"time"
)

func processStartTime(pid int) (time.Time, bool) {
	return processStartTimeWith(pid, func(pid int32) (int64, error) {
		// Construct a fresh observation each time: CreateTime caches on Process.
		// gopsutil uses native sysctl on Darwin and process times on Windows;
		// unavailable observations on other platforms fail closed.
		return (&process.Process{Pid: pid}).CreateTime()
	})
}
