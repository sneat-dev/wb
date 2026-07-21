package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/migrate"
)

func newMigrateCmd() *cobra.Command {
	var (
		apply     bool
		check     bool
		format    string
		reportDir string
	)
	cmd := &cobra.Command{
		Use:   "migrate <spec.hcl> <root> [root...]",
		Short: "Plan or apply a declarative source migration (dry-run by default)",
		Long: "Migrate evaluates a versioned, language-neutral specification against one or more source roots. " +
			"It never edits files unless --apply is set.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
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
	return cmd
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
