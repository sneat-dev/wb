package main

import (
	"github.com/sneat-dev/wb/internal/defaultbranch"
	"github.com/sneat-dev/wb/internal/runqueue"
	"github.com/spf13/cobra"
)

func newFleetDefaultBranchCmd(inv *invocation) *cobra.Command {
	parallel := min(runqueue.Budget(), 4)
	options := defaultbranch.Options{Parallel: parallel}
	command := &cobra.Command{Use: "default-branch", Short: "Audit or safely rename GitHub default branches", Long: `Audit the configured GitHub default branch across the accessible fleet. Audit is read-only.

The desired branch comes from wb.yaml fleet.default_branch, overridden by fleet.organizations.<owner>.default_branch; --branch has highest precedence. Apply requires explicit --org, --repo, or --user scope and writes a durable report before each mutation.

When the desired branch is absent, WB uses GitHub's branch-rename endpoint only after fresh repository and branch reads. If it already exists at the same SHA, WB may change the repository default only after confirming protection/ruleset coverage. A different target SHA, an archived repository, an open source-default PR (including fork-to-parent PRs), Pages source, or a protection/ruleset impact is a refusal. Workflow references are reported only; WB never rewrites branch strings blindly.

GitHub may make an accepted rename visible asynchronously. WB records the accepted response, waits with bounded read-only checks, and never retries the mutation. --reconcile-from accepts only an exact-byte, SHA-256-bound apply receipt and fresh remote proof before reconciling a canonical clone; it is remote read-only and records a blocker while any refreshed repository is not compliant.

--temporarily-unarchive is an explicit exception for an archived repository that passes all existing branch-safety checks. WB checkpoints its numeric repository ID and original archive state, restores archival before local reconciliation, and provides a digest-bound restore-only recovery path.`, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options.Owners = requestedDefaultBranchOwners(inv, cmd, options.Owners)
			if options.Parallel < 1 || options.Parallel > 16 {
				return usageError("--parallel must be between 1 and 16")
			}
			if len(options.Repositories) > 0 && (len(options.Owners) > 0 || options.IncludeUser || options.AllOrgs) {
				return usageError("--repo cannot be combined with --org, --all-orgs, or --user")
			}
			if options.AllOrgs && (len(options.Owners) > 0 || options.IncludeUser) {
				return usageError("--all-orgs cannot be combined with --org or --user")
			}
			if options.Apply && len(options.Owners) == 0 && len(options.Repositories) == 0 && !options.IncludeUser && !options.AllOrgs {
				return usageError("--apply requires explicit --org, --all-orgs, --repo, or --user scope")
			}
			if options.ReconcileFrom != "" && !options.Apply {
				return usageError("--reconcile-from requires --apply")
			}
			if options.ReconcileFrom != "" && !defaultbranch.ValidDigest(options.ReconcileSHA256) {
				return usageError("--reconcile-from requires --reconcile-sha256 with the exact 64-character SHA-256 of that report")
			}
			if options.ReconcileFrom == "" && options.ReconcileSHA256 != "" {
				return usageError("--reconcile-sha256 requires --reconcile-from")
			}
			if options.RestoreArchiveFrom != "" && !options.Apply {
				return usageError("--restore-archive-from requires --apply")
			}
			if options.RestoreArchiveFrom != "" && !defaultbranch.ValidDigest(options.RestoreArchiveSHA256) {
				return usageError("--restore-archive-from requires --restore-archive-sha256 with the exact 64-character SHA-256 of that report")
			}
			if options.RestoreArchiveFrom == "" && options.RestoreArchiveSHA256 != "" {
				return usageError("--restore-archive-sha256 requires --restore-archive-from")
			}
			if options.RestoreArchiveFrom != "" && (options.Branch != "" || options.ReconcileFrom != "" || options.TemporarilyUnarchive || len(options.Repositories) != 1 || len(options.Owners) > 0 || options.IncludeUser || options.AllOrgs) {
				return usageError("--restore-archive-from requires exactly one --repo and cannot be combined with migration or reconciliation flags")
			}
			if options.MigratePagesSource && options.TemporarilyUnarchive {
				return usageError("--migrate-pages-source does not support archived repositories; complete the separately reviewed archive transition first")
			}
			report, err := defaultbranch.Run(cmd.Context(), inv.projectsRoot, inv.filterFlag, options, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if options.JSON {
				err = writeJSONTo(cmd.OutOrStdout(), report)
			} else {
				err = defaultbranch.Print(cmd.OutOrStdout(), report)
			}
			if err != nil {
				return err
			}
			if defaultbranch.HasFindings(report) {
				return &exitError{code: exitFindings, message: "default-branch findings remain; see the report above"}
			}
			return nil
		}}
	command.Flags().BoolVar(&options.Apply, "apply", false, "apply only reviewed, safe branch renames")
	command.Flags().StringVar(&options.Branch, "branch", "", "desired default branch; overrides organization and fleet policy")
	command.Flags().StringArrayVarP(&options.Owners, "org", "o", nil, "only inspect or apply this GitHub organization (repeatable)")
	command.Flags().StringArrayVar(&options.Repositories, "repo", nil, "only inspect or apply this exact owner/repository (repeatable)")
	command.Flags().BoolVar(&options.IncludeUser, "user", false, "include the authenticated user's repositories in explicit scope")
	command.Flags().BoolVar(&options.AllOrgs, "all-orgs", false, "include every currently accessible organization, excluding personal repositories")
	command.Flags().IntVar(&options.Parallel, "parallel", parallel, "maximum GitHub repositories to inspect or apply concurrently (1-16)")
	command.Flags().StringVar(&options.ReportDir, "report-dir", "", "durable report directory (apply defaults below <wb-home>/reports/default-branch)")
	command.Flags().StringVar(&options.ReconcileFrom, "reconcile-from", "", "resume canonical-clone reconciliation from an earlier default-branch apply report")
	command.Flags().StringVar(&options.ReconcileSHA256, "reconcile-sha256", "", "required SHA-256 of the exact --reconcile-from report bytes")
	command.Flags().BoolVar(&options.TemporarilyUnarchive, "temporarily-unarchive", false, "allow a safe archived repository to be temporarily unarchived and restored")
	command.Flags().BoolVar(&options.MigratePagesSource, "migrate-pages-source", false, "migrate or verify a supported legacy GitHub Pages source transition")
	command.Flags().BoolVar(&options.RewriteWorkflowTriggers, "rewrite-workflow-triggers", false, "rewrite only supported plain workflow branch trigger scalars before a default-branch rename")
	command.Flags().StringVar(&options.RestoreArchiveFrom, "restore-archive-from", "", "restore archival only from an earlier default-branch apply report")
	command.Flags().StringVar(&options.RestoreArchiveSHA256, "restore-archive-sha256", "", "required SHA-256 of the exact --restore-archive-from report bytes")
	addJSONFormatFlags(command, &options.JSON)
	return command
}

func requestedDefaultBranchOwners(inv *invocation, command *cobra.Command, owners []string) []string {
	selected := append([]string(nil), owners...)
	if rootOrg := command.Root().PersistentFlags().Lookup("org"); rootOrg != nil && rootOrg.Changed {
		selected = append(selected, inv.extraOrgs...)
	}
	return selected
}
