package quality

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"regexp"
	"testing"
)

const qualityFixtureChildEnv = "WB_QUALITY_FIXTURE_CHILD"

func qualityFixtureChild(t *testing.T) bool {
	t.Helper()
	return os.Getenv(qualityFixtureChildEnv) == t.Name()
}

// runQualityFixtureChild reuses the immutable test binary while giving each
// environment-sensitive fixture its own process and temporary directories.
// Child selection intentionally reruns the entire top-level fixture; parent
// -run subtest suffixes are not forwarded. For a focused child invocation, set
// WB_QUALITY_FIXTURE_CHILD to the top-level name and pass the desired -run.
// Standard Go coverage counters share the parent's collection directory, so
// Go's test harness merges real child execution into the parent profile.
func runQualityFixtureChild(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if deadline, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$")
	command.Env = append(os.Environ(), qualityFixtureChildEnv+"="+t.Name())
	if coverFlag := flag.Lookup("test.gocoverdir"); testing.CoverMode() != "" && coverFlag != nil && coverFlag.Value.String() != "" {
		directory := coverFlag.Value.String()
		command.Args = append(command.Args, "-test.gocoverdir="+directory)
		command.Env = append(command.Env, "GOCOVERDIR="+directory)
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("isolated fixture: %v\n%s", err, output)
	}
}
