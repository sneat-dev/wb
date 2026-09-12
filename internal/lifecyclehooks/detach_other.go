//go:build !darwin && !linux

package lifecyclehooks

import "os/exec"

func configureDetached(*exec.Cmd) {}
