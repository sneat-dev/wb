package cmdhooks

import (
	"encoding/json"
	"fmt"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/spf13/cobra"
)

func (family commands) newHooksInstallCmd(repair bool) *cobra.Command {
	var (
		configPath string
		force      bool
		fleet      bool
	)
	verb := "install"
	short := "Install WB-managed Git hook shims"
	if repair {
		verb = "repair"
		short = "Restore missing, stale, or conflicting managed hook shims"
	}
	cmd := &cobra.Command{
		Use:   verb + " [repository-path]",
		Short: short,
		Long: `Install WB-managed hook shims without replacing unauthorized user hooks.

Worktree admission is installed by default at post-checkout, pre-commit, and
pre-push. Git has no pre-checkout hook, so post-checkout reports an unmanaged
checkout after it has happened and preserves it for inspection; pre-commit and
pre-push block unsafe work. To opt out, record profiles.exclude: [worktree] in
the selected hooks policy and run repair; the exception remains visible to
hooks check.

Managed shims retain no installer executable path. At each Git invocation they
prefer an explicit WB_EXECUTABLE; otherwise they resolve wb from PATH and
reject relative, repository-local, non-regular, or non-executable results.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if fleet {
				if len(args) > 0 {
					return fmt.Errorf("repository-path cannot be used with --fleet")
				}
				return family.applyHooksFleet(cmd.OutOrStdout(), cmd.ErrOrStderr(), configPath, repair, force)
			}
			repoPath := shared.ArgumentOrCurrent(args)
			result, err := family.git.Apply(hooks.ApplyOptions{
				RepoPath:     repoPath,
				ConfigPath:   configPath,
				WBExecutable: family.git.Executable(),
				ProjectsRoot: family.runtime.Flags().ProjectsRoot,
				Repair:       repair,
				Force:        force,
			})
			if err != nil {
				return err
			}
			for _, action := range result.Actions {
				if err := shared.WriteLine(cmd.OutOrStdout(), "✓", action); err != nil {
					return err
				}
			}
			if err := shared.WriteFormat(cmd.OutOrStdout(), "✓ hooks ready for %s\n", result.Report.RepoRoot); err != nil {
				return err
			}
			if result.Report.MetricsPath != "" {
				if err := shared.WriteFormat(cmd.OutOrStdout(), "  local metrics: %s\n", result.Report.MetricsPath); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "explicit hooks policy (default: global + repository policies)")
	cmd.Flags().BoolVar(&fleet, "fleet", false, "process every local repository under --projects-root")
	if repair {
		cmd.Flags().BoolVar(&force, "force", false, "back up unmanaged shims and replace a conflicting core.hooksPath")
	}
	return cmd
}

func (family commands) newHooksCheckCmd() *cobra.Command {
	var (
		configPath string
		jsonOut    bool
		fleet      bool
	)
	cmd := &cobra.Command{
		Use:     "check [repository-path]",
		Aliases: []string{"validate"},
		Short:   "Validate hook policy, templates, installation, and drift",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if fleet {
				if len(args) > 0 {
					return fmt.Errorf("repository-path cannot be used with --fleet")
				}
				return family.checkHooksFleet(cmd.OutOrStdout(), configPath, jsonOut)
			}
			report, err := family.git.Check(shared.ArgumentOrCurrent(args), configPath, family.git.Executable(), family.runtime.Flags().ProjectsRoot)
			if err != nil {
				return err
			}
			if jsonOut {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(report); err != nil {
					return err
				}
			} else {
				if err := printHooksCheck(cmd.OutOrStdout(), report); err != nil {
					return err
				}
			}
			if len(report.Findings) > 0 {
				return &CheckError{Count: len(report.Findings)}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "explicit hooks policy (default: global + repository policies)")
	shared.AddJSONFormatFlags(cmd, &jsonOut)
	cmd.Flags().BoolVar(&fleet, "fleet", false, "process every local repository under --projects-root")
	return cmd
}

type CheckError struct {
	Count int
	Fleet bool
}

func (e *CheckError) Error() string {
	if e.Fleet {
		return fmt.Sprintf("fleet hooks check found %d problem(s); run `wb hooks repair --fleet`", e.Count)
	}
	return fmt.Sprintf("hooks check found %d problem(s); run `wb hooks repair`", e.Count)
}
