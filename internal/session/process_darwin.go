//go:build darwin

package session

import (
	"bytes"
	"encoding/binary"

	"golang.org/x/sys/unix"
)

// darwinProcessEvidenceOps keeps the two kernel observations local to one
// lookup. Tests can supply exact syscall outcomes without replacing global
// process-table behavior.
type darwinProcessEvidenceOps struct {
	kinfoProc func(int) (*unix.KinfoProc, error)
	procArgs  func(int) ([]byte, error)
}

func processEvidence(pid int) (ProcessEvidence, bool) {
	return processEvidenceWith(pid, darwinProcessEvidenceOps{
		kinfoProc: func(pid int) (*unix.KinfoProc, error) {
			return unix.SysctlKinfoProc("kern.proc.pid", pid)
		},
		procArgs: func(pid int) ([]byte, error) {
			return unix.SysctlRaw("kern.procargs2", pid)
		},
	})
}

func processEvidenceWith(pid int, ops darwinProcessEvidenceOps) (ProcessEvidence, bool) {
	if pid <= 0 {
		return ProcessEvidence{}, false
	}
	process, err := ops.kinfoProc(pid)
	if err != nil || process == nil {
		return ProcessEvidence{}, false
	}
	nameBytes := process.Proc.P_comm[:]
	if end := bytes.IndexByte(nameBytes, 0); end >= 0 {
		nameBytes = nameBytes[:end]
	}
	if len(nameBytes) == 0 {
		return ProcessEvidence{}, false
	}
	raw, err := ops.procArgs(pid)
	if err != nil {
		return ProcessEvidence{}, false
	}
	args, ok := parseDarwinProcArgs2(raw)
	if !ok {
		return ProcessEvidence{}, false
	}
	return ProcessEvidence{Executable: string(nameBytes), Args: args}, true
}

// parseDarwinProcArgs2 reads argc, the saved executable path, optional NUL
// alignment, then exactly argc terminated argv strings. Ordinary trailing
// environment data is not parsed. An empty argv[0] cannot be distinguished
// from path padding in this kernel buffer, so this follows XNU's usual
// skip-padding convention rather than claiming that ambiguous case is proven.
func parseDarwinProcArgs2(raw []byte) ([]string, bool) {
	if len(raw) < 4 {
		return nil, false
	}
	argc := int(binary.LittleEndian.Uint32(raw[:4]))
	if argc <= 0 {
		return nil, false
	}
	rest := raw[4:]
	pathEnd := bytes.IndexByte(rest, 0)
	if pathEnd <= 0 {
		return nil, false
	}
	rest = rest[pathEnd+1:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	// Even an empty argv string needs one NUL byte. Check this before
	// allocating from an untrusted kernel-reported argc.
	if argc > len(rest) {
		return nil, false
	}
	args := make([]string, 0, argc)
	for range argc {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			return nil, false
		}
		args = append(args, string(rest[:end]))
		rest = rest[end+1:]
	}
	return args, true
}
