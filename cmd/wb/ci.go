package main

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/ciaudit"
	"github.com/sneat-dev/wb/internal/cli/cmdci"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/spf13/cobra"
	"os/exec"
	"strings"
	"time"
)

var exactGitObjectID = shared.ExactGitObjectID

func ciDependencies() cmdci.Dependencies {
	return cmdci.Dependencies{WaitChecks: orchestrate.WaitForCommitChecks, Audit: ciaudit.AuditBatch, ValidateBranch: validateCIBranch, Now: time.Now}
}
func validateCIBranch(target string) error {
	if output, err := exec.Command("git", "check-ref-format", "--branch", target).CombinedOutput(); err != nil {
		return fmt.Errorf("--target must be a valid Git branch: %s", strings.TrimSpace(string(output)))
	}
	return nil
}
func newCICmd(inv *invocation) *cobra.Command { return cmdci.New(newCLIRuntime(inv), ciDependencies()) }
func shellQuoteCIWaitArg(value string) string { return shared.ShellQuoteArg(value) }
