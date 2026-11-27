//go:build linux

package diskprune

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// goTools are the command names of the Go toolchain and the long-running
// tools that read the build cache.
var goTools = map[string]bool{
	"go": true, "compile": true, "link": true, "asm": true, "cgo": true, "vet": true, "cover": true,
	"gopls": true, "dlv": true, "golangci-lint": true, "staticcheck": true, "govulncheck": true,
}

// goProcessesIn lists the Go tools running on the machine, from every
// process's comm in procRoot. A /proc that cannot be listed is an error:
// prune then cannot tell, and does not clear the cache.
func goProcessesIn(procRoot string) ([]Process, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	var processes []Process
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		// A process that exits meanwhile has no comm: it is not running.
		// Any other read failure means prune cannot tell, and says so.
		raw, err := os.ReadFile(filepath.Join(procRoot, entry.Name(), "comm"))
		if gone(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		// comm is cut at 15 bytes, so the base name of argv[0] is checked
		// too: a long pkg.test binary keeps its suffix there.
		names := []string{strings.TrimSpace(string(raw))}
		cmdline, err := os.ReadFile(filepath.Join(procRoot, entry.Name(), "cmdline"))
		if err != nil && !gone(err) {
			return nil, err
		}
		argv0, _, _ := strings.Cut(string(cmdline), "\x00")
		names = append(names, filepath.Base(argv0))
		for _, name := range names {
			if goTools[name] || strings.HasSuffix(name, ".test") {
				processes = append(processes, Process{PID: pid, Command: name})
				break
			}
		}
	}
	return processes, nil
}

func goProcesses() ([]Process, error) { return goProcessesIn("/proc") }

// gone reports a read that failed because the process exited.
func gone(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

// mountPointsIn lists every mount point in a mountinfo listing, with the
// kernel's octal escapes (\040 for a space) decoded.
func mountPointsIn(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var points []string
	for _, line := range strings.Split(string(raw), "\n") {
		// Split on the ASCII space the kernel uses between fields: any other
		// whitespace (a non-breaking space) belongs to the path itself.
		if fields := strings.Split(line, " "); len(fields) >= 5 {
			points = append(points, unescapeOctal(fields[4]))
		}
	}
	return points, nil
}

func unescapeOctal(field string) string {
	var out strings.Builder
	for i := 0; i < len(field); i++ {
		if field[i] == '\\' && i+3 < len(field) {
			if value, err := strconv.ParseUint(field[i+1:i+4], 8, 8); err == nil {
				out.WriteByte(byte(value))
				i += 3
				continue
			}
		}
		out.WriteByte(field[i])
	}
	return out.String()
}

func mountPoints() ([]string, error) { return mountPointsIn("/proc/self/mountinfo") }
