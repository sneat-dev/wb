package main

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarkerAndOwnRootBindingsPreserveCurrentFlagsAndTypedCustody(t *testing.T) {
	t.Parallel()
	inv := &invocation{projectsRoot: filepath.Join(t.TempDir(), "absent")}
	marker := newWorktreeMarkerCmd(inv)
	// The actual native discovery observes the current invocation after construction.
	inv.projectsRoot = t.TempDir()
	var output bytes.Buffer
	marker.SetOut(&output)
	marker.SetErr(io.Discard)
	marker.SetArgs([]string{"--fleet", "--format", "json"})
	if err := marker.Execute(); err != nil || output.String() != "[]\n" {
		t.Fatal(err, output.String())
	}
	own := newWorktreeOwnCmd()
	for _, name := range []string{"mode", "initiator", "pid", "runtime", "model", "agent-id"} {
		if own.Flags().Lookup(name) == nil {
			t.Fatalf("missing actual own flag %q", name)
		}
	}
	own.SetOut(io.Discard)
	own.SetErr(io.Discard)
	own.SetArgs([]string{"--mode", "invalid"})
	if err := own.Execute(); err == nil || !strings.Contains(err.Error(), "execution mode") {
		t.Fatal(err)
	}
}
