//go:build darwin

package worktrees

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sneat-dev/wb/internal/console"
)

var (
	darwinGitExecutableOnce sync.Once
	darwinGitExecutable     string
	darwinGitExecutableErr  error
)

// platformTrustedGitExecutable resolves Apple's selected developer-tool Git
// before the sandboxed helper starts. `/usr/bin/git` is an xcrun shim that may
// create a global xcrun cache; the resolved executable keeps the child’s write
// authority limited to its explicit capability roots.
func platformTrustedGitExecutable() (string, error) {
	darwinGitExecutableOnce.Do(func() {
		darwinGitExecutable, darwinGitExecutableErr = resolveDarwinGitExecutable()
	})
	return darwinGitExecutable, darwinGitExecutableErr
}

func resolveDarwinGitExecutable() (string, error) {
	command := exec.Command("/usr/bin/xcrun", "--find", "git")
	command.Env = console.Env()
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("resolve developer Git with xcrun: %w", err)
	}
	gitExecutable := strings.TrimSpace(string(output))
	if !filepath.IsAbs(gitExecutable) {
		return "", fmt.Errorf("xcrun returned a non-absolute Git path: %q", gitExecutable)
	}
	info, err := os.Stat(gitExecutable)
	if err != nil {
		return "", fmt.Errorf("inspect developer Git %s: %w", gitExecutable, err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("developer Git is not executable: %s", gitExecutable)
	}
	return filepath.Clean(gitExecutable), nil
}
