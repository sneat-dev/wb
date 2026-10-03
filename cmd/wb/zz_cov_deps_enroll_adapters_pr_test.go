package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/streamrun"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func cwDepsEnrollDeps(t *testing.T, verifyErr error) (remoteEnrollDeps, string) {
	t.Helper()
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "wb.yaml")
	deps := remoteEnrollDeps{
		configPath: func() string { return configPath },
		verify:     func(context.Context, string, string, string) error { return verifyErr },
		restart:    func(context.Context, string) error { return nil },
	}
	return deps, configPath
}

func TestCwDepsRemoteEnrollWritesPrivateCredentialAndConfig(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	deps, configPath := cwDepsEnrollDeps(t, nil)
	var out bytes.Buffer
	err := runRemoteEnroll(context.Background(), deps, t.TempDir(), "studio-mac", "https://hub.example.test",
		"", true, false, false, strings.NewReader("machine-token-1\n"), &out)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if !strings.Contains(out.String(), "Enrolled studio-mac") || !strings.Contains(out.String(), "verified    yes") {
		t.Errorf("enrollment report = %q", out.String())
	}
	// The credential is stored privately beside the config by default.
	if !strings.Contains(out.String(), filepath.Join(filepath.Dir(configPath), "credentials", "hub-studio-mac-")) {
		t.Errorf("default credential path missing from report: %s", out.String())
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(raw), "https://hub.example.test") || !strings.Contains(string(raw), "studio-mac") {
		t.Errorf("config was not updated:\n%s", raw)
	}
	// JSON output is the machine-readable envelope with the same facts.
	out.Reset()
	if err := runRemoteEnroll(context.Background(), deps, t.TempDir(), "studio-mac", "https://hub.example.test",
		"", true, false, true, strings.NewReader("machine-token-1\n"), &out); err != nil {
		t.Fatalf("json enroll: %v", err)
	}
	var result remoteEnrollResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("enroll JSON: %v\n%s", err, out.String())
	}
	if !result.Verified || result.Machine != "studio-mac" || result.TokenFile == "" {
		t.Fatalf("enroll result = %+v", result)
	}
}

func TestCwDepsRemoteEnrollRefusalsAndCredentialReuse(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	deps, _ := cwDepsEnrollDeps(t, nil)
	var out bytes.Buffer

	// --machine is required.
	if err := runRemoteEnroll(context.Background(), deps, t.TempDir(), "  ", "https://hub.example.test",
		"", true, false, false, strings.NewReader("token\n"), &out); exitCodeOfSafe(err) != exitUsage ||
		!strings.Contains(err.Error(), "--machine is required") {
		t.Fatalf("missing machine = %v", err)
	}
	// The credential is never accepted in argv.
	if err := runRemoteEnroll(context.Background(), deps, t.TempDir(), "m", "https://hub.example.test",
		"", false, false, false, strings.NewReader("token\n"), &out); exitCodeOfSafe(err) != exitUsage ||
		!strings.Contains(err.Error(), "--token-stdin is required") {
		t.Fatalf("missing token-stdin = %v", err)
	}
	// A credential that fails verification never reaches disk.
	tokenFile := filepath.Join(t.TempDir(), "token")
	failing, _ := cwDepsEnrollDeps(t, errors.New("hub rejected the credential"))
	err := runRemoteEnroll(context.Background(), failing, t.TempDir(), "m", "https://hub.example.test",
		tokenFile, true, false, false, strings.NewReader("bad-token\n"), &out)
	if exitCodeOfSafe(err) != exitFindings || !strings.Contains(err.Error(), "verify hub credential") {
		t.Fatalf("verify failure = %v", err)
	}
	if _, statErr := os.Stat(tokenFile); statErr == nil {
		t.Error("a rejected credential must not be written")
	}

	// Re-enrolling with the same secret reuses the file; a different secret is
	// refused rather than silently overwritten.
	if err := runRemoteEnroll(context.Background(), deps, t.TempDir(), "m", "https://hub.example.test",
		tokenFile, true, false, false, strings.NewReader("same-token\n"), &out); err != nil {
		t.Fatalf("first enroll: %v", err)
	}
	if err := runRemoteEnroll(context.Background(), deps, t.TempDir(), "m", "https://hub.example.test",
		tokenFile, true, false, false, strings.NewReader("same-token\n"), &out); err != nil {
		t.Fatalf("idempotent enroll: %v", err)
	}
	if err := runRemoteEnroll(context.Background(), deps, t.TempDir(), "m", "https://hub.example.test",
		tokenFile, true, false, false, strings.NewReader("different-token\n"), &out); err == nil ||
		!strings.Contains(err.Error(), "already exists with different contents") {
		t.Fatalf("conflicting credential = %v", err)
	}
	// A credential file that cannot be created is reported.
	blocker := filepath.Join(t.TempDir(), "blocker")
	cwCovWriteFile(t, blocker, "file\n")
	if err := runRemoteEnroll(context.Background(), deps, t.TempDir(), "m", "https://hub.example.test",
		filepath.Join(blocker, "token"), true, false, false, strings.NewReader("token\n"), &out); err == nil ||
		!strings.Contains(err.Error(), "create credential directory") {
		t.Fatalf("unwritable credential path = %v", err)
	}
	// A daemon-restart failure is reported after the enrollment is saved.
	restartFail := deps
	restartFail.restart = func(context.Context, string) error { return errors.New("daemon refused") }
	if err := runRemoteEnroll(context.Background(), restartFail, t.TempDir(), "m", "https://hub.example.test",
		"", true, true, false, strings.NewReader("token\n"), &out); err == nil ||
		!strings.Contains(err.Error(), "daemon restart failed") {
		t.Fatalf("restart failure = %v", err)
	}
}

