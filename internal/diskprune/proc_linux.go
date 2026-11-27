//go:build linux

package diskprune

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
		raw, err := os.ReadFile(filepath.Join(procRoot, entry.Name(), "comm"))
		if err != nil {
			continue
		}
		// comm is cut at 15 bytes, so the base name of argv[0] is checked
		// too: a long pkg.test binary keeps its suffix there.
		names := []string{strings.TrimSpace(string(raw))}
		if cmdline, err := os.ReadFile(filepath.Join(procRoot, entry.Name(), "cmdline")); err == nil {
			argv0, _, _ := strings.Cut(string(cmdline), "\x00")
			names = append(names, filepath.Base(argv0))
		}
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
