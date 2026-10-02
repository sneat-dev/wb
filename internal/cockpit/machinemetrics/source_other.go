//go:build !linux && !darwin

package machinemetrics

// NewSource reports metrics as unsupported on this operating system.
func NewSource(string) Source { return unsupportedSource{} }
