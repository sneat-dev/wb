package testenv

import (
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const isolationFailureChildEnv = "WB_ISOLATION_FAILURE_CHILD"

//nolint:paralleltest // Environment mutations run exclusively in the isolated self-reexec child; parent and subtests are parallel.
func TestIsolateReportsEnvironmentFailures(t *testing.T) {
	if step := os.Getenv(isolationFailureChildEnv); step != "" {
		t.Setenv("WB_AGENT_ID", "original-agent")
		failure := errors.New("environment operation failed")
		isolateEnvironment(t, func(name string) error {
			if step == "unset" && name == "WB_AGENT_ID" {
				return failure
			}
			return os.Unsetenv(name)
		}, func(name, value string) error {
			if step == "restore" && name == "WB_AGENT_ID" {
				return failure
			}
			return os.Setenv(name, value)
		})
		return // The restore failure is reported during Cleanup.
	}
	t.Parallel()
	for _, step := range []string{"unset", "restore"} {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			command := exec.Command(os.Args[0], "-test.run=^TestIsolateReportsEnvironmentFailures$")
			command.Env = append(os.Environ(), isolationFailureChildEnv+"="+step)
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
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(output), "testenv.Isolate: "+step) || !strings.Contains(string(output), "environment operation failed") {
				t.Fatalf("%s child = %v\n%s", step, err, output)
			}
			if testing.CoverMode() != "" && coverDirectory != "" {
				counters, err := filepath.Glob(filepath.Join(coverDirectory, "covcounters.*"))
				if err != nil || len(counters) == 0 {
					t.Fatalf("child counters = %v, %v", counters, err)
				}
			}
		})
	}
}
