//go:build !linux && !darwin

package machinemetrics

import (
	"errors"
	"testing"
)

func TestSourceIsUnsupportedOnThisSystem(t *testing.T) {
	t.Parallel()
	if _, err := NewSource(t.TempDir()).Read(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}
