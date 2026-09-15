package agents

import (
	"errors"
	"syscall"
)

// terminationSignal is the graceful signal a stop sends before the owner's own
// bounded escalation handles anything that ignores it.
func terminationSignal() syscall.Signal { return syscall.SIGTERM }

func isNoSuchProcess(err error) bool { return errors.Is(err, syscall.ESRCH) }
