package cmdrun

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunCommandRejectsRecipeFlags(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--apply", "--", "/bin/true"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "belong to WB modes") {
		t.Errorf("stderr does not explain the incompatible flag: %s", stderr.String())
	}
}

func TestRunAsyncFlagRequiresCommandMode(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--async", "recipe-name"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--async requires command mode") {
		t.Errorf("stderr does not explain async command mode: %s", stderr.String())
	}
}

func TestRunAsyncRequiresExplicitStableWorker(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--async", "--", "go", "test"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--async requires --worker <stable-id>") || !strings.Contains(stderr.String(), "never guesses") {
		t.Errorf("stderr does not explain worker binding: %s", stderr.String())
	}
}

func TestRunWorkerFlagRequiresAsyncCommandMode(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--worker", "agent-a", "--", "go", "test"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--worker requires --async command mode") {
		t.Errorf("stderr does not explain worker binding: %s", stderr.String())
	}
}

func TestRunIdempotencyKeyRequiresAsyncCommandMode(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--idempotency-key", "retry-1", "--", "go", "test"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--idempotency-key requires --async command mode") {
		t.Errorf("stderr does not explain idempotency scope: %s", stderr.String())
	}
}

func TestRunHistoryRejectsChangedFlag(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--history", "--changed"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cannot be used with --history") {
		t.Errorf("stderr does not explain the incompatible flag: %s", stderr.String())
	}
}

func TestRunChangedCannotCombineWithAsync(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--changed", "--async", "--worker", "codex-local", "--", "go", "test"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--changed cannot be combined with --async") {
		t.Errorf("stderr = %q, want an explanation", stderr.String())
	}
}

func TestRunTargetRequiresChanged(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--target", "main", "--", "go", "test"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--target requires --changed") {
		t.Errorf("stderr = %q, want an explanation", stderr.String())
	}
}

func TestRunChangedRequiresCommandMode(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--changed", "some-recipe"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--changed and --target require command mode with run --") {
		t.Errorf("stderr = %q, want an explanation", stderr.String())
	}
}

func TestRunChangedRejectsArgsFlagInCommand(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--changed", "--target", "main", "--", "go", "test", "-args", "-v"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cannot be combined with -args/--args or a nested -- in the command") {
		t.Errorf("stderr = %q, want an explanation naming -args and --", stderr.String())
	}
}

func TestRunChangedRejectsDoubleDashArgsFlagInCommand(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--changed", "--target", "main", "--", "go", "test", "--args", "-v"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cannot be combined with -args/--args or a nested -- in the command") {
		t.Errorf("stderr = %q, want an explanation naming -args, --args and --", stderr.String())
	}
}

func TestRunChangedRejectsNestedDashDashInCommand(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--changed", "--target", "main", "--", "go", "test", "--", "-v"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cannot be combined with -args/--args or a nested -- in the command") {
		t.Errorf("stderr = %q, want an explanation naming -args and --", stderr.String())
	}
}

func TestRunQueueRejectsIncompatibleFlags(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--queue", "--apply"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cannot be used with --queue") {
		t.Errorf("stderr does not explain the incompatible flag: %s", stderr.String())
	}
}

func TestRunQuietRequiresCommandMode(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--quiet", "recipe-name"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--quiet requires command mode") {
		t.Errorf("stderr does not explain --quiet's command-mode requirement: %s", stderr.String())
	}
}
