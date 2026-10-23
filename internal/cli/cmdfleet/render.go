package cmdfleet

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/cli/statusview"
	"github.com/sneat-dev/wb/internal/fleetinspect"
	"gopkg.in/yaml.v3"
)

func writeFleetStatsOutput(out io.Writer, deps Dependencies, report fleetinspect.StatsReport, format, reportDir string) error {
	if reportDir != "" {
		if err := deps.MkdirAll(reportDir, 0o755); err != nil {
			return err
		}
		if err := deps.WriteFile(filepath.Join(reportDir, "fleet-stats.md"), []byte(fleetStatsMarkdown(report)), 0o644); err != nil {
			return err
		}
		// These concrete reports contain only primitive fields, slices, and pointers;
		// they have no custom marshalers or error-valued fields.
		raw, _ := yaml.Marshal(report)
		if err := deps.WriteFile(filepath.Join(reportDir, "fleet-stats.yaml"), raw, 0o644); err != nil {
			return err
		}
	}
	switch format {
	case "markdown":
		_, err := fmt.Fprint(out, fleetStatsMarkdown(report))
		return err
	case "yaml":
		// These concrete reports contain only primitive fields, slices, and pointers;
		// they have no custom marshalers or error-valued fields.
		raw, _ := yaml.Marshal(report)
		_, err := out.Write(raw)
		return err
	case "json":
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
}

func writeFleetOverviewOutput(out io.Writer, deps Dependencies, report fleetinspect.OverviewReport, format, reportDir string, details bool) error {
	if reportDir != "" {
		if err := deps.MkdirAll(reportDir, 0o755); err != nil {
			return err
		}
		if err := deps.WriteFile(filepath.Join(reportDir, "fleet-overview.md"), []byte(fleetOverviewMarkdown(report, details)), 0o644); err != nil {
			return err
		}
		// These concrete reports contain only primitive fields, slices, and pointers;
		// they have no custom marshalers or error-valued fields.
		raw, _ := yaml.Marshal(report)
		if err := deps.WriteFile(filepath.Join(reportDir, "fleet-overview.yaml"), raw, 0o644); err != nil {
			return err
		}
	}
	switch format {
	case "markdown":
		_, err := fmt.Fprint(out, fleetOverviewMarkdown(report, details))
		return err
	case "yaml":
		// These concrete reports contain only primitive fields, slices, and pointers;
		// they have no custom marshalers or error-valued fields.
		raw, _ := yaml.Marshal(report)
		_, err := out.Write(raw)
		return err
	case "json":
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
}

func fleetStatsMarkdown(report fleetinspect.StatsReport) string {
	var out strings.Builder
	out.WriteString("# WB fleet stats\n\n")
	fmt.Fprintf(&out, "- inventory: %s\n", fleetInventorySummary(report.Inventory))
	fmt.Fprintf(&out, "- git: %s\n", fleetGitSummary(report.Git))
	fmt.Fprintf(&out, "- layout: %s\n", fleetLayoutSummary(report.Layout))
	fmt.Fprintf(&out, "- worktrees: %s\n", fleetWorktreeSummary(report.Worktrees))
	if report.Remote != nil {
		fmt.Fprintf(&out, "- remote: %s\n", fleetRemoteSummary(*report.Remote))
	}
	if report.Hooks != nil {
		fmt.Fprintf(&out, "- hooks: %s\n", fleetHooksSummary(*report.Hooks))
	}
	return out.String()
}

func fleetOverviewMarkdown(report fleetinspect.OverviewReport, details bool) string {
	var out strings.Builder
	out.WriteString("# WB fleet overview\n\n")
	out.WriteString("## Stats\n\n")
	fmt.Fprintf(&out, "- inventory: %s\n", fleetInventorySummary(report.Stats.Inventory))
	fmt.Fprintf(&out, "- git: %s\n", fleetGitSummary(report.Stats.Git))
	fmt.Fprintf(&out, "- layout: %s\n", fleetLayoutSummary(report.Stats.Layout))
	fmt.Fprintf(&out, "- worktrees: %s\n", fleetWorktreeSummary(report.Stats.Worktrees))
	if report.Stats.Remote != nil {
		fmt.Fprintf(&out, "- remote: %s\n", fleetRemoteSummary(*report.Stats.Remote))
	}
	if report.Stats.Hooks != nil {
		fmt.Fprintf(&out, "- hooks: %s\n", fleetHooksSummary(*report.Stats.Hooks))
	}
	out.WriteString("\n## Attention\n\n")
	attention := strings.TrimPrefix(statusview.Markdown(report.Status, details, "# WB fleet status\n\n"), "# WB fleet status\n\n")
	out.WriteString(attention)
	return out.String()
}

func fleetInventorySummary(stats fleetinspect.InventoryStats) string {
	return fmt.Sprintf("%d organization%s · %d local repository%s",
		stats.Organizations, pluralSuffix(stats.Organizations),
		stats.Repositories, pluralSuffix(stats.Repositories))
}

func fleetGitSummary(stats fleetinspect.GitStats) string {
	return fmt.Sprintf("%d attention · %d clean · %d error (%d inspected)",
		stats.Attention, stats.Clean, stats.Error, stats.Inspected)
}

func fleetLayoutSummary(stats fleetinspect.LayoutStats) string {
	return fmt.Sprintf("%d ok · %d top-level · %d misowned · %d no-origin · %d unreadable",
		stats.OK, stats.TopLevel, stats.Misowned, stats.NoOrigin, stats.Unreadable)
}

func fleetRemoteSummary(stats fleetinspect.RemoteStats) string {
	return fmt.Sprintf("%d would-clone · %d would-pull · %d skipped-dirty · %d ignored · %d empty-remote · %d archived-unlandable · %d local-only · %d remote-only · %d noop · %d error",
		stats.WouldClone, stats.WouldPull, stats.SkippedDirty, stats.Ignored, stats.EmptyRemote, stats.ArchivedUnlandable, stats.LocalOnly, stats.RemoteOnly, stats.NoOp, stats.Error)
}

func fleetHooksSummary(stats fleetinspect.HooksStats) string {
	return fmt.Sprintf("%d finding%s across %d repository%s (%d error%s)",
		stats.Findings, pluralSuffix(stats.Findings),
		stats.Repositories, pluralSuffix(stats.Repositories),
		stats.Errors, pluralSuffix(stats.Errors))
}

func fleetWorktreeSummary(stats fleetinspect.WorktreeStats) string {
	return fmt.Sprintf("%d task%s · %d checkout%s · %d dirty · %d locked",
		stats.Tasks, pluralSuffix(stats.Tasks),
		stats.Checkouts, pluralSuffix(stats.Checkouts),
		stats.Dirty, stats.Locked)
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}
