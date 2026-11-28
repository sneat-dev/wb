package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestUmbrellaRootCommandWiringAndFlags(t *testing.T) {
	t.Parallel()
	root := newRootCmdFor(&invocation{})
	umbrellaCmd, _, err := root.Find([]string{"umbrella"})
	if err != nil || umbrellaCmd == nil || umbrellaCmd.Name() != "umbrella" {
		t.Fatalf("root missing umbrella command: cmd=%v err=%v", umbrellaCmd, err)
	}
	for _, sub := range []string{"show", "close"} {
		child, _, findErr := root.Find([]string{"umbrella", sub})
		if findErr != nil || child == nil || child.Name() != sub {
			t.Fatalf("umbrella missing %s subcommand: %v", sub, findErr)
		}
		if !persistentFlagSupport["projects-root"]["umbrella "+sub] {
			t.Fatalf("umbrella %s must support --projects-root", sub)
		}
	}
	if !persistentFlagSupport["projects-root"]["worktree set"] {
		t.Fatal("worktree set must support --projects-root")
	}

	// Unknown registry (synchestra) refuses through the root command with exit 2.
	var stdout, stderr bytes.Buffer
	code := run([]string{"--projects-root", t.TempDir(), "umbrella", "show", "synchestra:effort-1"}, &stdout, &stderr)
	if code != exitUsage || !strings.Contains(stderr.String(), "synchestra") {
		t.Fatalf("wb umbrella show synchestra:effort-1 -> exit %d stderr=%q, want exit %d naming synchestra", code, stderr.String(), exitUsage)
	}
}
