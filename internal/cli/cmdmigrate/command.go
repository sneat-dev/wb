// Package cmdmigrate owns migration argument policy and command-local presentation.
package cmdmigrate

import (
	"context"
	"fmt"
	cliprogress "github.com/sneat-dev/wb/internal/cli/progress"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/migrate"
	"github.com/sneat-dev/wb/internal/migraterun"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/spf13/cobra"
	"io"
	"strings"
)

type Dependencies struct {
	Local       func(context.Context, migraterun.LocalRequest) (migraterun.LocalResult, error)
	Campaign    func(context.Context, migraterun.CampaignRequest) (migraterun.CampaignResult, error)
	Interactive func(io.Writer, bool) bool
}

const localFailure = "the migration reported changes or failures; rerun with --apply to write them, or inspect the report above"
const campaignFailure = "the migration campaign reported failures; see the per-repository rows above and the report directory"

func New(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var (
		apply     bool
		check     bool
		format    string
		reportDir string

		hierarchical bool
		githubDir    string
		ref          string
		moduleRefs   []string
		verify       string
		noVerify     bool
		commit       bool
		push         bool
		pr           bool
		merge        bool
		parallel     int
		resume       bool
		cleanup      bool
	)
	cmd := &cobra.Command{
		Use:   "migrate <spec.hcl> <root> [root...]",
		Short: "Plan or apply a declarative source migration (dry-run by default)",
		Long: "Migrate evaluates a versioned, language-neutral specification against one or more source roots. " +
			"It never edits files unless --apply is set.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fail := func(cause error, message string) error {
				if _, err := fmt.Fprintln(cmd.ErrOrStderr(), cause); err != nil {
					return err
				}
				return runtime.ExitError(shared.ExitUsage, message)
			}
			if hierarchical {
				var refusal error
				switch {
				case cleanup && (apply || check || commit || push || pr || merge || resume || noVerify || cmd.Flags().Changed("verify")):
					refusal = fmt.Errorf("--cleanup cannot be combined with apply, verification, commit, push, PR, merge, resume, or check options")
				case check:
					refusal = fmt.Errorf("--check is not supported with --hierarchical; use the campaign report from the dry run")
				case noVerify && cmd.Flags().Changed("verify"):
					refusal = fmt.Errorf("--no-verify and --verify cannot be used together")
				}
				if refusal != nil {
					return fail(refusal, campaignFailure)
				}
				refs, err := parseModuleRefs(moduleRefs)
				if err != nil {
					return fail(err, campaignFailure)
				}
				flags := runtime.Flags()
				resolvedGitHubDir := githubDir
				if resolvedGitHubDir == "" {
					resolvedGitHubDir = flags.ProjectsRoot
				}
				verification := migrate.Verification(verify)
				if noVerify {
					verification = migrate.VerifyNone
				}
				var view *cliprogress.Campaign
				result, err := deps.Campaign(cmd.Context(), migraterun.CampaignRequest{
					SpecPath: args[0], Roots: args[1:], GitHubDir: resolvedGitHubDir, ReportDir: reportDir, Ref: ref,
					ModuleRefs: refs, Apply: apply, Verify: verification, Commit: commit, Push: push, PR: pr, Merge: merge,
					Resume: resume, Cleanup: cleanup, Parallel: parallel,
					PrepareProgress: func(id string) progress.Reporter {
						out := cmd.ErrOrStderr()
						view = cliprogress.NewCampaign(out, deps.Interactive(out, flags.NonInteractive), "migrate "+id)
						return view.Reporter()
					},
					BeforePersist: func(completion migraterun.CampaignCompletion) {
						status := "completed"
						if completion.Failed {
							status = "failed"
						}
						view.Finish(status)
					},
				})
				if err != nil {
					return fail(err, campaignFailure)
				}
				if result.Cleanup {
					for _, path := range result.Removed {
						if _, err := fmt.Fprintln(cmd.OutOrStdout(), path); err != nil {
							return err
						}
					}
					return nil
				}
				if err := validateFormat(format); err != nil {
					return fail(err, campaignFailure)
				}
				if err := writeCampaignReport(cmd.OutOrStdout(), result.Report, format); err != nil {
					return err
				}
				if result.RunError != nil {
					return fail(result.RunError, campaignFailure)
				}
				return nil
			}
			if len(args) < 2 {
				return fmt.Errorf("migrate requires at least one source root")
			}
			if commit || push || pr || merge || resume || cleanup || noVerify || cmd.Flags().Changed("verify") || cmd.Flags().Changed("github-dir") || cmd.Flags().Changed("ref") || cmd.Flags().Changed("parallel") || len(moduleRefs) > 0 {
				return fmt.Errorf("--commit, --push, --pr, --merge, --resume, --cleanup, --verify, --no-verify, --github-dir, --ref, --module-ref, and --parallel require --hierarchical")
			}
			if apply && check {
				return fail(fmt.Errorf("--apply and --check cannot be used together"), localFailure)
			}
			result, err := deps.Local(cmd.Context(), migraterun.LocalRequest{SpecPath: args[0], Roots: args[1:], Apply: apply, ReportDir: reportDir})
			if err != nil {
				return fail(err, localFailure)
			}
			if err := validateFormat(format); err != nil {
				return fail(err, localFailure)
			}
			if err := writeMigrationReport(cmd.OutOrStdout(), result.Report, format); err != nil {
				return err
			}
			if check && result.HasChanges {
				return runtime.ExitError(shared.ExitFindings, localFailure)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "write the planned changes")
	cmd.Flags().BoolVar(&check, "check", false, "exit 1 when changes would be made")
	cmd.Flags().StringVar(&format, "format", "markdown", "stdout format: markdown, yaml, or json")
	cmd.Flags().StringVar(&reportDir, "report-dir", "", "write migration.md and migration.yaml to this directory")
	cmd.Flags().BoolVar(&hierarchical, "hierarchical", false, "migrate Go dependents in isolated local worktrees")
	cmd.Flags().StringVar(&githubDir, "github-dir", "", "canonical GitHub clone root (defaults to --projects-root)")
	cmd.Flags().StringVar(&ref, "ref", "main", "base ref for campaign worktrees")
	cmd.Flags().StringArrayVar(&moduleRefs, "module-ref", nil, "module-specific ref as module=ref (repeatable)")
	cmd.Flags().StringVar(&verify, "verify", "full", "post-apply verification: compile, test, full, or none")
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "skip post-apply verification")
	cmd.Flags().BoolVar(&commit, "commit", false, "commit verified campaign changes on local branches")
	cmd.Flags().BoolVar(&push, "push", false, "push committed campaign branches without opening pull requests")
	cmd.Flags().BoolVar(&pr, "pr", false, "open pull requests for pushed campaign branches")
	cmd.Flags().BoolVar(&merge, "merge", false, "merge campaign pull requests only after required GitHub checks pass")
	cmd.Flags().IntVar(&parallel, "parallel", 1, "maximum independent repositories to migrate concurrently")
	cmd.Flags().BoolVar(&resume, "resume", false, "resume existing campaign worktrees and preserve partial changes")
	cmd.Flags().BoolVar(&cleanup, "cleanup", false, "remove clean campaign worktrees without touching branches or reports")
	return cmd
}

func parseModuleRefs(values []string) (map[string]string, error) {
	refs := make(map[string]string, len(values))
	for _, value := range values {
		module, ref, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(module) == "" || strings.TrimSpace(ref) == "" {
			return nil, fmt.Errorf("invalid --module-ref %q (want module=ref)", value)
		}
		if _, exists := refs[module]; exists {
			return nil, fmt.Errorf("duplicate --module-ref for %s", module)
		}
		refs[module] = ref
	}
	return refs, nil
}

func validateFormat(format string) error {
	switch format {
	case "markdown", "yaml", "json":
		return nil
	}
	return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
}
