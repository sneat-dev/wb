package main

import (
	"strings"
	"testing"
)

// TestMigrateCommandRequiresTwoSourceRootsWithoutHierarchical covers
// newMigrateCmd's non-hierarchical `len(args) < 2` guard (cmd/wb/migrate.go):
// a single positional argument (just the spec path, no source root) must be
// refused before any migration runs.
func TestMigrateCommandRequiresTwoSourceRootsWithoutHierarchical(t *testing.T) {
	t.Parallel()
	cmd := newMigrateCmd(&invocation{})
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"spec.hcl"})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("migrate with one arg = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "migrate requires at least one source root") {
		t.Fatalf("migrate with one arg error = %q, want it to mention 'requires at least one source root'", err.Error())
	}
}

// TestMigrateCommandRejectsHierarchicalOnlyFlagsWithoutHierarchical
// covers the guard that refuses --commit (and its hierarchical-only
// siblings) unless --hierarchical is also set.
func TestMigrateCommandRejectsHierarchicalOnlyFlagsWithoutHierarchical(t *testing.T) {
	t.Parallel()
	cmd := newMigrateCmd(&invocation{})
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--commit", "spec.hcl", "root1"})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("migrate --commit without --hierarchical = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "require --hierarchical") {
		t.Fatalf("migrate --commit error = %q, want it to mention 'require --hierarchical'", err.Error())
	}
}
