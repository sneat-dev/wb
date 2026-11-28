//go:build !windows

package orchestrate

import "github.com/sneat-dev/wb/internal/procalive"

func operationProcessMayBeLive(pid int) bool {
	return procalive.Alive(pid)
}
