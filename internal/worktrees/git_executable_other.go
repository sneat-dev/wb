//go:build !darwin

package worktrees

import (
	"fmt"
	"os/exec"
	"path/filepath"
)

func platformTrustedGitExecutable() (string, error) {
	gitExecutable, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("locate Git before secure staging handoff: %w", err)
	}
	gitExecutable, err = filepath.Abs(gitExecutable)
	if err != nil {
		return "", fmt.Errorf("make Git path absolute before secure staging handoff: %w", err)
	}
	return gitExecutable, nil
}
