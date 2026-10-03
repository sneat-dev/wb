package wbexec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

func TestResolveWBExecutableForHookPrefersBareNameWhenPathMatches(t *testing.T) {
	binDir := t.TempDir()
	self := filepath.Join(t.TempDir(), "wb-binary")
	if err := testenv.WriteExecutableFile(self, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write self: %v", err)
	}
	onPath := filepath.Join(binDir, "wb")
	if err := os.Symlink(self, onPath); err != nil {
		t.Fatalf("symlink onto PATH: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if got := ResolveGovernorExecutable(self); got != "" {
		t.Fatalf("ResolveGovernorExecutable(%q) = %q, want \"\" (bare wb)", self, got)
	}
}
func TestResolveWBExecutableForHookKeepsAbsolutePathWhenDifferent(t *testing.T) {
	binDir := t.TempDir()
	self := filepath.Join(t.TempDir(), "wb-binary")
	if err := testenv.WriteExecutableFile(self, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write self: %v", err)
	}
	other := filepath.Join(binDir, "wb")
	if err := testenv.WriteExecutableFile(other, []byte("#!/bin/sh\necho different\n"), 0o755); err != nil {
		t.Fatalf("write a different wb on PATH: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if got := ResolveGovernorExecutable(self); got != self {
		t.Fatalf("ResolveGovernorExecutable(%q) = %q, want %q", self, got, self)
	}

	t.Setenv("PATH", "")
	if got := ResolveGovernorExecutable(self); got != self {
		t.Fatalf("ResolveGovernorExecutable(%q) with no PATH match = %q, want %q", self, got, self)
	}
}
func TestResolveWBExecutableForHookHandlesEmptyAndUnstattableSelf(t *testing.T) {
	if got := ResolveGovernorExecutable(""); got != "" {
		t.Fatalf("ResolveGovernorExecutable(\"\") = %q, want \"\"", got)
	}

	binDir := t.TempDir()
	onPath := filepath.Join(binDir, "wb")
	if err := testenv.WriteExecutableFile(onPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write a wb on PATH: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if got := ResolveGovernorExecutable(missing); got != missing {
		t.Fatalf("ResolveGovernorExecutable(%q) = %q, want %q (self unchanged)", missing, got, missing)
	}
}
