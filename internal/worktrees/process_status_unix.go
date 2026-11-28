//go:build !windows

package worktrees

import "github.com/sneat-dev/wb/internal/procalive"

func processStatus(pid int) error { return procalive.Probe(pid) }
func processIsDead(pid int) bool  { return !procalive.Alive(pid) }
