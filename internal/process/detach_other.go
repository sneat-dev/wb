//go:build !darwin && !linux && !windows

package process

import "os/exec"

// ConfigureDetached has no platform-specific meaning here; the child is still
// started as an independent process.
func ConfigureDetached(*exec.Cmd) {}
