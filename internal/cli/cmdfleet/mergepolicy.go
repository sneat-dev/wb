package cmdfleet

import (
	"fmt"
	"io"
	"strings"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/mergepolicy"
	"github.com/spf13/cobra"
)

func NewMergePolicy(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	defaultParallel := min(deps.Budget(), 16)
	var asJSON bool
	options := mergepolicy.Options{Parallel: defaultParallel}
	command := &cobra.Command{
		Use:   "merge-policy",
		Short: "Audit or apply merge-commit policy across GitHub repositories",
		Long: `Audit GitHub repository merge settings and effective default-branch rules.

The desired policy enables merge commits only and asks GitHub to use the pull
request title and body for the merge commit. Audit is the default and is
read-only. --apply is explicit: WB first inventories the exact selected scope,
writes the durable plan, then changes only merge settings.

Audit without selectors may inventory the authenticated user and member
organizations. Apply fails closed unless --org/-o, --repo, or --user selects
the mutation scope explicitly; --org restricts rather than enlarges that scope.

Repository-owned required-linear-history policy is desired drift: apply removes
the dedicated classic setting and the corresponding repository ruleset rule
while preserving every other protection; WB never weakens unrelated protection.
Existing merge-queue rules remain preserved blockers.
Existing organization and enterprise rulesets are higher-level authorities and remain
audit-only in this slice; repository fallback is refused when either conflicts.

Exit codes: 0 compliant/applied, 1 drift, conflicts, or inspection errors,
2 invalid usage.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			effective := options

			effective.Owners = requestedOwners(runtime, command, effective.Owners)
			if len(effective.Repositories) > 0 && (len(effective.Owners) > 0 || effective.IncludeUser) {
				return usage(runtime, "--repo cannot be combined with --org or --user")
			}
			if effective.Parallel < 1 || effective.Parallel > 16 {
				return usage(runtime, "--parallel must be between 1 and 16")
			}
			if effective.Resume && (!effective.Apply || strings.TrimSpace(effective.ReportDir) == "") {
				return usage(runtime, "--resume requires --apply and --report-dir")
			}
			if effective.Apply && len(effective.Owners) == 0 && len(effective.Repositories) == 0 && !effective.IncludeUser {
				return usage(runtime, "--apply requires explicit --org, --repo, or --user scope")
			}
			report, err := deps.MergePolicy(command.Context(), mergepolicy.Request{Scope: mergepolicy.Scope{ProjectsRoot: runtime.Flags().ProjectsRoot, Filter: runtime.Flags().Filter}, Options: effective}, command.ErrOrStderr())
			if err != nil {
				return err
			}
			if asJSON {
				if err := writeJSON(command.OutOrStdout(), report); err != nil {
					return err
				}
			} else if err := printMergePolicyReport(command.OutOrStdout(), report); err != nil {
				return err
			}
			if report.Summary.Drift+report.Summary.Blocked+report.Summary.Errors > 0 {
				return runtime.ExitError(shared.ExitFindings, "merge-policy findings remain; see the report above")
			}
			return nil
		},
	}
	command.Flags().BoolVar(&options.Apply, "apply", false, "apply the exact planned merge policy; audit is the default")
	command.Flags().StringArrayVarP(&options.Owners, "org", "o", nil, "only inspect or apply this GitHub organization (repeatable)")
	command.Flags().StringArrayVar(&options.Repositories, "repo", nil, "only inspect or apply this exact owner/repository (repeatable)")
	command.Flags().BoolVar(&options.IncludeUser, "user", false, "include the authenticated user's own repositories in explicit scope")
	command.Flags().IntVar(&options.Parallel, "parallel", defaultParallel, "maximum GitHub repositories to inspect or apply concurrently (1-16; default: WB CPU budget)")
	command.Flags().StringVar(&options.ReportDir, "report-dir", "", "durable report directory (apply defaults below <wb-home>/reports/merge-policy)")
	command.Flags().BoolVar(&options.Resume, "resume", false, "resume into an existing --report-dir after interruption; all decisions are re-observed")
	shared.AddJSONFormatFlags(command, &asJSON)
	return command
}
func printMergePolicyReport(out io.Writer, report mergepolicy.Report) error {
	if _, err := fmt.Fprintf(out, "Merge policy: %s\n", report.Mode); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "  %d repositories · %d compliant · %d drift · %d blocked · %d errors · %d applied\n", report.Summary.Inspected, report.Summary.Compliant, report.Summary.Drift, report.Summary.Blocked, report.Summary.Errors, report.Summary.Applied); err != nil {
		return err
	}
	for _, repo := range report.Repositories {
		detail := strings.Join(append(append([]string{}, repo.Drift...), repo.Conflicts...), "; ")
		if repo.Error != "" {
			detail = repo.Error
		}
		if detail == "" {
			detail = "merge commits only; PR title + PR body"
		}
		if _, err := fmt.Fprintf(out, "  %-9s %-42s %s\n", repo.Disposition, repo.Repository, detail); err != nil {
			return err
		}
	}
	for _, rule := range report.Rulesets {
		if _, err := fmt.Fprintf(out, "  %-9s %s ruleset %d (%d selected) %s\n", rule.Disposition, strings.ToLower(rule.SourceType), rule.ID, len(rule.Repositories), rule.Error); err != nil {
			return err
		}
	}
	if report.ReportPath != "" {
		if _, err := fmt.Fprintf(out, "Report: %s\n", report.ReportPath); err != nil {
			return err
		}
	}
	return nil
}
