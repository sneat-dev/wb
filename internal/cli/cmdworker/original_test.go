package cmdworker

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestCwCovWorkerCmdSurface(t *testing.T) {
	command := New(testRuntime(), testDependencies())
	sub, _, err := command.Find([]string{"connect"})
	if err != nil || sub == command {
		t.Fatalf("worker connect subcommand is missing: %v", err)
	}
	connect := newConnect(testRuntime(), testDependencies())
	for _, name := range []string{"id", "root", "cpu-capacity", "format", "json"} {
		if connect.Flags().Lookup(name) == nil {
			t.Errorf("worker connect is missing --%s", name)
		}
	}
}
func TestWorkerConnectHelpExposesStableIdentityRootsAndFormats(t *testing.T) {
	command := newConnect(testRuntime(), testDependencies())
	for _, name := range []string{"id", "root", "cpu-capacity", "format", "json"} {
		if command.Flags().Lookup(name) == nil {
			t.Fatalf("worker connect help is missing --%s", name)
		}
	}
}
func TestCwCovWorkerConnectCommandValidation(t *testing.T) {
	root := "/canonical"
	build := func() *cobra.Command { return newConnect(testRuntime(), testDependencies()) }
	cases := map[string][]string{
		"missing id":      {"--root", root},
		"relative root":   {"--id", "cw-worker", "--root", "relative"},
		"missing root":    {"--id", "cw-worker", "--root", "/absent"},
		"no roots at all": {"--id", "cw-worker"},
		"unknown format":  {"--id", "cw-worker", "--root", root, "--format", "toml"},
	}
	for name, args := range cases {
		command := build()
		command.SilenceErrors = true
		command.SilenceUsage = true
		command.SetArgs(args)
		err := command.Execute()
		if err == nil {
			t.Errorf("%s: worker connect accepted an invalid invocation", name)
			continue
		}
		if code := testExitCode(t, err); code != 2 {
			t.Errorf("%s: exit = %d, want usage", name, code)
		}
	}
}
