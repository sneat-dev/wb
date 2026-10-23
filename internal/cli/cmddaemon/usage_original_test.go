package cmddaemon

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/daemonoperation"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
	"github.com/spf13/cobra"
)

func TestCwWtDaemonOperationUsageErrors(t *testing.T) {
	t.Parallel()
	deps := testDependencies()

	builders := map[string]func() *cobra.Command{
		"submit": func() *cobra.Command { return commandForTest("operation submit", testRuntime(), deps) },
		"get":    func() *cobra.Command { return commandForTest("operation get", testRuntime(), deps) },
		"wait":   func() *cobra.Command { return commandForTest("operation wait", testRuntime(), deps) },
		"cancel": func() *cobra.Command { return commandForTest("operation cancel", testRuntime(), deps) },
	}
	arguments := map[string][]string{
		"submit": {"--", "true"},
		"get":    {"op-1"},
		"wait":   {"op-1"},
		"cancel": {"op-1"},
	}
	for name, build := range builders {
		args := append([]string{"--format", "yaml"}, arguments[name]...)
		command := build()
		command.SilenceUsage, command.SilenceErrors = true, true
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs(args)
		if err := command.Execute(); err == nil {
			t.Errorf("operation %s --format yaml returned nil, want a usage error", name)
		}

		args = append([]string{"--json", "--format", "yaml"}, arguments[name]...)
		command = build()
		command.SilenceUsage, command.SilenceErrors = true, true
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs(args)
		if err := command.Execute(); err == nil {
			t.Errorf("operation %s --json with a conflicting --format returned nil", name)
		}
	}

	// Submit without a command after -- is a usage error.
	submit := commandForTest("operation submit", testRuntime(), deps)
	submit.SilenceUsage, submit.SilenceErrors = true, true
	submit.SetOut(&bytes.Buffer{})
	submit.SetErr(&bytes.Buffer{})
	submit.SetArgs([]string{"--"})
	if err := submit.Execute(); err == nil || !strings.Contains(err.Error(), "command is required after --") {
		t.Fatalf("submit without a command = %v", err)
	}
	submitNoDash := commandForTest("operation submit", testRuntime(), deps)
	submitNoDash.SilenceUsage, submitNoDash.SilenceErrors = true, true
	submitNoDash.SetOut(&bytes.Buffer{})
	submitNoDash.SetErr(&bytes.Buffer{})
	submitNoDash.SetArgs([]string{"true"})
	if err := submitNoDash.Execute(); err == nil || !strings.Contains(err.Error(), "command is required after --") {
		t.Fatalf("submit without -- = %v", err)
	}

	// A denied raw-execution policy refuses before any RPC.
	denied := deps
	denied.Submit = (daemonoperation.Service{RawPolicy: func(string) (bool, string, error) { return false, "/tmp/policy.json", nil }, Client: func(context.Context, string, io.Writer) (daemonv1connect.DaemonServiceClient, error) {
		t.Fatal("denied request reached RPC bootstrap")
		return nil, nil
	}}).Submit
	deniedSubmit := commandForTest("operation submit", testRuntime(), denied)
	deniedSubmit.SilenceUsage, deniedSubmit.SilenceErrors = true, true
	deniedSubmit.SetOut(&bytes.Buffer{})
	deniedSubmit.SetErr(&bytes.Buffer{})
	deniedSubmit.SetArgs(append([]string{"--"}, []string{"true"}...))
	if err := deniedSubmit.Execute(); err == nil || !strings.Contains(err.Error(), "raw daemon execution is disabled") {
		t.Fatalf("denied raw execution = %v", err)
	}
}
