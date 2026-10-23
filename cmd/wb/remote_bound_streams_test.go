package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// The actual default config resolver is the purpose of this serial fixture.
//
//nolint:paralleltest
func TestRemoteMachinesUsesItsCurrentBoundOutput(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	clone := filepath.Join(root, "github.com", "team", "wb-state")
	origin := testenv.CloneWithOrigin(t, t.TempDir(), "wb-state", clone)
	canonical := "git@github.com:team/wb-state.git"
	testenv.Git(t, clone, "remote", "set-url", "origin", canonical)
	testenv.Git(t, clone, "config", "url."+origin+".insteadOf", canonical)
	config := wbconfig.DefaultPath()
	if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("remote:\n  repo: team/wb-state\n  machine: bound-output\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command := newRemoteCmd(&invocation{projectsRoot: root})
	var output, diagnostics bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&diagnostics)
	command.SetArgs([]string{"machines"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "MACHINE") || !strings.Contains(output.String(), "PUBLISHED_AT") {
		t.Fatalf("actual successful command bypassed bound stdout: %q", output.String())
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("unexpected diagnostics: %q", diagnostics.String())
	}
}
