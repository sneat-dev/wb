package cmdsync

import (
	"context"
	"io"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/syncrun"
	"github.com/spf13/cobra"
)

func New(runtime shared.Runtime, run func(context.Context, syncrun.Options, io.Writer, io.Writer) int) *cobra.Command {
	var (
		dryRun        bool
		workers       int
		only          []string
		publish       bool
		pruneArchived bool
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Clone/pull/prune local clones to match GitHub (parallel, with a full-screen progress UI)",
		Long: `Clone missing repositories, fast-forward existing ones, and — only with
--prune-archived — delete a local clone whose repository is confirmed archived
on GitHub, exactly when it passes the same safety predicate 'wb archive clean'
uses (live-confirmed archived status, no uncommitted/untracked changes, no
stash, no unpushed commits on any branch, no local-only branch, no unpushed
tag, no linked worktree, no non-terminal WB Work Log claim, not marked
wb.skip-sync).

Without --prune-archived, an archived repository is never deleted: sync pulls
its local clone exactly like any other repository's, and the report still
names it as archived so it is never silently indistinguishable from an
ordinary clone.

After successful mutations, trusted checkout-updated hooks from the standard
user wb.yaml are durably enqueued only for clones and repositories whose
checked-out HEAD actually changed; already-current pulls and dry runs never
fire them. Sync does not wait for the external executors.

When stdout is a terminal, progress uses the full terminal and the final text
report is written to stderr after the terminal is restored. Piped, CI, and
--non-interactive runs keep their report on stdout.`,
		Example: `# Preview fleet reconciliation without changing repositories
wb sync --dry-run

# Sync selected owners with bounded concurrency
wb sync --org owner-a --org owner-b --parallel 4`,
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := runtime.Flags()
			owners := requestedSyncOwners(flags.ExtraOrgs, cmd, only)
			if code := run(cmd.Context(), syncrun.Options{ProjectsRoot: flags.ProjectsRoot, Filter: flags.Filter, Owners: owners, Workers: workers, DryRun: dryRun, Publish: publish, PruneArchived: pruneArchived, Interactive: console.Interactive(cmd.OutOrStdout(), flags.NonInteractive)}, cmd.OutOrStdout(), cmd.ErrOrStderr()); code != 0 {
				return runtime.ExitError(code, "sync did not complete; see diagnostics above")
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "print the plan; change nothing")
	cmd.Flags().BoolVar(&pruneArchived, "prune-archived", false, "delete a local clone whose repository is confirmed archived on GitHub, but only when it passes the same safety predicate as 'wb archive clean' (default: pull archived repos like any other, never delete)")
	// --parallel is the fleet-wide name for this ceiling: six other commands
	// already spell it that way, and "workers" reads as a second noun beside
	// WB's own tasks. --workers/-j stays as a hidden deprecated alias so
	// existing scripts and muscle memory keep working.
	// GitHub can close SSH handshakes when a large fleet starts too many at
	// once. Four still keeps sync comfortably parallel while avoiding that
	// transport limit on the default path; callers that know their network can
	// opt into a higher ceiling explicitly.
	cmd.Flags().IntVar(&workers, "parallel", 4, "maximum repositories to inspect concurrently")
	cmd.Flags().IntVarP(&workers, "workers", "j", 4, "maximum repositories to inspect concurrently")
	_ = cmd.Flags().MarkDeprecated("workers", "use --parallel instead")
	cmd.Flags().StringArrayVarP(&only, "org", "o", nil, "only sync this org (repeatable); default: all your orgs + your own account")
	cmd.Flags().BoolVar(&publish, "publish", false, "after a successful sync, run wb remote publish")
	return cmd
}
func requestedSyncOwners(extraOrgs []string, cmd *cobra.Command, only []string) []string {
	owners := append([]string(nil), only...)
	// `wb --org acme sync` sets the root repeatable flag, while
	// `wb sync --org acme` sets sync's command-local restriction. Both
	// spellings are advertised by Cobra and therefore have identical selection
	// semantics.
	if rootOrg := cmd.Root().PersistentFlags().Lookup("org"); rootOrg != nil && rootOrg.Changed {
		owners = append(owners, extraOrgs...)
	}
	return owners
}
