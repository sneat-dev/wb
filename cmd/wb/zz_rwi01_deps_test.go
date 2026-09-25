package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/deps"
)

// TestRwi01DepsDriftFleetWithRepositoryPathIsRejected covers the early
// options.fleet && len(args) == 1 refusal in `wb deps drift`: a repository
// path is meaningless with --fleet (which selects its own set), and the
// command must reject before touching any repository.
func TestRwi01DepsDriftFleetWithRepositoryPathIsRejected(t *testing.T) {
	inv := &invocation{}
	_, _, err := cwCovExec(t, t.TempDir(), func() *cobra.Command { return newDepsDriftCmd(inv) }, "--fleet", "some/path")
	if err == nil || !strings.Contains(err.Error(), "repository-path cannot be used with --fleet") {
		t.Fatalf("wb deps drift --fleet some/path: err = %v, want the repository-path/--fleet refusal", err)
	}
}

// TestRwi01DependencyOptionsNoVerifyForcesValidationModeNone covers
// dependencyOptions' options.noVerify branch: --no-verify must force
// ValidationModeNone regardless of what --validation said.
func TestRwi01DependencyOptionsNoVerifyForcesValidationModeNone(t *testing.T) {
	inv := &invocation{projectsRoot: "/tmp/does-not-matter"}
	got := dependencyOptions(inv, depsSetOptions{noVerify: true, validation: string(deps.ValidationModeFull)}, nil)
	if got.ValidationMode != deps.ValidationModeNone {
		t.Fatalf("ValidationMode = %q, want %q", got.ValidationMode, deps.ValidationModeNone)
	}
	if got.Verify {
		t.Fatal("Verify = true with --no-verify, want false")
	}
}

// TestRwi01DependencyValidationOptionsNoVerifyRejectsChecks covers
// dependencyValidationOptions' checksChanged refusal: --no-verify and an
// explicit --checks cannot be combined, since --no-verify already implies
// no checks run.
func TestRwi01DependencyValidationOptionsNoVerifyRejectsChecks(t *testing.T) {
	command := &cobra.Command{Use: "bump"}
	var checks string
	command.Flags().StringVar(&checks, "checks", "", "checks")
	if err := command.Flags().Set("checks", "lint"); err != nil {
		t.Fatalf("setting --checks: %v", err)
	}
	_, _, err := dependencyValidationOptions(command, depsSetOptions{noVerify: true})
	if err == nil || !strings.Contains(err.Error(), "--no-verify and --checks cannot be used together") {
		t.Fatalf("dependencyValidationOptions(noVerify, --checks changed) = %v, want the no-verify/--checks refusal", err)
	}
}