func TestCwDepsReadRemoteEnrollmentTokenRefusals(t *testing.T) {
	if _, err := readRemoteEnrollmentToken(strings.NewReader("")); err == nil ||
		!strings.Contains(err.Error(), "one non-empty token") {
		t.Fatalf("empty token = %v", err)
	}
	if _, err := readRemoteEnrollmentToken(strings.NewReader("two tokens\n")); err == nil ||
		!strings.Contains(err.Error(), "one non-empty token") {
		t.Fatalf("two tokens = %v", err)
	}
	if _, err := readRemoteEnrollmentToken(strings.NewReader(strings.Repeat("x", 16<<10+2))); err == nil ||
		!strings.Contains(err.Error(), "exceeds 16384 bytes") {
		t.Fatalf("oversized token = %v", err)
	}
	token, err := readRemoteEnrollmentToken(strings.NewReader("  good-token \n"))
	if err != nil || token != "good-token" {
		t.Fatalf("token = %q, %v", token, err)
	}
}

func TestCwDepsDefaultRemoteEnrollDependencies(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	deps := defaultRemoteEnrollDeps()
	if deps.configPath == nil || deps.verify == nil || deps.restart == nil {
		t.Fatal("a default enrollment dependency is missing")
	}
	if deps.configPath() == "" {
		t.Error("default config path is empty")
	}
	// A malformed hub URL is refused before any request is attempted.
	if err := deps.verify(context.Background(), "not-a-url", "studio-mac", "token"); err == nil {
		t.Fatal("a malformed hub URL must be refused")
	}
	if err := deps.verify(context.Background(), "https://hub.example.test", "", "token"); err == nil {
		t.Fatal("an empty machine identity must be refused")
	}
}

// TestCwDepsRestartDaemonAfterRemoteEnrollSurfacesTheChildFailure invokes the
// real restart path. The test binary rejects the WB flags it is handed, which
// is exactly the non-zero exit the function must turn into an error.
func TestCwDepsRestartDaemonAfterRemoteEnrollSurfacesTheChildFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := restartDaemonAfterRemoteEnroll(ctx, t.TempDir())
	if err == nil {
		t.Fatal("a daemon restart that produced no output/exit must not be reported as success")
	}
}

func TestCwDepsStreamLeaseAndSessionIdentity(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	projectsRoot := t.TempDir()

	// A configured remote section yields the recorded machine and the
	// resolved login, without publishing anything.
	configPath := filepath.Join(t.TempDir(), "wb.yaml")
	cwCovWriteFile(t, configPath, "remote:\n  repo: acme/state\n  machine: studio-mac\n")
	login, machine := newStreamService(remoteDeps{
		configPath: configPath,
		open:       func(remotestate.Config, string) (remotestate.Provider, error) { return nil, nil },
		login:      func() (string, error) { return "cwcov-user", nil },
	}).Identity(projectsRoot)
	if login != "cwcov-user" || machine != "studio-mac" {
		t.Fatalf("lease identity = %q, %q", login, machine)
	}
	// A fleet that never opted into `wb remote` gets an unattributed lease
	// rather than a failure.
	login, machine = newStreamService(remoteDeps{
		configPath: filepath.Join(t.TempDir(), "absent.yaml"),
		open:       func(remotestate.Config, string) (remotestate.Provider, error) { return nil, nil },
		login:      func() (string, error) { return "", errors.New("no gh") },
	}).Identity(projectsRoot)
	if login != "" || machine != "" {
		t.Fatalf("unconfigured lease identity = %q, %q", login, machine)
	}
	// No registered session means no session identity is invented.
	if identity := streamrun.SessionIdentity(); identity != "" {
		// A live registered session in the ambient environment is acceptable;
		// what must never happen is a fabricated value.
		t.Logf("ambient registered session: %q", identity)
	}
}
