package daemon

import (
	"strings"
	"testing"
)

// TestGovernedChildEnvironmentSetsGoParallelism proves the daemon's durable
// executor applies the same GOFLAGS -p=<units> rule cmd/wb/run.go's
// governedEnvironment and cmd/wb/worker.go's workerChildEnvironment do: the
// caller's own GOFLAGS is preserved, and -p=<units> is appended so package
// parallelism follows the CPU allocation instead of the whole machine
// (sneat-dev/wb#621).
func TestGovernedChildEnvironmentSetsGoParallelism(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=readonly")
	environment := governedChildEnvironment([]string{"PATH=/bin"}, []string{"go", "test", "./..."}, nil, "wbo-test", 4)
	joined := strings.Join(environment, "\n")
	for _, want := range []string{
		"WB_OPERATION_ID=wbo-test",
		"WB_CPU_UNITS=4",
		"GOMAXPROCS=4",
		"NX_PARALLEL=4",
		"GOFLAGS=-mod=readonly -p=4",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment is missing %q:\n%s", want, joined)
		}
	}
}

// TestGovernedChildEnvironmentRespectsCallersExplicitDashP proves an
// explicit caller -p wins over the derived one.
func TestGovernedChildEnvironmentRespectsCallersExplicitDashP(t *testing.T) {
	t.Setenv("GOFLAGS", "-p=8")
	environment := governedChildEnvironment([]string{"PATH=/bin"}, []string{"go", "build", "./..."}, nil, "wbo-test", 4)
	joined := strings.Join(environment, "\n")
	if !strings.Contains(joined, "GOFLAGS=-p=8") {
		t.Fatalf("environment does not preserve the caller's -p=8: %s", joined)
	}
	if strings.Contains(joined, "-p=4") {
		t.Fatalf("environment overrode the caller's explicit -p: %s", joined)
	}
}

// TestGovernedChildEnvironmentLeavesGOFLAGSAloneForNonGoCommands proves the
// GOFLAGS injection is scoped to the go tool.
func TestGovernedChildEnvironmentLeavesGOFLAGSAloneForNonGoCommands(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	environment := governedChildEnvironment([]string{"PATH=/bin"}, []string{"pytest", "-q"}, nil, "wbo-test", 2)
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "GOFLAGS=") {
		t.Fatalf("environment set GOFLAGS for a non-go command: %s", joined)
	}
}
