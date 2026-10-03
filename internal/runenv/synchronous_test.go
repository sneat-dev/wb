package runenv

import (
	"strings"
	"testing"
)

func TestGovernedEnvironmentCapsChildParallelism(t *testing.T) {
	t.Parallel()
	environment := Synchronous([]string{"PATH=/bin"}, []string{"go", "test", "./..."}, "wbo-test", 2, "-mod=readonly")
	joined := strings.Join(environment, "\n")
	for _, want := range []string{
		"WB_OPERATION_ID=wbo-test",
		"WB_CPU_UNITS=2",
		"GOMAXPROCS=2",
		"NX_PARALLEL=2",
		// -p follows the allocation (units), not a hardcoded -p=1: forcing
		// single-package parallelism was the root cause of sneat-dev/wb#621
		// (a broad `go test ./...` building one package at a time on an
		// otherwise idle 18-core machine).
		"GOFLAGS=-mod=readonly -p=2",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment is missing %q:\n%s", want, joined)
		}
	}
}

func TestGovernedEnvironmentRespectsCallersExplicitDashP(t *testing.T) {
	t.Parallel()
	environment := Synchronous([]string{"PATH=/bin"}, []string{"go", "build", "./..."}, "wbo-test", 5, "-p=8")
	joined := strings.Join(environment, "\n")
	if !strings.Contains(joined, "GOFLAGS=-p=8") {
		t.Fatalf("environment does not preserve the caller's -p=8: %s", joined)
	}
	if strings.Contains(joined, "-p=5") {
		t.Fatalf("environment overrode the caller's explicit -p: %s", joined)
	}
}

func TestGovernedEnvironmentLeavesGOFLAGSAloneForNonGoCommands(t *testing.T) {
	t.Parallel()
	environment := Synchronous([]string{"PATH=/bin"}, []string{"pytest", "-q"}, "wbo-test", 2, "")
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "GOFLAGS=") {
		t.Fatalf("environment set GOFLAGS for a non-go command: %s", joined)
	}
}
