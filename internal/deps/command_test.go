package deps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	leakedOutputPipeHelperEnv  = "WB_TEST_LEAKED_OUTPUT_PIPE_HELPER"
	leakedOutputPipePIDFileEnv = "WB_TEST_LEAKED_OUTPUT_PIPE_PID_FILE"
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

	environment := append(os.Environ(),
		leakedOutputPipeHelperEnv+"=1",
		leakedOutputPipePIDFileEnv+"="+pidFile,
	)
	started := time.Now()
	_, _, err := runCommandWithEnv(
		context.Background(),
		5*time.Second,
		0,
		directory,
		environment,
		os.Args[0],
		"-test.run=^TestRunCommandLeakedOutputPipeHelper$",
	)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("runCommand waited %s for a descendant-held output pipe", elapsed)
	}
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("error = %v, want wrapped exec.ErrWaitDelay", err)
	}
}

func TestRunCommandLeakedOutputPipeHelper(t *testing.T) {
	if os.Getenv(leakedOutputPipeHelperEnv) != "1" {
		return
	}
	pidFile := os.Getenv(leakedOutputPipePIDFileEnv)
	if pidFile == "" {
		fmt.Fprintln(os.Stderr, "missing leaked-output-pipe PID file")
		os.Exit(2)
	}
	descendant := exec.Command("sleep", "10")
	descendant.Stdout = os.Stdout
	descendant.Stderr = os.Stderr
	if err := descendant.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(descendant.Process.Pid)), 0o600); err != nil {
		_ = descendant.Process.Kill()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(4)
	}
	os.Exit(0)
}
