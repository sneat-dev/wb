//go:build !windows

package sessionlaunch

import "github.com/sneat-dev/wb/internal/procalive"

func processStatus(pid int) error { return procalive.Probe(pid) }
