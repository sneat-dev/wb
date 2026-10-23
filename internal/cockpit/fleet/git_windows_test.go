//go:build e2e && windows

package fleet

// processAlive is never reached on Windows: the tests that use it run a shell
// script as a stand-in for Git.
func processAlive(int) bool { return false }
