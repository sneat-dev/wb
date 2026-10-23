package cmdworktree

import (
	"context"
	"github.com/sneat-dev/wb/internal/graduation"
	"github.com/spf13/cobra"
)

// ReceiptDependencies supplies reusable evidence operations to the receipt command.
type ReceiptDependencies struct {
	Compose func(graduation.EvidencePaths) ([]byte, error)
	Observe func(context.Context, graduation.RemoteTargetRequest) ([]byte, error)
}

// NewReceipt constructs verification evidence commands without importing the executable.
func NewReceipt(deps ReceiptDependencies) *cobra.Command {
	var paths graduation.EvidencePaths
	command := &cobra.Command{Use: "receipt", Short: "Compose exact verification, remote, deployment, and cleanup evidence", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(command *cobra.Command, _ []string) error {
		raw, err := deps.Compose(paths)
		if err != nil {
			return err
		}
		_, err = command.OutOrStdout().Write(raw)
		return err
	}}
	command.Flags().StringVar(&paths.LocalCheck, "local-check", "", "JSON from wb check --profile ci --format json for one clean exact revision")
	command.Flags().StringVar(&paths.CIWait, "ci-wait", "", "JSON from wb ci wait --json for the exact target revision")
	command.Flags().StringVar(&paths.RemoteTarget, "remote-target", "", "JSON from wb verify receipt remote-target")
	command.Flags().StringVar(&paths.DeployedRevision, "deployed-revision", "", "closed external deployment-provider JSON receipt")
	command.Flags().StringVar(&paths.TerminalCleanup, "terminal-cleanup", "", "cleanup.json from wb worktree cleanup --apply --remote")
	command.Flags().StringVar(&paths.Output, "output", "", "create this new JSON receipt file as well as writing stdout")
	command.AddCommand(newReceiptRemoteTarget(deps))
	return command
}
func newReceiptRemoteTarget(deps ReceiptDependencies) *cobra.Command {
	var request graduation.RemoteTargetRequest
	command := &cobra.Command{Use: "remote-target", Short: "Observe one exact remote target ref with git ls-remote", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(command *cobra.Command, _ []string) error {
		raw, err := deps.Observe(command.Context(), request)
		if err != nil {
			return err
		}
		_, err = command.OutOrStdout().Write(raw)
		return err
	}}
	command.Flags().StringVar(&request.Repository, "repo", "", "expected owner/repository identity")
	command.Flags().StringVar(&request.RepositoryPath, "repository-path", ".", "local checkout used to resolve the named remote")
	command.Flags().StringVar(&request.Remote, "remote", "origin", "configured Git remote to observe")
	command.Flags().StringVar(&request.Target, "target", "", "target branch to observe")
	command.Flags().StringVar(&request.Output, "output", "", "create this new JSON evidence file as well as writing stdout")
	return command
}
