package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/streamrun"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestCwCovStreamLeaseIdentity(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(configPath, []byte("remote:\n  provider: git\n  repo: acme/state\n  machine: cw-machine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dependencies := remoteDeps{
		configPath: configPath,
		login:      func() (string, error) { return "cw-login", nil },
		open:       func(remotestate.Config, string) (remotestate.Provider, error) { return nil, nil },
	}
	login, machine := newStreamService(dependencies).Identity(t.TempDir())
	if login != "cw-login" || machine != "cw-machine" {
		t.Fatalf("lease identity = (%q, %q)", login, machine)
	}

	// An unconfigured store degrades to an unattributed lease rather than
	// failing, so a fleet that never opted into wb remote still gets streams.
	dependencies.configPath = filepath.Join(t.TempDir(), "absent.yaml")
	if login, machine := newStreamService(dependencies).Identity(t.TempDir()); login != "" || machine != "" {
		t.Fatalf("unconfigured identity = (%q, %q), want empty", login, machine)
	}

	// A failing login resolver leaves the login empty but keeps the machine.
	dependencies.configPath = configPath
	dependencies.login = func() (string, error) { return "", context.DeadlineExceeded }
	if login, machine := newStreamService(dependencies).Identity(t.TempDir()); login != "" || machine != "cw-machine" {
		t.Fatalf("failed login identity = (%q, %q)", login, machine)
	}
}

func TestCwCovStreamSessionIdentityWithoutRegistration(t *testing.T) {
	projects := t.TempDir()
	t.Setenv(wbhome.EnvOverride, projects)
	installSessionResolver(testInvocation(t, projects))
	t.Cleanup(func() { worktrees.SetSessionResolver(nil) })
	if identity := streamrun.SessionIdentity(); identity != "" {
		t.Fatalf("streamrun.SessionIdentity() = %q, want empty with no registered session", identity)
	}
}
