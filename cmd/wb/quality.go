package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmdquality"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/qualityrun"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"time"
)

// qualityOptions temporarily preserves the selectors used by remaining root families.
type qualityOptions struct {
	fleet        bool
	match, regex string
	parallel     int
	timeout      time.Duration
	retry        int
	allowEmpty   bool
}
type qualityTarget struct {
	repository string
	path       string
}

func qualityCommandDependencies() cmdquality.Dependencies {
	return cmdquality.Dependencies{Coverage: qualityrun.Coverage, Verification: qualityrun.Verification, Changed: qualityrun.ChangedCoverage, Stored: qualityrun.StoredCoverage, Baseline: qualityrun.Baseline, Summary: qualityrun.Summary, Worklist: qualityrun.Worklist, Analyze: quality.Deadcode, WriteDeadcodeBaseline: quality.WriteDeadcodeBaseline, Abs: filepath.Abs, WorkflowAnnotations: func() bool { return cmdquality.GitHubActionsEnabled(os.Getenv) }}
}
func newCoverageCmd(inv *invocation) *cobra.Command {
	return cmdquality.NewCoverage(newCLIRuntime(inv), qualityCommandDependencies())
}
func newVerifyCmd(inv *invocation) *cobra.Command {
	command := cmdquality.NewVerify(newCLIRuntime(inv), qualityCommandDependencies())
	command.AddCommand(newVerifyReceiptCmd())
	return command
}
func newCheckCmd(inv *invocation) *cobra.Command {
	return cmdquality.NewCheck(newCLIRuntime(inv), qualityCommandDependencies())
}
