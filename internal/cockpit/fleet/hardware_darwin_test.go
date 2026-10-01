package fleet

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestBootTimeIsReadFromTheKernel(t *testing.T) {
	t.Parallel()
	if bootTime().IsZero() {
		t.Error("kern.boottime is not readable on macOS")
	}
}

func TestBootTimeIsZeroWhenTheKernelRefuses(t *testing.T) { //nolint:paralleltest // it replaces a package variable
	read := sysctlTimeval
	t.Cleanup(func() { sysctlTimeval = read })
	sysctlTimeval = func(string) (*unix.Timeval, error) { return nil, errors.New("refused") }
	if !bootTime().IsZero() {
		t.Error("a refused sysctl gave a boot time")
	}
}
