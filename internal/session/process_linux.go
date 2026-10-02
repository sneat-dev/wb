//go:build linux

package session

import (
	"os"
)

func processEvidence(pid int) (ProcessEvidence, bool) {
	return processEvidenceFromProc(pid, os.Readlink, os.ReadFile)
}
