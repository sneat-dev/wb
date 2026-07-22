package deps

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Markdown renders the wave, repository, and release-evidence index.
func (report BumpReport) Markdown() string {
	var output strings.Builder
	output.WriteString("# WB dependency bump waves\n\n")
	fmt.Fprintf(&output, "- Operation: `%s`\n", report.Operation)
	fmt.Fprintf(&output, "- Ecosystem: `%s`\n", report.Ecosystem)
	fmt.Fprintf(&output, "- Status: `%s`\n", report.Status)
	fmt.Fprintf(&output, "- Base ref: `%s`\n", report.BaseRef)
	fmt.Fprintf(&output, "- Waves: `%d`\n\n", len(report.Waves))
	output.WriteString("## Seed release events\n\n")
	for _, event := range report.SeedEvents {
		fmt.Fprintf(&output, "- `%s@%s` — `%s`\n", event.Dependency, event.Version, event.Source)
	}
	for _, wave := range report.Waves {
		fmt.Fprintf(&output, "\n## Wave %d — `%s`\n\n", wave.Index, wave.Status)
		output.WriteString("Events:\n\n")
		for _, event := range wave.Events {
			fmt.Fprintf(&output, "- `%s@%s` — `%s`\n", event.Dependency, event.Version, event.Source)
		}
		output.WriteString("\n| Repository | Status | Reason | Changed | Commit | PR | Merged |\n")
		output.WriteString("|---|---|---|---:|---|---|---|\n")
		for _, repository := range wave.Repositories {
			pr := ""
			if repository.PR != "" {
				pr = "[PR](" + repository.PR + ")"
			}
			fmt.Fprintf(&output, "| `%s` | `%s` | %s | `%d` | `%s` | %s | `%t` |\n",
				repository.Repository, repository.Status, escapeTable(repository.Reason), len(repository.ChangedFiles), repository.Commit, pr, repository.Merged)
		}
		if len(wave.Releases) > 0 {
			output.WriteString("\n### Release evidence\n\n")
			output.WriteString("| Module | Repository | Before | After | Expected requirements | Status | Source | Reason |\n")
			output.WriteString("|---|---|---|---|---|---|---|---|\n")
			for _, release := range wave.Releases {
				fmt.Fprintf(&output, "| `%s` | `%s` | `%s` | `%s` | %s | `%s` | `%s` | %s |\n",
					release.Module, release.Repository, release.Before, release.After, expectedRequirementsMarkdown(release.ExpectedRequirements), release.Status, release.Source, escapeTable(release.Reason))
			}
		}
		for _, repository := range wave.Repositories {
			if len(repository.Decisions) == 0 {
				continue
			}
			fmt.Fprintf(&output, "\n### %s decisions\n\n", repository.Repository)
			if repository.WorktreeDir != "" {
				fmt.Fprintf(&output, "Inspect the detailed patch with `git -C %s diff origin/%s`.\n\n", shellQuote(repository.WorktreeDir), repository.Ref)
			}
			for _, decision := range repository.Decisions {
				observed := decision.BeforeVersion
				if observed == "" {
					observed = "unknown"
				}
				fmt.Fprintf(&output, "- `%s` in `%s`: `%s` → `%s` (`%s`) — %s\n",
					decision.Dependency, decision.File, observed, decision.AfterVersion, decision.Action, decision.Reason)
			}
		}
	}
	return output.String()
}

func expectedRequirementsMarkdown(requirements map[string]string) string {
	if len(requirements) == 0 {
		return "—"
	}
	values := make([]string, 0, len(requirements))
	for dependency, version := range requirements {
		values = append(values, "`"+dependency+"@"+version+"`")
	}
	sort.Strings(values)
	return strings.Join(values, "<br>")
}

// YAML renders deterministic machine-readable wave state.
func (report BumpReport) YAML() ([]byte, error) { return yaml.Marshal(report) }

// WriteBumpReports atomically replaces the human and machine campaign indexes.
func WriteBumpReports(directory string, report BumpReport) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(directory, "deps-bump.md"), []byte(report.Markdown()), 0o644); err != nil {
		return err
	}
	raw, err := report.YAML()
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(directory, "deps-bump.yaml"), raw, 0o644)
}

// LoadBumpReport loads persisted resume state.
func LoadBumpReport(directory string) (BumpReport, error) {
	contents, err := os.ReadFile(filepath.Join(directory, "deps-bump.yaml"))
	if err != nil {
		return BumpReport{}, err
	}
	var report BumpReport
	if err := yaml.Unmarshal(contents, &report); err != nil {
		return BumpReport{}, err
	}
	return report, nil
}
