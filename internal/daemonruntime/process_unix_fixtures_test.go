//go:build !windows && !darwin

package daemonruntime

// startDaemonProcess refuses a Go test binary before spawning anything
// (sneat-dev/wb#622: a test previously reached this function for real and
// took a real daemon down).

// Off macOS there is no fixed-label launchd service for a start to remove, so
// the other-root check never refuses.
