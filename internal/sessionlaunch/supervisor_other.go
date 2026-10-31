//go:build !darwin && !linux

package sessionlaunch

import (
	"os"
	"os/exec"
)

func prepareOwnedTerminal(_ *exec.Cmd, _ *os.File) (func() error, error) {
	return func() error { return nil }, nil
}
func cleanupOwnedChild(_ *exec.Cmd) error { return nil }
