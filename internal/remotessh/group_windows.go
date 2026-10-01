package remotessh

import "os/exec"

// ownGroup changes nothing on Windows: the end of the context kills the process
// itself, which is exec's default.
func ownGroup(*exec.Cmd) {}
