package deps

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/encode"
)

// Markdown renders a linked drift index for people and agents.
func (report DriftReport) Markdown() string {
	var output strings.Builder
	fmt.Fprintf(&output, "# WB dependency drift (%s)\n\n", report.Ecosystem)
	fmt.Fprintf(&output, "- Mode: `%s`\n", report.Mode)
	fmt.Fprintf(&output, "- Base ref: `%s`\n", report.BaseRef)
	fmt.Fprintf(&output, "- Observed at: `%s`\n", report.ObservedAt.UTC().Format(timeRFC3339))
	fmt.Fprintf(&output, "- Repositories: `%d`\n", report.Summary.Repositories)
	fmt.Fprintf(&output, "- Dependencies: `%d`\n\n", report.Summary.Dependencies)
	fmt.Fprintf(&output, "- Converged: `%d` · divergent: `%d` · replaced: `%d` · major-path split: `%d` · behind latest: `%d` · unavailable: `%d` · error: `%d`\n\n",
		report.Summary.Converged, report.Summary.Divergent, report.Summary.Replaced, report.Summary.MajorSplit, report.Summary.Behind, report.Summary.Unavailable, report.Summary.Error)

	if len(report.Excluded) > 0 {
		output.WriteString("## Excluded repositories\n\n")
		for _, slug := range report.Excluded {
			fmt.Fprintf(&output, "- `%s` — never inspected because it matched an --exclude pattern\n", slug)
		}
		output.WriteByte('\n')
	}

	if len(report.DiscoverySkips) > 0 {
		output.WriteString("## Discovery skips\n\n")
		for _, skip := range report.DiscoverySkips {
			fmt.Fprintf(&output, "- `%s`: %s\n", skip.Repository, escapeTable(skip.Reason))
		}
		output.WriteByte('\n')
	}

	output.WriteString("## Dependency groups\n\n")
	output.WriteString("| Dependency | Classification | Versions | Latest | Behind | Reason |\n")
	output.WriteString("|---|---|---|---|---|---|\n")
	for _, group := range report.Groups {
		versions := make([]string, 0, len(group.Versions))
		for _, version := range group.Versions {
			versions = append(versions, fmt.Sprintf("`%s` (%s: %s)", version.Version, version.Kind, strings.Join(version.Repositories, ", ")))
		}
		latest := "—"
		if group.Latest != nil {
			if group.Latest.Value != "" {
				latest = "`" + group.Latest.Value + "`"
			} else if group.Latest.Reason != "" {
				latest = escapeTable(group.Latest.Reason)
			}
		}
		behind := "—"
		if group.Behind {
			behind = escapeTable(strings.Join(group.BehindRepositories, ", "))
		}
		fmt.Fprintf(&output, "| `%s` | `%s` | %s | %s | %s | %s |\n",
			group.Dependency, group.Classification, escapeTable(strings.Join(versions, "; ")), latest, behind, escapeTable(group.Reason))
	}

	for _, repository := range report.Repositories {
		fmt.Fprintf(&output, "\n## %s\n\n", repository.Repository)
		fmt.Fprintf(&output, "- Status: `%s`", repository.Status)
		if repository.Reason != "" {
			fmt.Fprintf(&output, " — %s", repository.Reason)
		}
		output.WriteByte('\n')
		if repository.Path != "" {
			fmt.Fprintf(&output, "- Path: [%s](%s)\n", repository.Path, localURL(repository.Path))
		}
		if len(repository.Dependencies) == 0 {
			continue
		}
		output.WriteString("\n| Dependency | Field | Declared | Selected | Replacement | Latest |\n")
		output.WriteString("|---|---|---|---|---|---|\n")
		for _, dependency := range repository.Dependencies {
			replacement := "—"
			if dependency.Replacement != nil {
				replacement = fmt.Sprintf("`%s` → `%s`", dependency.Replacement.OldPath, dependency.Replacement.NewPath)
			}
			latest := "—"
			if dependency.Latest != nil {
				if dependency.Latest.Value != "" {
					latest = "`" + dependency.Latest.Value + "`"
				} else if dependency.Latest.Reason != "" {
					latest = escapeTable(dependency.Latest.Reason)
				}
			}
			field := "—"
			if dependency.Field != "" {
				field = "`" + dependency.Field + "`"
			}
			fmt.Fprintf(&output, "| `%s` | %s | `%s` | `%s` | %s | %s |\n",
				dependency.Dependency,
				field,
				evidenceOrDash(dependency.Declared),
				evidenceOrDash(dependency.Selected),
				replacement,
				latest,
			)
		}
	}
	return output.String()
}

func evidenceOrDash(evidence VersionEvidence) string {
	if evidence.Value != "" {
		return evidence.Value
	}
	if evidence.Reason != "" {
		return evidence.Reason
	}
	return "—"
}

const timeRFC3339 = "2006-01-02T15:04:05Z07:00"

// YAML renders the drift report.
func (report DriftReport) YAML() ([]byte, error) {
	return yaml.Marshal(report)
}

// JSON renders the drift report.
func (report DriftReport) JSON() ([]byte, error) {
	return encode.JSON(report)
}

// WriteDriftReports writes markdown, yaml, and json drift artifacts.
func WriteDriftReports(directory string, report DriftReport) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(directory, "deps-drift.md"), []byte(report.Markdown()), 0o644); err != nil {
		return err
	}
	raw, err := report.YAML()
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(directory, "deps-drift.yaml"), raw, 0o644); err != nil {
		return err
	}
	raw, err = report.JSON()
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(directory, "deps-drift.json"), raw, 0o644)
}
