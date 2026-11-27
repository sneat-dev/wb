//go:build !linux

package diskprune

import "errors"

// goProcesses cannot be listed without /proc; the cache is then never
// cleared.
func goProcesses() ([]Process, error) {
	return nil, errors.New("this platform has no /proc to read")
}

// mountPoints has no mountinfo to read here; the device check alone keeps a
// tree on another filesystem.
func mountPoints() ([]string, error) { return nil, nil }
