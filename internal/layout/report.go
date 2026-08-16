package layout

import (
	"fmt"
	"strings"
	"time"
)

// Markdown renders a human/agent layout audit index.
func (report Report) Markdown() string {
	var out strings.Builder
	out.WriteString("# WB layout audit\n\n")
	fmt.Fprintf(&out, "- Projects root: `%s`\n", report.ProjectsRoot)
	fmt.Fprintf(&out, "- Observed at: `%s`\n", report.ObservedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&out, "- Inspected: `%d` · ok: `%d` · top-level: `%d` · misowned: `%d` · no-origin: `%d` · unreadable: `%d`\n\n",
		report.Summary.Inspected, report.Summary.OK, report.Summary.TopLevel, report.Summary.Misowned, report.Summary.NoOrigin, report.Summary.Unreadable)
	if len(report.Findings) == 0 {
		out.WriteString("No Git checkouts found under the projects root.\n")
		return out.String()
	}
	out.WriteString("| Path | Kind | Path slug | Origin | Reason |\n|---|---|---|---|---|\n")
	for _, finding := range report.Findings {
		fmt.Fprintf(&out, "| `%s` | `%s` | `%s` | `%s` | %s |\n",
			finding.Path, finding.Kind, dash(finding.PathSlug), dash(finding.OriginSlug), escape(finding.Reason))
	}
	return out.String()
}

// Markdown renders a clean plan/result.
func (report CleanReport) Markdown() string {
	var out strings.Builder
	out.WriteString("# WB layout clean\n\n")
	fmt.Fprintf(&out, "- Projects root: `%s`\n", report.ProjectsRoot)
	if report.DryRun {
		out.WriteString("- Mode: `dry-run` (pass `--apply` to remove)\n\n")
	} else {
		out.WriteString("- Mode: `apply`\n\n")
	}
	if len(report.Actions) == 0 {
		out.WriteString("No top-level clones to consider.\n")
		return out.String()
	}
	out.WriteString("| Path | Status | Origin | Reason |\n|---|---|---|---|\n")
	for _, action := range report.Actions {
		fmt.Fprintf(&out, "| `%s` | `%s` | `%s` | %s |\n",
			action.Path, action.Status, dash(action.OriginSlug), escape(action.Reason))
	}
	return out.String()
}

func dash(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

func escape(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "|", "\\|"), "\n", " ")
}
