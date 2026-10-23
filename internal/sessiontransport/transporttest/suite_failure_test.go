package transporttest

import (
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessiontransport"
)

const suiteFailureChildEnv = "WB_TRANSPORT_SUITE_FAILURE_CHILD"

func TestSuiteFailsBrokenTransport(t *testing.T) {
	t.Parallel()
	if os.Getenv(suiteFailureChildEnv) == "1" {
		Suite(t, func() sessiontransport.Transport { return fakeTransport{kindMismatch: true} })
		t.Fatal("Suite returned despite a broken transport")
	}
	command := exec.Command(os.Args[0], "-test.run=^TestSuiteFailsBrokenTransport$")
	command.Env = append(os.Environ(), suiteFailureChildEnv+"=1")
	// Standard Go coverage teardown collects every child counter pod in this
	// directory alongside the parent, including expected-failure processes.
	coverDirectory := ""
	if coverFlag := flag.Lookup("test.gocoverdir"); coverFlag != nil {
		coverDirectory = coverFlag.Value.String()
	}
	if testing.CoverMode() != "" && coverDirectory != "" {
		command.Args = append(command.Args, "-test.gocoverdir="+coverDirectory)
		command.Env = append(command.Env, "GOCOVERDIR="+coverDirectory)
	}
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(output), "contract suite failed") || !strings.Contains(string(output), "KindMatchesCapabilities") {
		t.Fatalf("broken transport child: %v\n%s", err, output)
	}
	if testing.CoverMode() != "" && coverDirectory != "" {
		counters, err := filepath.Glob(filepath.Join(coverDirectory, "covcounters.*"))
		if err != nil || len(counters) == 0 {
			t.Fatalf("child coverage counters = %v, %v", counters, err)
		}
	}
}
