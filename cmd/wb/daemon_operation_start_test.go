package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestDaemonOperationSubcommandsReportWhenTheLocalDaemonCannotBeStarted: each
// `wb daemon operation` verb that needs the local daemon starts it first, and
// when the controller cannot start it the verb fails with "start local daemon"
// and the cause, without a daemon, a listener or a request.
func TestDaemonOperationSubcommandsReportWhenTheLocalDaemonCannotBeStarted(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.Start = func(string, []string, string) (int, error) { return 0, errors.New("supervisor refused") }
	inv := &invocation{projectsRoot: root}
	for name, build := range map[string]func() (*cobra.Command, []string){
		"get": func() (*cobra.Command, []string) {
			return daemonCommandForTest("operation get", inv, deps), []string{"op-1"}
		},
		"cancel": func() (*cobra.Command, []string) {
			return daemonCommandForTest("operation cancel", inv, deps), []string{"op-1"}
		},
		"wait": func() (*cobra.Command, []string) {
			return daemonCommandForTest("operation wait", inv, deps), []string{"op-1"}
		},
	} {
		command, args := build()
		command.SilenceUsage, command.SilenceErrors = true, true
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs(args)
		err := command.Execute()
		if err == nil || !strings.Contains(err.Error(), "start local daemon") || !strings.Contains(err.Error(), "supervisor refused") {
			t.Errorf("operation %s: err = %v, want a start local daemon failure naming the cause", name, err)
		}
	}
}
