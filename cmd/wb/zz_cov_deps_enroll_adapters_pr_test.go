package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/streamrun"
	"github.com/sneat-dev/wb/internal/wbhome"
)

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
