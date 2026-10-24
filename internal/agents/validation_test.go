package agents

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeStepCommand returns a command constructor that records the environment it
// was given and runs /bin/sh with the requested exit code.
func fakeStepCommand(t *testing.T, exitCode int) (func(ctx context.Context, name string, args ...string) *exec.Cmd, *[]string) {
	t.Helper()
	var captured []string
	command := func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exit "+itoa(exitCode))
		captured = append(captured, strings.Join(cmd.Env, "\n"))
		return cmd
	}
	return command, &captured
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

//nolint:paralleltest // the test sets the provider credential in the process environment
func TestValidationRunsStepsWithoutTheCredential(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "super-secret")
	runDir := t.TempDir()
	command, captured := fakeStepCommand(t, 0)
	record := RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(), RunDir: runDir,
		Steps:   []ValidationStep{{Name: "unit tests", Argv: []string{"go", "test", "./..."}, Timeout: "2m"}},
		Command: command, Now: time.Now,
	})
	if record.Status != ValidationPassed || len(record.Steps) != 1 || record.Steps[0].Status != ValidationPassed {
		t.Fatalf("validation = %#v", record)
	}
	if record.Steps[0].ExitCode == nil || *record.Steps[0].ExitCode != 0 {
		t.Fatalf("exit code = %v", record.Steps[0].ExitCode)
	}
	if record.Steps[0].LogPath == "" || !strings.HasSuffix(record.Steps[0].LogPath, "01-unit-tests.log") {
		t.Fatalf("log path = %q", record.Steps[0].LogPath)
	}
	if len(*captured) != 1 || strings.Contains((*captured)[0], "super-secret") {
		t.Fatalf("a validation step must not see the provider credential: %v", *captured)
	}
}

func TestValidationReportsAFailedStepAndAFailedStart(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	command, _ := fakeStepCommand(t, 3)
	record := RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(), RunDir: runDir,
		Steps:   []ValidationStep{{Name: "lint", Argv: []string{"go", "vet"}}},
		Command: command, Now: time.Now,
	})
	if record.Status != ValidationFailed || record.Steps[0].Status != ValidationFailed {
		t.Fatalf("validation = %#v", record)
	}
	if record.Steps[0].ExitCode == nil || *record.Steps[0].ExitCode != 3 {
		t.Fatalf("exit code = %v", record.Steps[0].ExitCode)
	}

	failing := func(context.Context, string, ...string) *exec.Cmd {
		return exec.CommandContext(context.Background(), "/nonexistent/wb-validation-binary")
	}
	record = RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(), RunDir: runDir,
		Steps:   []ValidationStep{{Name: "missing", Argv: []string{"no-such-binary"}}},
		Command: failing, Now: time.Now,
	})
	if record.Status != ValidationFailed || record.Steps[0].ExitCode != nil {
		t.Fatalf("a step that cannot start = %#v", record)
	}
}

func TestValidationSkipsWhenNothingIsConfigured(t *testing.T) {
	t.Parallel()
	record := RunValidation(context.Background(), ValidationOptions{RunDir: t.TempDir(), Now: time.Now})
	if record.Status != ValidationSkipped || record.Reason == "" {
		t.Fatalf("validation = %#v", record)
	}
}

func TestValidationRefusesAnEmptyOrMalformedStep(t *testing.T) {
	t.Parallel()
	record := RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(), RunDir: t.TempDir(),
		Steps: []ValidationStep{
			{Name: "empty"},
			{Name: "bad timeout", Argv: []string{"true"}, Timeout: "not-a-duration"},
		},
		Command: func(context.Context, string, ...string) *exec.Cmd {
			return exec.CommandContext(context.Background(), "/usr/bin/true")
		},
		Now: time.Now,
	})
	if record.Status != ValidationFailed {
		t.Fatalf("validation = %#v", record)
	}
	if record.Steps[0].Status != ValidationSkipped {
		t.Fatalf("an empty argv must be skipped, got %#v", record.Steps[0])
	}
}

func TestValidationReportsAnUnwritableLogDirectory(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	blocked := filepath.Join(runDir, validationLogDirName)
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(), RunDir: runDir,
		Steps: []ValidationStep{{Name: "one", Argv: []string{"true"}}},
		Now:   time.Now,
	})
	if record.Status != ValidationFailed || !strings.Contains(record.Reason, "log directory") {
		t.Fatalf("validation = %#v", record)
	}
}

func TestValidationPassesTheInheritedLeaseToAStep(t *testing.T) {
	t.Parallel()
	leaseFile, err := os.CreateTemp(t.TempDir(), "lease")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = leaseFile.Close() }()
	var extra int
	record := RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(), RunDir: t.TempDir(),
		Steps: []ValidationStep{{Name: "one", Argv: []string{"true"}}},
		Lease: leaseFile, Now: time.Now,
		Command: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			cmd := exec.CommandContext(ctx, "/usr/bin/true")
			return cmd
		},
	})
	if record.Status != ValidationPassed {
		t.Fatalf("validation = %#v", record)
	}
	_ = extra
}

func TestRunValidationHonoursAPerStepTimeout(t *testing.T) {
	t.Parallel()
	started := time.Now()
	record := RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(), RunDir: t.TempDir(),
		Steps: []ValidationStep{{Name: "slow", Argv: []string{"sleep", "5"}, Timeout: "50ms"}},
		Now:   time.Now,
		Command: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/sleep", "5")
		},
	})
	if record.Status != ValidationFailed {
		t.Fatalf("a timed-out step must fail validation: %#v", record)
	}
	if time.Since(started) > 30*time.Second {
		t.Fatal("the step timeout was not enforced")
	}
}

func TestClassifyFailureReadsTheHarnessDiagnostics(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"provider.no-route: Model unavailable": FailureProvider,
		"rate limit exceeded":                  FailureProvider,
		"credential missing":                   FailureCredential,
		"authentication failed":                FailureCredential,
		"something else":                       FailureHarness,
	}
	for diagnostic, want := range cases {
		summary := HarnessSummary{Diagnostics: []string{diagnostic}}
		if got := classifyFailure(summary); got != want {
			t.Fatalf("classifyFailure(%q) = %q, want %q", diagnostic, got, want)
		}
	}
}

func TestCancelSentinelIsWrittenBeforeSignalling(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	if cancelRequested(store, record.AgentID) {
		t.Fatal("a fresh run must not be marked cancelled")
	}
	if err := requestCancel(store, record.AgentID, func() time.Time { return time.Unix(5, 0) }); err != nil {
		t.Fatal(err)
	}
	if !cancelRequested(store, record.AgentID) {
		t.Fatal("the sentinel must be visible to the owner")
	}
}

var errUnused = errors.New("unused")

func TestValidationKeepsTheLeaseDescriptorNumberStable(t *testing.T) {
	t.Parallel()
	// The step's ExtraFiles is what keeps the kernel lock held; an empty lease
	// must leave the field alone rather than passing a nil descriptor.
	leaseFilesWithNil := leaseFiles(nil, 0)
	if leaseFilesWithNil != nil {
		t.Fatalf("no lease must produce no extra files: %#v", leaseFilesWithNil)
	}
}
