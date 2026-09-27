//go:build e2e

package runner_test

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
)

//nolint:paralleltest // This helper exits the test process in child mode and must run directly under -test.run.
func TestCombinedCaptureHelperProcess(t *testing.T) {
	if os.Getenv("WB_RUNNER_COMBINED_HELPER") != "1" {
		return
	}
	for _, output := range []struct {
		file *os.File
		text string
	}{
		{os.Stdout, "out-1\n"},
		{os.Stderr, "err-1\n"},
		{os.Stdout, "out-2\n"},
		{os.Stderr, "err-2\n"},
	} {
		if _, err := output.file.WriteString(output.text); err != nil {
			os.Exit(9)
		}
	}
	code, _ := strconv.Atoi(os.Getenv("WB_RUNNER_COMBINED_EXIT"))
	os.Exit(code)
}

func TestRealRunOptsCombinedCaptureKeepsOutputOnFailure(t *testing.T) {
	t.Parallel()
	opts := runner.RunOptions{
		Env:             append(os.Environ(), "WB_RUNNER_COMBINED_HELPER=1", "WB_RUNNER_COMBINED_EXIT=7"),
		CaptureCombined: true,
	}
	result, err := runner.New().RunOpts(context.Background(), t.TempDir(), opts,
		os.Args[0], "-test.run=^TestCombinedCaptureHelperProcess$")
	if err == nil {
		t.Fatal("RunOpts succeeded for a child that exited 7")
	}
	if result.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7", result.ExitCode)
	}
	if want := "out-1\nerr-1\nout-2\nerr-2\n"; result.CombinedOutput != want {
		t.Fatalf("CombinedOutput = %q, want %q", result.CombinedOutput, want)
	}
	if result.Stdout != "" || result.Stderr != "" {
		t.Fatalf("separate streams populated during combined capture: %+v", result)
	}
}

func TestRealRunOptsCombinedCapturePreservesAlternatingStreams(t *testing.T) {
	t.Parallel()
	opts := runner.RunOptions{
		Env:             append(os.Environ(), "WB_RUNNER_COMBINED_HELPER=1", "WB_RUNNER_COMBINED_EXIT=0"),
		CaptureCombined: true,
	}
	result, err := runner.New().RunOpts(context.Background(), t.TempDir(), opts,
		os.Args[0], "-test.run=^TestCombinedCaptureHelperProcess$")
	if err != nil {
		t.Fatalf("RunOpts: %v", err)
	}
	if want := "out-1\nerr-1\nout-2\nerr-2\n"; result.CombinedOutput != want {
		t.Fatalf("CombinedOutput = %q, want %q", result.CombinedOutput, want)
	}
	if result.Stdout != "" || result.Stderr != "" || result.ExitCode != 0 {
		t.Fatalf("combined capture result = %+v", result)
	}
}
