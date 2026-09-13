//go:build !darwin && !linux && !windows

package lifecyclehooks

import "os/exec"

func configureDetached(*exec.Cmd) {}
