package main

import (
	"errors"
	"strings"
	"testing"
)

// The explicit --replace-other-root request reaches the platform check on both
// commands that can start the daemon, and an unrequested start passes false.
func TestDaemonStartAndRestartPassTheReplaceOtherRootFlagToTheCheck(t *testing.T) {
	for _, command := range []string{"start", "restart"} {
		for _, flagged := range []bool{false, true} {
			root := daemonTestRoot(t)
			deps := daemonTestDependencies(t, root)
			var asked []bool
			deps.checkOtherRoot = func(checkedRoot string, replace bool) error {
				if checkedRoot != root {
					t.Errorf("%s checked root %q, want %q", command, checkedRoot, root)
				}
				asked = append(asked, replace)
				return nil
			}
			cmd := newDaemonCmdWithDependencies(&invocation{projectsRoot: root}, deps)
			args := []string{command}
			if flagged {
				args = append(args, "--replace-other-root")
			}
			cmd.SetArgs(args)
			cmd.SetOut(&strings.Builder{})
			cmd.SetErr(&strings.Builder{})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("%v: %v", args, err)
			}
			if len(asked) != 1 || asked[0] != flagged {
				t.Fatalf("%v: check saw %v, want one call with %t", args, asked, flagged)
			}
		}
	}
}

// A refusal from the check ends the start before lifecycle state is written
// or the process is started, for every implicit caller of Start alike.
func TestDaemonLaunchStopsBeforeStateAndProcessWhenTheCheckRefuses(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	starts := 0
	deps.start = func(string, []string, string) (int, error) { starts++; return 1, nil }
	deps.checkOtherRoot = func(string, bool) error { return errors.New("registered for another root") }
	controller := newDaemonController(deps, root)

	if _, err := controller.Start(t.Context(), daemonDefaultListen); err == nil || !strings.Contains(err.Error(), "another root") {
		t.Fatalf("Start = %v", err)
	}
	if _, found, err := controller.store.Load(); err != nil || found || starts != 0 {
		t.Fatalf("a refused launch wrote state (found=%t, err=%v) or started a process (%d)", found, err, starts)
	}
}
