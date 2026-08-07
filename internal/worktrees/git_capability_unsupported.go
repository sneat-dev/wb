//go:build !darwin && !linux

package worktrees

import "fmt"

func platformGitFilesystemCapabilityAvailable() error {
	return fmt.Errorf("secure Git capability is unavailable on this platform")
}

func runPlatformGitWithFilesystemCapability(_ gitFilesystemCapability, _ string, _ []string, _ []string) int {
	return 1
}
