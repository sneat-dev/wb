//go:build darwin

package session

import (
	"bytes"
	"encoding/binary"
	"strings"

	"golang.org/x/sys/unix"
)

func processEvidence(pid int) (ProcessEvidence, bool) {
	if pid <= 0 {
		return ProcessEvidence{}, false
	}
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || process == nil {
		return ProcessEvidence{}, false
	}
	name := strings.TrimRight(string(process.Proc.P_comm[:]), "\x00")
	if name == "" {
		return ProcessEvidence{}, false
	}
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(raw) < 4 {
		return ProcessEvidence{}, false
	}
	argc := int(binary.LittleEndian.Uint32(raw[:4]))
	if argc <= 0 {
		return ProcessEvidence{}, false
	}
	argsRaw := raw[4:]
	args := make([]string, 0, argc)
	for len(args) < argc {
		end := bytes.IndexByte(argsRaw, 0)
		if end < 0 {
			return ProcessEvidence{}, false
		}
		args = append(args, string(argsRaw[:end]))
		argsRaw = argsRaw[end+1:]
	}
	return ProcessEvidence{Executable: name, Args: args}, true
}
