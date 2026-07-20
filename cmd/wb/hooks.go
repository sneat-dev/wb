package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/hooks"
)

func newHooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Install, validate, run, and measure user-owned Git hooks",
	}
	cmd.AddCommand(newHooksInstallCmd(false))
	cmd.AddCommand(newHooksCheckCmd())
	cmd.AddCommand(newHooksInstallCmd(true))
	cmd.AddCommand(newHooksRunCmd())
	cmd.AddCommand(newHooksMetricsCmd())
	return cmd
}

func newHooksInstallCmd(repair bool) *cobra.Command {
	var (
		configPath string
		force      bool
		fleet      bool
	)
	verb := "install"
	short := "Install WB-managed Git hook shims"
	if repair {
		verb = "repair"
		short = "Restore missing, stale, or conflicting managed hook shims"
	}
	cmd := &cobra.Command{
		Use:   verb + " [repository-path]",
		Short: short,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if fleet {
				if len(args) > 0 {
					return fmt.Errorf("repository-path cannot be used with --fleet")
				}
				return applyHooksFleet(cmd, configPath, repair, force)
			}
			repoPath := argumentOrCurrent(args)
			result, err := hooks.Apply(hooks.ApplyOptions{
				RepoPath:     repoPath,
				ConfigPath:   configPath,
				WBExecutable: hookExecutable(),
				Repair:       repair,
				Force:        force,
			})
			if err != nil {
				return err
			}
			for _, action := range result.Actions {
				if err := writeLine(cmd.OutOrStdout(), "✓", action); err != nil {
					return err
				}
			}
			if err := writeFormat(cmd.OutOrStdout(), "✓ hooks ready for %s\n", result.Report.RepoRoot); err != nil {
				return err
			}
			if result.Report.MetricsPath != "" {
				if err := writeFormat(cmd.OutOrStdout(), "  local metrics: %s\n", result.Report.MetricsPath); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "explicit hooks policy (default: global + repository policies)")
	cmd.Flags().BoolVar(&fleet, "fleet", false, "process every local repository under --projects-root")
	if repair {
		cmd.Flags().BoolVar(&force, "force", false, "back up unmanaged shims and replace a conflicting core.hooksPath")
	}
	return cmd
}

func newHooksCheckCmd() *cobra.Command {
	var (
		configPath string
		jsonOut    bool
		fleet      bool
	)
	cmd := &cobra.Command{
		Use:     "check [repository-path]",
		Aliases: []string{"validate"},
		Short:   "Validate hook policy, templates, installation, and drift",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if fleet {
				if len(args) > 0 {
					return fmt.Errorf("repository-path cannot be used with --fleet")
				}
				return checkHooksFleet(cmd, configPath, jsonOut)
			}
			report, err := hooks.Check(argumentOrCurrent(args), configPath, hookExecutable())
			if err != nil {
				return err
			}
			if jsonOut {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(report); err != nil {
					return err
				}
			} else {
				if err := printHooksCheck(cmd, report); err != nil {
					return err
				}
			}
			if len(report.Findings) > 0 {
				return &hooksCheckError{count: len(report.Findings)}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "explicit hooks policy (default: global + repository policies)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	cmd.Flags().BoolVar(&fleet, "fleet", false, "process every local repository under --projects-root")
	return cmd
}

func applyHooksFleet(cmd *cobra.Command, configPath string, repair, force bool) error {
	repos, err := localHookRepos(projectsRoot, filterFlag)
	if err != nil {
		return err
	}
	failed := 0
	for _, repo := range repos {
		result, applyErr := hooks.Apply(hooks.ApplyOptions{
			RepoPath:     repo.Path,
			ConfigPath:   configPath,
			WBExecutable: hookExecutable(),
			Repair:       repair,
			Force:        force,
		})
		if applyErr != nil {
			failed++
			if err := writeFormat(cmd.ErrOrStderr(), "✗ %s: %v\n", repo.Slug(), applyErr); err != nil {
				return err
			}
			continue
		}
		if err := writeLine(cmd.OutOrStdout(), repo.Slug()); err != nil {
			return err
		}
		for _, action := range result.Actions {
			if err := writeLine(cmd.OutOrStdout(), "  ✓", action); err != nil {
				return err
			}
		}
		if err := writeLine(cmd.OutOrStdout(), "  ✓ hooks ready"); err != nil {
			return err
		}
	}
	if err := writeFormat(cmd.OutOrStdout(), "Processed %d repositories; %d failed\n", len(repos), failed); err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("hooks operation failed in %d of %d repositories", failed, len(repos))
	}
	return nil
}

type fleetHooksCheck struct {
	Repository string             `json:"repository"`
	Report     *hooks.CheckReport `json:"report,omitempty"`
	Error      string             `json:"error,omitempty"`
}

func checkHooksFleet(cmd *cobra.Command, configPath string, jsonOut bool) error {
	repos, err := localHookRepos(projectsRoot, filterFlag)
	if err != nil {
		return err
	}
	results := make([]fleetHooksCheck, 0, len(repos))
	problems := 0
	for _, repo := range repos {
		report, checkErr := hooks.Check(repo.Path, configPath, hookExecutable())
		entry := fleetHooksCheck{Repository: repo.Slug()}
		if checkErr != nil {
			entry.Error = checkErr.Error()
			problems++
		} else {
			entry.Report = &report
			problems += len(report.Findings)
		}
		results = append(results, entry)
	}
	if jsonOut {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(results); err != nil {
			return err
		}
	} else {
		for _, result := range results {
			if err := writeLine(cmd.OutOrStdout(), result.Repository); err != nil {
				return err
			}
			if result.Error != "" {
				if err := writeLine(cmd.OutOrStdout(), "  ✗", result.Error); err != nil {
					return err
				}
				continue
			}
			if err := printHooksCheckDetails(cmd, *result.Report); err != nil {
				return err
			}
		}
		if err := writeFormat(cmd.OutOrStdout(), "Checked %d repositories; %d problems\n", len(repos), problems); err != nil {
			return err
		}
	}
	if problems > 0 {
		return &hooksCheckError{count: problems, fleet: true}
	}
	return nil
}

func localHookRepos(root, filter string) ([]discover.Repo, error) {
	repos, err := discover.ScanLocal(root)
	if err != nil {
		return nil, fmt.Errorf("scan local repositories: %w", err)
	}
	selected := repos[:0]
	for _, repo := range repos {
		if filter == "" || strings.Contains(repo.Slug(), filter) {
			selected = append(selected, repo)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Slug() < selected[j].Slug() })
	return selected, nil
}

func printHooksCheck(cmd *cobra.Command, report hooks.CheckReport) error {
	out := cmd.OutOrStdout()
	if err := writeLine(out, report.RepoRoot); err != nil {
		return err
	}
	return printHooksCheckDetails(cmd, report)
}

func printHooksCheckDetails(cmd *cobra.Command, report hooks.CheckReport) error {
	out := cmd.OutOrStdout()
	if len(report.ConfigPaths) == 0 {
		if err := writeLine(out, "  ✓ conservative built-in templates"); err != nil {
			return err
		}
	} else {
		for _, path := range report.ConfigPaths {
			if err := writeLine(out, "  ✓ policy", path); err != nil {
				return err
			}
		}
	}
	if report.ProfilesAuto && len(report.ActiveProfiles) == 0 {
		if err := writeLine(out, "  ✓ automatic profiles enabled; none detected"); err != nil {
			return err
		}
	}
	for _, profile := range report.ActiveProfiles {
		if err := writeFormat(out, "  ✓ profile %s (%s)\n", profile.Name, profile.Reason); err != nil {
			return err
		}
	}
	hookNames := make([]string, 0, len(report.HookBlocks))
	for hookName := range report.HookBlocks {
		hookNames = append(hookNames, hookName)
	}
	sort.Strings(hookNames)
	for _, hookName := range hookNames {
		if err := writeLine(out, "  ✓ "+hookName+" blocks", strings.Join(report.HookBlocks[hookName], ", ")); err != nil {
			return err
		}
	}
	if len(report.Findings) == 0 {
		if err := writeLine(out, "  ✓ core.hooksPath", report.ManagedPath); err != nil {
			return err
		}
		if err := writeLine(out, "  ✓ managed hooks", strings.Join(report.Hooks, ", ")); err != nil {
			return err
		}
		if report.MetricsPath != "" {
			if err := writeLine(out, "  ✓ local metrics", report.MetricsPath); err != nil {
				return err
			}
		}
		return nil
	}
	for _, finding := range report.Findings {
		where := ""
		if finding.Path != "" {
			where = " (" + finding.Path + ")"
		}
		if err := writeFormat(out, "  ✗ %s: %s%s\n", finding.Code, finding.Message, where); err != nil {
			return err
		}
	}
	return nil
}

type hooksCheckError struct {
	count int
	fleet bool
}

func (e *hooksCheckError) Error() string {
	if e.fleet {
		return fmt.Sprintf("fleet hooks check found %d problem(s); run `wb hooks repair --fleet`", e.count)
	}
	return fmt.Sprintf("hooks check found %d problem(s); run `wb hooks repair`", e.count)
}

func newHooksRunCmd() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:    "run <hook> [hook-args...]",
		Short:  "Run a configured hook template (normally invoked by a managed shim)",
		Args:   cobra.MinimumNArgs(1),
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := hooks.Run(hooks.RunOptions{
				RepoPath:   ".",
				ConfigPath: configPath,
				Hook:       args[0],
				Args:       args[1:],
				Stdin:      cmd.InOrStdin(),
				Stdout:     cmd.OutOrStdout(),
				Stderr:     cmd.ErrOrStderr(),
			})
			if result.MetricsError != nil {
				// A metrics warning must never turn a successful Git hook into a
				// failure, including when stderr itself is unavailable.
				_ = writeLine(cmd.ErrOrStderr(), "warning: hook succeeded but local metrics could not be recorded:", result.MetricsError)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "explicit hooks policy")
	return cmd
}

func newHooksMetricsCmd() *cobra.Command {
	var (
		configPath  string
		metricsFile string
		repository  string
		days        int
		jsonOut     bool
	)
	cmd := &cobra.Command{
		Use:   "metrics [repository-path]",
		Short: "Chart local commits, push attempts, hook failures, and duration",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			policy, err := hooks.LoadPolicy(argumentOrCurrent(args), configPath)
			if err != nil {
				return err
			}
			if metricsFile == "" {
				metricsFile = policy.Metrics.Path
			}
			events, err := hooks.ReadEvents(metricsFile)
			if err != nil {
				return err
			}
			summary := hooks.Summarize(events, days, repository, time.Now())
			if jsonOut {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(summary)
			}
			return printHookMetrics(cmd, summary, metricsFile)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "explicit hooks policy")
	cmd.Flags().StringVar(&metricsFile, "file", "", "hook events JSONL file")
	cmd.Flags().StringVar(&repository, "repo", "", "only repositories containing this text")
	cmd.Flags().IntVar(&days, "days", 14, "number of calendar days to chart")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable summary JSON")
	return cmd
}

func printHookMetrics(cmd *cobra.Command, summary hooks.MetricsSummary, metricsFile string) error {
	out := cmd.OutOrStdout()
	if err := writeFormat(out, "Local hook metrics · %s through %s\n", summary.From, summary.Through); err != nil {
		return err
	}
	if summary.RepositoryFilter != "" {
		if err := writeLine(out, "Repository filter:", summary.RepositoryFilter); err != nil {
			return err
		}
	}
	for _, day := range summary.Days {
		if err := writeFormat(out, "%s  commits %-20s %3d  pushes %-20s %3d  failures %d\n",
			day.Date, metricBar(day.Commits), day.Commits, metricBar(day.PushAttempts), day.PushAttempts, day.HookFailures); err != nil {
			return err
		}
	}
	if err := writeFormat(out, "Totals: %d commits · %d push attempts · %d commit checks · %d failures · %d hook runs\n",
		summary.Commits, summary.PushAttempts, summary.CommitChecks, summary.HookFailures, summary.HookRuns); err != nil {
		return err
	}
	if err := writeFormat(out, "Average hook duration: %s\n", time.Duration(summary.AverageDurationMS)*time.Millisecond); err != nil {
		return err
	}
	if len(summary.Blocks) > 0 {
		if err := writeLine(out, "Blocks:"); err != nil {
			return err
		}
		for _, block := range summary.Blocks {
			if err := writeFormat(out, "  %-24s runs %3d  failures %2d  average %s\n",
				block.ID, block.Runs, block.Failures, time.Duration(block.AverageDurationMS)*time.Millisecond); err != nil {
				return err
			}
		}
	}
	if err := writeLine(out, "Pushes are pre-push attempts; Git has no post-push hook to confirm remote acceptance."); err != nil {
		return err
	}
	return writeLine(out, "Events:", metricsFile)
}

func writeLine(writer io.Writer, values ...any) error {
	_, err := fmt.Fprintln(writer, values...)
	return err
}

func writeFormat(writer io.Writer, format string, values ...any) error {
	_, err := fmt.Fprintf(writer, format, values...)
	return err
}

func metricBar(value int) string {
	if value <= 0 {
		return "·"
	}
	if value > 20 {
		value = 20
	}
	return strings.Repeat("█", value)
}

func argumentOrCurrent(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	return "."
}

func hookExecutable() string {
	if path, err := os.Executable(); err == nil {
		return path
	}
	if path, err := exec.LookPath("wb"); err == nil {
		return path
	}
	return "wb"
}
