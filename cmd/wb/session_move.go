package main

import (
	"github.com/sneat-dev/wb/internal/sessionrun"
)

func newSessionMoveService(_ *invocation) *sessionrun.MoveService {
	return sessionrun.NewMove(sessionrun.DefaultMoveDependencies())
}
