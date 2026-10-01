package remotessh

import "os/exec"

// ownGroup changes nothing on Windows: the end of the context kills the process
// itself (ssh.exe), which is exec's default. A helper that process started, a
// ProxyCommand say, is not killed with it and may survive.
func ownGroup(*exec.Cmd) {}
