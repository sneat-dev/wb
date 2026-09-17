package main

import (
	"errors"
	"syscall"
)

// daemonAddressInUse reports whether a listener failure means the endpoint is
// already held. It is its own condition because the right response is to name
// the endpoint and stop, not to retry or to start a second daemon.
func daemonAddressInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}
