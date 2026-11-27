//go:build !linux

package diskprune

import "errors"

// goProcesses cannot be listed without /proc; the cache is then never
// cleared.
func goProcesses() ([]Process, error) {
	return nil, errors.New("this platform has no /proc to read")
}
