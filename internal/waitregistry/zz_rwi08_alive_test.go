package waitregistry

import (
	"os"
	"testing"
)

// Alive's own pid<=0 short-circuit and its delegation to session.ProcessAlive
// for a positive pid. The process-liveness check itself only inspects the
// current process's own pid, never starting or signalling anything.
func TestRWI08AliveNonPositivePID(t *testing.T) {
	t.Parallel()

	if Alive(0) {
		t.Fatalf("Alive(0) = true, want false")
	}
	if Alive(-1) {
		t.Fatalf("Alive(-1) = true, want false")
	}
}

func TestRWI08AliveDelegatesForPositivePID(t *testing.T) {
	t.Parallel()

	if !Alive(os.Getpid()) {
		t.Fatalf("Alive(os.Getpid()) = false, want true")
	}
}
