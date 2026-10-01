//go:build windows

package fleet

import "os/exec"

// killWithDescendants leaves command with the default cancellation, which
// kills the process itself. Windows has no process group to signal here; a
// descendant that holds the output open is abandoned after the wait delay.
func killWithDescendants(*exec.Cmd) {}
