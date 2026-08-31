package deps

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGoCommandEnvironmentExtendsPrivateModuleSettings(t *testing.T) {
	t.Parallel()
	environment := goCommandEnvironment([]string{
		"PATH=/bin",
		"GOPRIVATE=example.org/internal",
		"GONOPROXY=example.net/private",
		"GONOSUMDB=example.com/legacy",
	}, []string{"github.com/sneat-co", "github.com/bots-go-framework,example.org/internal", "github.com/sneat-co"})

	values := environmentValues(environment)
	if got, want := values["GOPRIVATE"], "example.org/internal,github.com/sneat-co,github.com/bots-go-framework"; got != want {
		t.Fatalf("GOPRIVATE = %q, want %q", got, want)
	}
	if got, want := values["GONOPROXY"], "example.net/private,github.com/sneat-co,github.com/bots-go-framework,example.org/internal"; got != want {
		t.Fatalf("GONOPROXY = %q, want %q", got, want)
	}
	if got, want := values["GONOSUMDB"], "example.com/legacy,github.com/sneat-co,github.com/bots-go-framework,example.org/internal"; got != want {
		t.Fatalf("GONOSUMDB = %q, want %q", got, want)
	}
	if got, want := values["PATH"], "/bin"; got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
}

func TestGoCommandEnvironmentLeavesInheritedSettingsUntouchedWithoutPatterns(t *testing.T) {
	t.Parallel()
	base := []string{"GOPRIVATE=example.org/internal", "PATH=/bin"}
	got := goCommandEnvironment(base, nil)
	if strings.Join(got, "\n") != strings.Join(base, "\n") {
		t.Fatalf("environment = %q, want %q", got, base)
	}
}

func environmentValues(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = value
		}
	}
	return values
}

func TestRunCommandBoundsLeakedPackageManagerOutputPipe(t *testing.T) {
	// Keep this regression bounded while preserving the production five-second
	// protection interval. This test is deliberately not parallel because it
	// changes a package-private process-launch setting.
	previousWaitDelay := commandPipeDrainWaitDelay
	commandPipeDrainWaitDelay = 25 * time.Millisecond
	t.Cleanup(func() { commandPipeDrainWaitDelay = previousWaitDelay })

	directory := t.TempDir()
	pidFile := filepath.Join(directory, "descendant.pid")
	launcher := filepath.Join(directory, "launcher.sh")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nsleep 2 &\necho $! > \"$1\"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		contents, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(contents)))
		if err == nil {
			if process, processErr := os.FindProcess(pid); processErr == nil {
				_ = process.Kill()
			}
		}
	})

	started := time.Now()
	_, _, err := runCommand(context.Background(), time.Second, 0, directory, launcher, pidFile)
	// Leave a small scheduler allowance around the one-second command timeout.
	// The regression boundary is the two-second descendant-held pipe, not a
	// sub-millisecond assertion about when a loaded runner observes a deadline.
	if elapsed := time.Since(started); elapsed > 1100*time.Millisecond {
		t.Fatalf("runCommand waited %s for a descendant-held output pipe", elapsed)
	}
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("error = %v, want wrapped exec.ErrWaitDelay", err)
	}
}
