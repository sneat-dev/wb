//go:build darwin

package session

import "golang.org/x/sys/unix"

// parentPID reads a process's parent through sysctl, which is where macOS
// keeps it: there is no /proc, so the Linux reader answers "unknown" for every
// process on this platform. That is not a cosmetic gap — ancestry resolution is
// the whole mechanism (WB runs as a grandchild of the agent, never as the agent
// itself), so without this a session registered on the founder's own machine
// could never be attributed to the commands it started.
func parentPID(pid int) (int, bool) {
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || process == nil {
		return 0, false
	}
	parent := int(process.Eproc.Ppid)
	if parent <= 0 {
		return 0, false
	}
	return parent, true
}
