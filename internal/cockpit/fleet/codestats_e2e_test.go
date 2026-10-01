//go:build e2e

package fleet

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
)

// fakeCodeGrapher writes a shell script standing in for the codegrapher
// command, in a temporary directory, and returns its path. The script's body
// is the test's own.
func fakeCodeGrapher(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codegrapher")
	if err := testenv.WriteExecutableFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

const statusJSON = `printf '{"initialized":true,"projectPath":"%s","fileCount":12,"nodeCount":52,"edgeCount":90,"nodesByKind":{"file":12,"function":30,"struct":10}}' "$4"`

// TestE2ECodeGrapherProviderReadsItsStatusCommand runs the provider against a
// stand-in command and checks what it asked and what it took.
func TestE2ECodeGrapherProviderReadsItsStatusCommand(t *testing.T) {
	t.Parallel()
	checkout := realTempDir(t)
	log := filepath.Join(t.TempDir(), "args")
	provider := CodeGrapherProvider{Binary: fakeCodeGrapher(t, `echo "$@" > `+log+"\n"+statusJSON)}
	got, err := provider.Statistics(t.Context(), checkout)
	if err != nil {
		t.Fatal(err)
	}
	want := ProviderStatistics{Indexed: true, Files: 12, Symbols: 40, Edges: 90, Kinds: map[string]int{"function": 30, "struct": 10}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("statistics = %+v, want %+v", got, want)
	}
	if args, _ := os.ReadFile(log); strings.TrimSpace(string(args)) != "status --json --path "+checkout {
		t.Fatalf("the command ran with %q", args)
	}
	if provider.Name() != "codegrapher" || provider.Indexer() != "codegrapher" || (CodeGrapherProvider{IndexerName: "x"}).Indexer() != "x" {
		t.Fatalf("name %q, indexer %q", provider.Name(), provider.Indexer())
	}
}

// TestE2ECodeGrapherProviderFailsWithoutLeakingWhatTheCommandPrints covers a
// missing command, a failing one that prints a secret to both streams, output
// that is not JSON, and output past the cap.
func TestE2ECodeGrapherProviderFailsWithoutLeakingWhatTheCommandPrints(t *testing.T) {
	t.Parallel()
	checkout := realTempDir(t)
	ctx := t.Context()
	if _, err := (CodeGrapherProvider{Binary: filepath.Join(t.TempDir(), "absent")}).Statistics(ctx, checkout); !errors.Is(err, errProviderUnavailable) {
		t.Errorf("a missing command: %v, want errProviderUnavailable", err)
	}
	_, err := CodeGrapherProvider{Binary: fakeCodeGrapher(t, "echo SECRET-OUT; echo SECRET-ERR /private/path >&2; exit 3")}.Statistics(ctx, checkout)
	if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "/private") {
		t.Errorf("a failing command: %v, want an error without its output", err)
	}
	if _, err := (CodeGrapherProvider{Binary: fakeCodeGrapher(t, "echo not json")}).Statistics(ctx, checkout); !errors.Is(err, errProviderOutput) {
		t.Errorf("output that is not JSON: %v, want errProviderOutput", err)
	}
	huge := fakeCodeGrapher(t, fmt.Sprintf("head -c %d /dev/zero | tr '\\000' a", maxProviderOutput+4096))
	_, err = CodeGrapherProvider{Binary: huge}.Statistics(ctx, checkout)
	if code := providerFailureCode(ctx, err); code != ErrorProviderOutput {
		t.Errorf("output past the cap: %v, code %q, want %q", err, code, ErrorProviderOutput)
	}
}

// TestE2ECodeGrapherProviderRunsWithASanitisedEnvironmentAndStopsOnItsDeadline
// proves the daemon's other variables do not reach the command and that a
// command that hangs is killed with its group when the ask's context ends.
//
//nolint:paralleltest // calls t.Setenv to plant daemon variables the provider must not forward, which Go's testing package forbids combined with t.Parallel
func TestE2ECodeGrapherProviderRunsWithASanitisedEnvironmentAndStopsOnItsDeadline(t *testing.T) {
	t.Setenv("WB_PROVIDER_TEST_SECRET", "hunter2")
	t.Setenv("LC_TEST_LOCALE", "kept")
	checkout := realTempDir(t)
	dump := filepath.Join(t.TempDir(), "env")
	provider := CodeGrapherProvider{Binary: fakeCodeGrapher(t, "env > "+dump+"\n"+statusJSON)}
	if _, err := provider.Statistics(t.Context(), checkout); err != nil {
		t.Fatal(err)
	}
	environment, _ := os.ReadFile(dump)
	if strings.Contains(string(environment), "hunter2") || !strings.Contains(string(environment), "LC_TEST_LOCALE=kept") || !strings.Contains(string(environment), "PATH=") {
		t.Fatalf("the command's environment = %q", environment)
	}

	marker := filepath.Join(t.TempDir(), "survived")
	hang := CodeGrapherProvider{Binary: fakeCodeGrapher(t, "(sleep 0.25; touch "+marker+") &\nsleep 30")}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := hang.Statistics(ctx, checkout)
	if err == nil || time.Since(started) > 10*time.Second {
		t.Fatalf("a hung command: %v after %v", err, time.Since(started))
	}
	if code := providerFailureCode(ctx, err); code != ErrorProviderTimeout {
		t.Fatalf("a hung command is %q, want %q", code, ErrorProviderTimeout)
	}
	time.Sleep(350 * time.Millisecond)
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("a descendant of the command outlived its group")
	}
}
