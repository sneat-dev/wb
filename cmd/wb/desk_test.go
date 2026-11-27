package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDeskNamesTheMachineAsTheFleetDocumentDoes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	configured := filepath.Join(dir, "wb.yaml")
	if err := os.WriteFile(configured, []byte("remote:\n  provider: git\n  repo: acme/wb-state\n  machine: studio-mac\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	host := func() (string, error) { return "host-1", nil }
	if got := deskMachineName(configured, host); got != "studio-mac" {
		t.Fatalf("remote.machine wins, got %q", got)
	}
	missing := filepath.Join(dir, "none.yaml")
	if got := deskMachineName(missing, host); got != "host-1" {
		t.Fatalf("then the hostname, got %q", got)
	}
	if got := deskMachineName(missing, func() (string, error) { return "", errors.New("no name") }); got != "local" {
		t.Fatalf("then local, got %q", got)
	}
	if deps := defaultDeskDependencies(); deps.MachineName(missing) == "" || deps.WBVersion == "" {
		t.Fatalf("default dependencies = %+v", deps)
	} else if id, err := deps.MachineID(t.TempDir()); err != nil || len(id) != 32 {
		t.Fatalf("machine id = %q, %v", id, err)
	}
	if command := newDeskCmd(&invocation{}); command.Name() != "desk" || len(command.Commands()) != 4 {
		t.Fatalf("wb desk = %v", command.Commands())
	}
}
