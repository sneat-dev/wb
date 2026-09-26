package waitregistry

import (
	"os"
	"testing"
)

// Alive's own pid<=0 short-circuit and its delegation to session.ProcessAlive
// for a positive pid. The process-liveness check itself only inspects the
// current process's own pid, never starting or signalling anything.
func TestAliveNonPositivePID(t *testing.T) {
	t.Parallel()

	if DefaultAlive(0) {
		t.Fatalf("DefaultAlive(0) = true, want false")
	}
	if DefaultAlive(-1) {
		t.Fatalf("DefaultAlive(-1) = true, want false")
	}
}

func TestAliveDelegatesForPositivePID(t *testing.T) {
	t.Parallel()

	if !DefaultAlive(os.Getpid()) {
		t.Fatalf("DefaultAlive(os.Getpid()) = false, want true")
	}
}
