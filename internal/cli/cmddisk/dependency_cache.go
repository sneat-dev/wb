package cmddisk

import (
	"errors"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func newDependencyCacheCmd(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	command := &cobra.Command{
		Use: "dependency-cache", Short: "Plan or apply bounded Linux dependency-archive caching",
		Long: "Plan without writes by default. --apply restores or installs exact packageManager dependencies into mounted Linux masks and enforces archive age, aggregate size and free-space bounds. Only .wb-worklog/linux-dependency-cache is managed; source, receipts and global package stores are excluded.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if deps.DependencyCache == nil {
				return errors.New("dependency-cache operation is unavailable")
			}
			var args []string
			cmd.Flags().Visit(func(flag *pflag.Flag) {
				if flag.Value.Type() == "bool" && flag.Value.String() == "false" {
					return
				}
				args = append(args, "--"+flag.Name)
				if flag.Value.Type() != "bool" {
					args = append(args, flag.Value.String())
				}
			})
			err := deps.DependencyCache(cmd.Context(), args, cmd.OutOrStdout(), cmd.ErrOrStderr())
			var status interface{ ExitCode() int }
			if errors.As(err, &status) && status.ExitCode() == shared.ExitUsage {
				return runtime.ExitError(shared.ExitUsage, "invalid dependency-cache arguments; see helper diagnostics")
			}
			return err
		},
	}
	command.Flags().Bool("apply", false, "Run the cache job and apply its archive policy")
	command.Flags().String("image", "", "Exact immutable Linux image digest (required with --apply)")
	command.Flags().String("source-manifest", "", "Read-only frozen source manifest; avoids requiring .git")
	command.Flags().Int64("max-bytes", 2*1024*1024*1024, "Aggregate archive and manifest byte limit")
	command.Flags().Int64("max-age-seconds", 7*86400, "Maximum unused completed-archive age")
	command.Flags().Int64("min-free-bytes", 512*1024*1024, "Free-space reserve that must remain")
	command.Flags().Int64("growth-bytes", 4*1024*1024*1024, "Additional free headroom required before install or restore")
	command.Flags().String("store-dir", "/cache/pnpm-store", "Separate ephemeral store: /cache/pnpm-store or /tmp/wb-pnpm-store")
	return command
}
