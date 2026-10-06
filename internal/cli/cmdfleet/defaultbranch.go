package cmdfleet

import (
	"fmt"
	"io"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/defaultbranch"
	"github.com/spf13/cobra"
)

func NewDefaultBranch(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	Parallel := min(deps.Budget(), 4)
	var asJSON bool
	options := defaultbranch.Options{Parallel: Parallel}
	command := &cobra.Command{Use: "default-branch", Short: "Audit or safely rename GitHub default branches", Long: `Audit the configured GitHub default branch across the accessible fleet. Audit is read-only.

The desired branch comes from wb.yaml fleet.default_branch, overridden by fleet.organizations.<owner>.default_branch; --branch has highest precedence. Apply requires explicit --org, --repo, or --user scope and writes a durable report before each mutation.

When the desired branch is absent, WB uses GitHub's branch-rename endpoint only after fresh repository and branch reads. If it already exists at the same SHA, WB may change the repository default only after confirming protection/ruleset coverage. A different target SHA, an archived repository, an open source-default PR (including fork-to-parent PRs), Pages source, or a protection/ruleset impact is a refusal. Workflow references are reported only; WB never rewrites branch strings blindly.

GitHub may make an accepted rename visible asynchronously. WB records the accepted response, waits with bounded read-only checks, and never retries the mutation. --reconcile-from accepts only an exact-byte, SHA-256-bound apply receipt and fresh remote proof before reconciling a canonical clone; it is remote read-only and records a blocker while any refreshed repository is not compliant.

--temporarily-unarchive is an explicit exception for an archived repository that passes all existing branch-safety checks. WB checkpoints its numeric repository ID and original archive state, restores archival before local reconciliation, and provides a digest-bound restore-only recovery path.`, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			effective := options

			effective.Owners = requestedOwners(runtime, cmd, effective.Owners)
			if effective.Parallel < 1 || effective.Parallel > 16 {
				return usage(runtime, "--parallel must be between 1 and 16")
			}
			if len(effective.Repositories) > 0 && (len(effective.Owners) > 0 || effective.IncludeUser || effective.AllOrgs) {
				return usage(runtime, "--repo cannot be combined with --org, --all-orgs, or --user")
			}
			if effective.AllOrgs && (len(effective.Owners) > 0 || effective.IncludeUser) {
				return usage(runtime, "--all-orgs cannot be combined with --org or --user")
			}
			if effective.Apply && len(effective.Owners) == 0 && len(effective.Repositories) == 0 && !effective.IncludeUser && !effective.AllOrgs {
				return usage(runtime, "--apply requires explicit --org, --all-orgs, --repo, or --user scope")
			}
			if effective.ReconcileFrom != "" && !effective.Apply {
				return usage(runtime, "--reconcile-from requires --apply")
			}
			if effective.ReconcileFrom != "" && !defaultbranch.ValidDigest(effective.ReconcileSHA256) {
				return usage(runtime, "--reconcile-from requires --reconcile-sha256 with the exact 64-character SHA-256 of that report")
			}
			if effective.ReconcileFrom == "" && effective.ReconcileSHA256 != "" {
				return usage(runtime, "--reconcile-sha256 requires --reconcile-from")
			}
			if effective.RestoreArchiveFrom != "" && !effective.Apply {
				return usage(runtime, "--restore-archive-from requires --apply")
			}
			if effective.RestoreArchiveFrom != "" && !defaultbranch.ValidDigest(effective.RestoreArchiveSHA256) {
				return usage(runtime, "--restore-archive-from requires --restore-archive-sha256 with the exact 64-character SHA-256 of that report")
			}
			if effective.RestoreArchiveFrom == "" && effective.RestoreArchiveSHA256 != "" {
				return usage(runtime, "--restore-archive-sha256 requires --restore-archive-from")
			}
			if effective.RestoreArchiveFrom != "" && (effective.Branch != "" || effective.ReconcileFrom != "" || effective.TemporarilyUnarchive || len(effective.Repositories) != 1 || len(effective.Owners) > 0 || effective.IncludeUser || effective.AllOrgs) {
				return usage(runtime, "--restore-archive-from requires exactly one --repo and cannot be combined with migration or reconciliation flags")
			}
			if effective.MigratePagesSource && effective.TemporarilyUnarchive {
				return usage(runtime, "--migrate-pages-source does not support archived repositories; complete the separately reviewed archive transition first")
			}
			report, err := deps.DefaultBranch(cmd.Context(), defaultbranch.Request{Scope: defaultbranch.Scope{ProjectsRoot: runtime.Flags().ProjectsRoot, Filter: runtime.Flags().Filter}, Options: effective}, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if asJSON {
				err = writeJSON(cmd.OutOrStdout(), report)
			} else {
				err = printDefaultBranchReport(cmd.OutOrStdout(), report)
			}
			if err != nil {
				return err
			}
			if defaultbranch.HasFindings(report) {
				return runtime.ExitError(shared.ExitFindings, "default-branch findings remain; see the report above")
			}
			return nil
		}}
	command.Flags().BoolVar(&options.Apply, "apply", false, "apply only reviewed, safe branch renames")
	command.Flags().StringVar(&options.Branch, "branch", "", "desired default branch; overrides organization and fleet policy")
	command.Flags().StringArrayVarP(&options.Owners, "org", "o", nil, "only inspect or apply this GitHub organization (repeatable)")
	command.Flags().StringArrayVar(&options.Repositories, "repo", nil, "only inspect or apply this exact owner/repository (repeatable)")
	command.Flags().BoolVar(&options.IncludeUser, "user", false, "include the authenticated user's repositories in explicit scope")
	command.Flags().BoolVar(&options.AllOrgs, "all-orgs", false, "include every currently accessible organization, excluding personal repositories")
	command.Flags().IntVar(&options.Parallel, "parallel", Parallel, "maximum GitHub repositories to inspect or apply concurrently (1-16)")
	command.Flags().StringVar(&options.ReportDir, "report-dir", "", "durable report directory (apply defaults below <wb-home>/reports/default-branch)")
	command.Flags().StringVar(&options.ReconcileFrom, "reconcile-from", "", "resume canonical-clone reconciliation from an earlier default-branch apply report")
	command.Flags().StringVar(&options.ReconcileSHA256, "reconcile-sha256", "", "required SHA-256 of the exact --reconcile-from report bytes")
	command.Flags().BoolVar(&options.TemporarilyUnarchive, "temporarily-unarchive", false, "allow a safe archived repository to be temporarily unarchived and restored")
	command.Flags().BoolVar(&options.MigratePagesSource, "migrate-pages-source", false, "migrate or verify a supported legacy GitHub Pages source transition")
	command.Flags().BoolVar(&options.RewriteWorkflowTriggers, "rewrite-workflow-triggers", false, "rewrite only supported plain workflow branch trigger scalars before a default-branch rename")
	command.Flags().StringVar(&options.RestoreArchiveFrom, "restore-archive-from", "", "restore archival only from an earlier default-branch apply report")
	command.Flags().StringVar(&options.RestoreArchiveSHA256, "restore-archive-sha256", "", "required SHA-256 of the exact --restore-archive-from report bytes")
	shared.AddJSONFormatFlags(command, &asJSON)
	return command
}
func printDefaultBranchReport(out io.Writer, report defaultbranch.Report) error {
	if _, err := fmt.Fprintf(out, "Default branch %s (%s)\\n", report.Desired, report.Mode); err != nil {
		return err
	}
	for _, repo := range report.Repositories {
		if _, err := fmt.Fprintf(out, "- %s: %s", repo.Repository, repo.Disposition); err != nil {
			return err
		}
		if repo.Error != "" {
			if _, err := fmt.Fprintf(out, " — %s", repo.Error); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
		for _, clone := range repo.CanonicalClones {
			if _, err := fmt.Fprintf(out, "  - %s: %s", clone.Path, clone.Disposition); err != nil {
				return err
			}
			if clone.Error != "" {
				if _, err := fmt.Fprintf(out, " — %s", clone.Error); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintln(out); err != nil {
				return err
			}
		}
	}
	return nil
}
