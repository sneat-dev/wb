//go:build linux

package procalive

import (
	"os"
	"strconv"
)

func defaultReadProcStat(pid int) ([]byte, error) {
	return os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
}
