package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/migrate"
)

func newMigrateCmd() *cobra.Command {
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
			if hierarchical {
				code := runHierarchicalMigration(args[0], args[1:], hierarchicalMigrationOptions{
					apply: apply, check: check, format: format, reportDir: reportDir, githubDir: githubDir,
					ref: ref, moduleRefs: moduleRefs, verify: verify, noVerify: noVerify, commit: commit, push: push,
					pr: pr, merge: merge, parallel: parallel, resume: resume, cleanup: cleanup,
					verifyExplicit: cmd.Flags().Changed("verify"),
				})
				if code != 0 {
					os.Exit(code)
				}
				return nil
			}
			if len(args) < 2 {
				return fmt.Errorf("migrate requires at least one source root")
			}
			if commit || push || pr || merge || resume || cleanup || noVerify || cmd.Flags().Changed("verify") || cmd.Flags().Changed("github-dir") || cmd.Flags().Changed("ref") || cmd.Flags().Changed("parallel") || len(moduleRefs) > 0 {
				return fmt.Errorf("--commit, --push, --pr, --merge, --resume, --cleanup, --verify, --no-verify, --github-dir, --ref, --module-ref, and --parallel require --hierarchical")
			}
			code := runMigrate(args[0], args[1:], apply, check, format, reportDir)
			if code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "write the planned changes")
	cmd.Flags().BoolVar(&check, "check", false, "exit 1 when changes would be made")
	cmd.Flags().StringVar(&format, "format", "markdown", "stdout format: markdown or yaml")
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

type hierarchicalMigrationOptions struct {
	apply, check, noVerify, commit, push, pr, merge, resume, cleanup bool
	format, reportDir, githubDir, ref, verify                        string
	moduleRefs                                                       []string
	parallel                                                         int
	verifyExplicit                                                   bool
}

func runHierarchicalMigration(specPath string, roots []string, options hierarchicalMigrationOptions) int {
	if options.cleanup && (options.apply || options.check || options.commit || options.push || options.pr || options.merge || options.resume || options.noVerify || options.verifyExplicit) {
		fmt.Fprintln(os.Stderr, "--cleanup cannot be combined with apply, verification, commit, push, PR, merge, resume, or check options")
		return 2
	}
	if options.check {
		fmt.Fprintln(os.Stderr, "--check is not supported with --hierarchical; use the campaign report from the dry run")
		return 2
	}
	if options.noVerify && options.verifyExplicit {
		fmt.Fprintln(os.Stderr, "--no-verify and --verify cannot be used together")
		return 2
	}
	refs, err := parseModuleRefs(options.moduleRefs)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	spec, err := migrate.Load(specPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	githubDir := options.githubDir
	if githubDir == "" {
		githubDir = projectsRoot
	}
	if options.cleanup {
		if len(roots) != 0 {
			fmt.Fprintln(os.Stderr, "--cleanup does not take a source root")
			return 2
		}
		worktrees, err := migrate.CleanupCampaignWorktrees(githubDir, spec.ID)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		for _, worktree := range worktrees {
			fmt.Println(worktree)
		}
		return 0
	}
	if len(roots) != 1 {
		fmt.Fprintln(os.Stderr, "--hierarchical requires exactly one source root")
		return 2
	}
	verification := migrate.Verification(options.verify)
	if options.noVerify {
		verification = migrate.VerifyNone
	}
	reportDir := options.reportDir
	if reportDir == "" {
		reportDir = filepath.Join(githubDir, ".wb", "reports", spec.ID)
	}
	report, runErr := migrate.RunCampaign(spec, roots[0], migrate.CampaignOptions{
		GitHubDir: githubDir, Ref: options.ref, ModuleRefs: refs, Apply: options.apply,
		Verify: verification, Commit: options.commit, Push: options.push, PR: options.pr, Merge: options.merge, Resume: options.resume,
		Parallel: options.parallel, ReportDir: reportDir,
	})
	if err := migrate.WriteCampaignReports(reportDir, report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := writeCampaignReport(report, options.format); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if runErr != nil {
		fmt.Fprintln(os.Stderr, runErr)
		return 2
	}
	return 0
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

func runMigrate(specPath string, roots []string, apply, check bool, format, reportDir string) int {
	if apply && check {
		fmt.Fprintln(os.Stderr, "--apply and --check cannot be used together")
		return 2
	}
	spec, err := migrate.Load(specPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	plan, err := migrate.BuildPlan(spec, roots...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	report := migrate.NewReport(spec, plan, roots, "planned")
	if apply {
		if err := migrate.Apply(plan); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		report.Status = "applied"
		if err := writeMigrationReport(report, format, reportDir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return 0
	}
	if err := writeMigrationReport(report, format, reportDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if check && len(plan.Changes) > 0 {
		return 1
	}
	return 0
}

func writeMigrationReport(report migrate.Report, format, reportDir string) error {
	if reportDir != "" {
		if err := migrate.WriteReports(reportDir, report); err != nil {
			return err
		}
	}
	switch format {
	case "markdown":
		_, err := fmt.Print(report.Markdown())
		return err
	case "yaml":
		raw, err := report.YAML()
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(raw)
		return err
	default:
		return fmt.Errorf("unknown --format %q (want markdown or yaml)", format)
	}
}

func writeCampaignReport(report migrate.CampaignReport, format string) error {
	switch format {
	case "markdown":
		_, err := fmt.Print(report.Markdown())
		return err
	case "yaml":
		raw, err := report.YAML()
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(raw)
		return err
	default:
		return fmt.Errorf("unknown --format %q (want markdown or yaml)", format)
	}
}
