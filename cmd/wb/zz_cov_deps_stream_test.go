package main

import (
	"errors"
)

// exitCodeOfSafe reports the WB exit code implied by an error, treating a
// plain error as findings.
func exitCodeOfSafe(err error) int {
	if err == nil {
		return exitOK
	}
	var exit *exitError
	if errors.As(err, &exit) {
		return exit.code
	}
	return exitFindings
}
