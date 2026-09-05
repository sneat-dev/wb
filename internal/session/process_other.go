//go:build !linux && !darwin

package session

func processEvidence(int) (ProcessEvidence, bool) { return ProcessEvidence{}, false }
