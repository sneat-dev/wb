package cmdhooks

import (
	"io"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/hooks"
)

func printHooksCheck(out io.Writer, report hooks.CheckReport) error {
	if err := shared.WriteLine(out, report.RepoRoot); err != nil {
		return err
	}
	return printHooksCheckDetails(out, report)
}

func printHooksCheckDetails(out io.Writer, report hooks.CheckReport) error {
	if len(report.ConfigPaths) == 0 {
		if err := shared.WriteLine(out, "  ✓ conservative built-in templates"); err != nil {
			return err
		}
	} else {
		for _, path := range report.ConfigPaths {
			if err := shared.WriteLine(out, "  ✓ policy", path); err != nil {
				return err
			}
		}
	}
	if report.ProfilesAuto && len(report.ActiveProfiles) == 0 {
		if err := shared.WriteLine(out, "  ✓ automatic profiles enabled; none detected"); err != nil {
			return err
		}
	}
	for _, profile := range report.ActiveProfiles {
		if err := shared.WriteFormat(out, "  ✓ profile %s (%s)\n", profile.Name, profile.Reason); err != nil {
			return err
		}
	}
	for _, profile := range report.ExcludedProfiles {
		if err := shared.WriteFormat(out, "  ! profile %s (explicitly excluded by policy)\n", profile); err != nil {
			return err
		}
	}
	hookNames := make([]string, 0, len(report.HookBlocks))
	for hookName := range report.HookBlocks {
		hookNames = append(hookNames, hookName)
	}
	sort.Strings(hookNames)
	for _, hookName := range hookNames {
		if err := shared.WriteLine(out, "  ✓ "+hookName+" blocks", strings.Join(report.HookBlocks[hookName], ", ")); err != nil {
			return err
		}
	}
	if len(report.Findings) == 0 {
		if err := shared.WriteLine(out, "  ✓ core.hooksPath", report.ManagedPath); err != nil {
			return err
		}
		if err := shared.WriteLine(out, "  ✓ managed hooks", strings.Join(report.Hooks, ", ")); err != nil {
			return err
		}
		if report.MetricsPath != "" {
			if err := shared.WriteLine(out, "  ✓ local metrics", report.MetricsPath); err != nil {
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
		if err := shared.WriteFormat(out, "  ✗ %s: %s%s\n", finding.Code, finding.Message, where); err != nil {
			return err
		}
	}
	return nil
}

func printHookMetrics(out io.Writer, summary hooks.MetricsSummary, metricsFile string) error {
	if err := shared.WriteFormat(out, "Local hook metrics · %s through %s\n", summary.From, summary.Through); err != nil {
		return err
	}
	if summary.RepositoryFilter != "" {
		if err := shared.WriteLine(out, "Repository filter:", summary.RepositoryFilter); err != nil {
			return err
		}
	}
	for _, day := range summary.Days {
		if err := shared.WriteFormat(out, "%s  commits %-20s %3d  pushes %-20s %3d  failures %d\n",
			day.Date, metricBar(day.Commits), day.Commits, metricBar(day.PushAttempts), day.PushAttempts, day.HookFailures); err != nil {
			return err
		}
	}
	if err := shared.WriteFormat(out, "Totals: %d commits · %d push attempts · %d commit checks · %d failures · %d hook runs\n",
		summary.Commits, summary.PushAttempts, summary.CommitChecks, summary.HookFailures, summary.HookRuns); err != nil {
		return err
	}
	if err := shared.WriteFormat(out, "Average hook duration: %s\n", time.Duration(summary.AverageDurationMS)*time.Millisecond); err != nil {
		return err
	}
	if len(summary.Blocks) > 0 {
		if err := shared.WriteLine(out, "Blocks:"); err != nil {
			return err
		}
		for _, block := range summary.Blocks {
			if err := shared.WriteFormat(out, "  %-24s runs %3d  failures %2d  average %s\n",
				block.ID, block.Runs, block.Failures, time.Duration(block.AverageDurationMS)*time.Millisecond); err != nil {
				return err
			}
		}
	}
	if err := shared.WriteLine(out, "Pushes are pre-push attempts; Git has no post-push hook to confirm remote acceptance."); err != nil {
		return err
	}
	return shared.WriteLine(out, "Events:", metricsFile)
}

func printHookProfileDelta(out io.Writer, delta hooks.ProfileDelta, metricsFile string) error {
	if err := shared.WriteFormat(out, "Hook profile cost · %s through %s\n\n", delta.From, delta.Through); err != nil {
		return err
	}
	if err := shared.WriteFormat(out, "%-14s %6s %9s %9s %9s %9s\n", "profile", "runs", "failures", "total ms", "avg ms", "budget ms"); err != nil {
		return err
	}
	rows := []struct {
		name string
		cost hooks.ProfileCost
	}{
		{"commit", delta.Commit},
		{"stream push", delta.StreamPush},
		{"other push", delta.OtherPush},
	}
	for _, row := range rows {
		if err := shared.WriteFormat(out, "%-14s %6d %9d %9d %9d %9d\n",
			row.name, row.cost.Runs, row.cost.Failures, row.cost.TotalDurationMS,
			row.cost.AverageDurationMS, row.cost.MaxDurationMS); err != nil {
			return err
		}
	}
	if err := shared.WriteFormat(out,
		"\n%d push(es) to a stream branch ran no local verification, saving about %d ms at the measured %d ms average of a non-stream push.\n",
		delta.SavedRuns, delta.SavedDurationMS, delta.SavedBasisMS); err != nil {
		return err
	}
	if len(delta.Blocks) > 0 {
		if err := shared.WriteLine(out, "\nper-block cost:"); err != nil {
			return err
		}
		for _, block := range delta.Blocks {
			if err := shared.WriteFormat(out, "  %-28s runs %4d  avg %6d ms  failures %d\n",
				block.ID, block.Runs, block.AverageDurationMS, block.Failures); err != nil {
				return err
			}
		}
	}
	for _, unmeasured := range delta.Unmeasured {
		if err := shared.WriteFormat(out, "  ? not measured: %s\n", unmeasured); err != nil {
			return err
		}
	}
	return shared.WriteFormat(out, "\nsource: %s\n", metricsFile)
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
