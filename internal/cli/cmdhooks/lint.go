package cmdhooks

import (
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/spf13/cobra"
)

// newHooksLintCmd is the static-check step the built-in Go and Node profile
// templates exec at pre-commit and pre-push. Like push-tier it is a hidden,
// git-hook-adjacent helper whose exit code is the hook's verdict.
func (family commands) newHooksLintCmd() *cobra.Command {
	var hook, language string
	cmd := &cobra.Command{
		Use:    "lint",
		Short:  "Run the built-in profile's lint for one Git hook",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			family.git.Exit(family.git.Lint(hooks.LintOptions{
				Hook: hook, Language: language,
				ProjectsRoot: family.runtime.Flags().ProjectsRoot,
				WBExecutable: family.git.Executable(),
				Stdin:        cmd.InOrStdin(), Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr(),
			}))
			return nil
		},
	}
	cmd.Flags().StringVar(&hook, "hook", "", "the Git hook being run: pre-commit or pre-push")
	cmd.Flags().StringVar(&language, "language", "", "the profile to lint: go or node")
	return cmd
}
