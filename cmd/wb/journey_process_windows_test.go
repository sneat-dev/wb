//go:build windows

package main

import (
	"os"
	"os/exec"
	"strconv"
)

func terminateJourneyProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil { return err }
	return exec.Command("taskkill", "/PID", strconv.Itoa(process.Pid), "/T", "/F").Run()
}
