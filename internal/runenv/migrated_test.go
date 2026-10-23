package runenv

import (
	"strings"
	"testing"
)

func TestWorkerChildKeepsInheritedSecretsLocal(t *testing.T) {
	t.Parallel()
	environment := Worker([]string{"PATH=/bin", "GITHUB_TOKEN=worker-secret"}, []string{"/bin/true"}, "wbo-test", 2, "")
	joined := strings.Join(environment, "\n")
	for _, want := range []string{"GITHUB_TOKEN=worker-secret", "WB_OPERATION_ID=wbo-test", "WB_CPU_UNITS=2", "GOMAXPROCS=2"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("worker environment is missing %q: %s", want, joined)
		}
	}
}

func TestWorkerChildSetsGoParallelismForGoCommands(t *testing.T) {
	t.Parallel()
	environment := Worker([]string{"PATH=/bin"}, []string{"go", "test", "./..."}, "wbo-test", 4, "-mod=readonly")
	joined := strings.Join(environment, "\n")
	if !strings.Contains(joined, "GOFLAGS=-mod=readonly -p=4") {
		t.Fatalf("worker environment is missing the derived GOFLAGS: %s", joined)
	}
}

func TestWorkerChildNeverSetsGOFLAGSForNonGoCommands(t *testing.T) {
	t.Parallel()
	environment := Worker([]string{"PATH=/bin"}, []string{"pytest", "-q"}, "wbo-test", 2, "")
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "GOFLAGS=") {
		t.Fatalf("worker environment set GOFLAGS for a non-go command: %s", joined)
	}
}
func TestGovernedChildEnvironmentSetsGoParallelism(t *testing.T) {
	t.Parallel()
	environment := Daemon([]string{"PATH=/bin"}, []string{"go", "test", "./..."}, nil, "wbo-test", 4, "-mod=readonly")
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

func TestGovernedChildEnvironmentRespectsCallersExplicitDashP(t *testing.T) {
	t.Parallel()
	environment := Daemon([]string{"PATH=/bin"}, []string{"go", "build", "./..."}, nil, "wbo-test", 4, "-p=8")
	joined := strings.Join(environment, "\n")
	if !strings.Contains(joined, "GOFLAGS=-p=8") {
		t.Fatalf("environment does not preserve the caller's -p=8: %s", joined)
	}
	if strings.Contains(joined, "-p=4") {
		t.Fatalf("environment overrode the caller's explicit -p: %s", joined)
	}
}

func TestGovernedChildEnvironmentLeavesGOFLAGSAloneForNonGoCommands(t *testing.T) {
	t.Parallel()
	environment := Daemon([]string{"PATH=/bin"}, []string{"pytest", "-q"}, nil, "wbo-test", 2, "")
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "GOFLAGS=") {
		t.Fatalf("environment set GOFLAGS for a non-go command: %s", joined)
	}
}
func TestWorkerEnvironmentReplacesEveryOccurrence(t *testing.T) {
	t.Parallel()
	environment := merge(
		[]string{"PATH=/bin", "GOMAXPROCS=99", "GOMAXPROCS=98", "KEEP=1"},
		map[string]string{"GOMAXPROCS": "4", "WB_CPU_UNITS": "4"},
	)
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "GOMAXPROCS=99") || strings.Contains(joined, "GOMAXPROCS=98") {
		t.Fatalf("stale values survived: %v", environment)
	}
	if strings.Count(joined, "GOMAXPROCS=4") != 1 {
		t.Fatalf("GOMAXPROCS was not set exactly once: %v", environment)
	}
	for _, want := range []string{"KEEP=1", "PATH=/bin", "WB_CPU_UNITS=4"} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment lost %q: %v", want, environment)
		}
	}
}
