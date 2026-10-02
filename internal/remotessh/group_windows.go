package remotessh

import (
	"os"
	"os/exec"
)

// ownGroup changes nothing on Windows: the end of the context kills the process
// itself (ssh.exe), which is exec's default. A helper that process started, a
// ProxyCommand say, is not killed with it and may survive.
func ownGroup(*exec.Cmd) {}

// inspectPath reads the mode of path. Windows does not say who owns it.
func inspectPath(path string) (pathFacts, error) {
	info, err := os.Stat(path)
	if err != nil {
		return pathFacts{}, err
	}
	return pathFacts{mode: info.Mode(), owner: -1}, nil
}
