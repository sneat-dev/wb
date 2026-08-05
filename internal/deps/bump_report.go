package deps

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/encode"
)

// Markdown renders the wave, repository, and release-evidence index.
func (report BumpReport) Markdown() string {
	var output strings.Builder
	output.WriteString("# WB dependency bump waves\n\n")
	fmt.Fprintf(&output, "- Operation: `%s`\n", report.Operation)
	fmt.Fprintf(&output, "- Ecosystem: `%s`\n", report.Ecosystem)
	fmt.Fprintf(&output, "- Status: `%s`\n", report.Status)
	if report.Phase != "" {
		fmt.Fprintf(&output, "- Phase: `%s`\n", report.Phase)
	}
	if report.Progress.RepositoriesTotal > 0 {
		fmt.Fprintf(&output, "- Progress: wave `%d`, repositories `%d/%d`", report.Progress.Wave, report.Progress.RepositoriesCompleted, report.Progress.RepositoriesTotal)
		if report.Progress.LastRepository != "" {
			fmt.Fprintf(&output, "; last completed `%s`", report.Progress.LastRepository)
		}
		output.WriteByte('\n')
	}
	fmt.Fprintf(&output, "- Base ref: `%s`\n", report.BaseRef)
	fmt.Fprintf(&output, "- Waves: `%d`\n\n", len(report.Waves))
	output.WriteString("## Seed release events\n\n")
	for _, event := range report.SeedEvents {
		fmt.Fprintf(&output, "- `%s@%s` — `%s`\n", event.Dependency, event.Version, event.Source)
	}
	if len(report.DiscoverySkips) > 0 {
		output.WriteString("\n## Skipped discovery failures\n\n")
		output.WriteString("Each repository below failed discovery but was not treated as fatal: either a local scan proved it carries no relevant manifest, or its local clone was unreadable and needs manual repair. Neither case was silently dropped:\n\n")
		for _, skip := range report.DiscoverySkips {
			fmt.Fprintf(&output, "- `%s` — %s\n", skip.Repository, skip.Reason)
		}
	}
	for _, wave := range report.Waves {
		fmt.Fprintf(&output, "\n## Wave %d — `%s`\n\n", wave.Index, wave.Status)
		output.WriteString("Events:\n\n")
		for _, event := range wave.Events {
			fmt.Fprintf(&output, "- `%s@%s` — `%s`\n", event.Dependency, event.Version, event.Source)
		}
		if len(wave.Refreshes) > 0 {
			output.WriteString("\n### Stale-event registry checks\n\n")
			output.WriteString("| Dependency | Before | After | Checked | Reason |\n")
			output.WriteString("|---|---|---|---|---|\n")
			for _, refresh := range wave.Refreshes {
				fmt.Fprintf(&output, "| `%s` | `%s` | `%s` | `%s` | %s |\n",
					refresh.Dependency, refresh.Before, refresh.After, refresh.CheckedAt.Format(time.RFC3339), escapeTable(refresh.Reason))
			}
		}
		if len(wave.DeferredRepositories) > 0 {
			output.WriteString("\n### Deferred to coalesce releases\n\n")
			output.WriteString("No worktree or CI run was started for these later provider-path repositories: ")
			fmt.Fprintf(&output, "`%s`.\n", strings.Join(wave.DeferredRepositories, "`, `"))
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

// JSON renders the same wave state with the field names YAML uses.
func (report BumpReport) JSON() ([]byte, error) { return encode.JSON(report) }

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
