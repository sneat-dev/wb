package main

import (
	"errors"
	"github.com/spf13/cobra"
	"io"
	"testing"
)

func TestQualityFactoriesBindProductionOperationsAndChildren(t *testing.T) {
	t.Parallel()
	inv := &invocation{}
	cases := []struct {
		name string
		cmd  *cobra.Command
	}{{"coverage", newCoverageCmd(inv)}, {"verify", newVerifyCmd(inv)}, {"check", newCheckCmd(inv)}, {"deadcode", newDeadcodeCmd()}, {"coverage", newFleetCoverageCmd(inv)}}
	for _, tc := range cases {
		if tc.cmd.Name() != tc.name || tc.cmd.RunE == nil {
			t.Fatalf("unbound factory %q: %+v", tc.name, tc.cmd)
		}
	}
	verify := cases[1].cmd
	receipt, _, err := verify.Find([]string{"receipt"})
	if err != nil || receipt == verify || receipt.Name() != "receipt" {
		t.Fatalf("receipt composition: %v %v", receipt, err)
	}
	deps := qualityCommandDependencies()
	if deps.Coverage == nil || deps.Verification == nil || deps.Changed == nil || deps.Stored == nil || deps.Baseline == nil || deps.Summary == nil || deps.Worklist == nil || deps.Analyze == nil || deps.WriteDeadcodeBaseline == nil || deps.Abs == nil {
		t.Fatal("missing concrete operation")
	}
	_ = deps.WorkflowAnnotations()
}
func TestQualityUsagePreservesRootExitErrorIdentity(t *testing.T) {
	t.Parallel()
	cmd := newCoverageCmd(&invocation{})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"--changed", "--target", "main", "--format", "yaml"})
	err := cmd.Execute()
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage {
		t.Fatalf("usage identity=%T %v", err, err)
	}
}
