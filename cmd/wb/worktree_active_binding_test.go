package main

import (
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
	"strings"
	"testing"
)

//nolint:paralleltest // The native default inventory reads process-wide HOME and WB_HOME, isolated with t.Setenv.
func TestWorktreeActiveRootBindingUsesNativeInventory(t *testing.T) {
	// Default dependencies against an empty root. Isolate the user home too:
	// the default claim reader includes the retired $HOME/.wb layout.
	projects := t.TempDir()
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome)
	t.Setenv(wbhome.EnvOverride, projects)
	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeActiveCmd(&invocation{projectsRoot: projects}) }, "--local-only")
	if err != nil {
		t.Fatalf("active --local-only: %v", err)
	}
	if !strings.Contains(stdout, "local: available") || !strings.Contains(stdout, "remote: local_only") {
		t.Fatalf("active text stdout = %q", stdout)
	}

	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeActiveCmd(&invocation{projectsRoot: projects}) }, "--format", "json", "--local-only")
	if err != nil {
		t.Fatalf("active json: %v", err)
	}
	if !strings.Contains(stdout, "schema_version") {
		t.Fatalf("active json stdout = %q", stdout)
	}

	// Without a configured remote the preflight is incomplete: exit 1.
	_, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeActiveCmd(&invocation{projectsRoot: projects}) })
	if code := exitCodeOf(t, err); code != exitFindings {
		t.Fatalf("active without a remote exit = %d (%v)", code, err)
	}

	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeActiveCmd(&invocation{projectsRoot: projects}) }, "--format", "bogus"); err == nil {
		t.Fatal("active with a bogus format must fail")
	}
}
