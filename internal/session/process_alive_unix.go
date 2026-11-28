//go:build !windows

package session

import "github.com/sneat-dev/wb/internal/procalive"

func processAlive(pid int) bool {
	return procalive.Alive(pid)
}
