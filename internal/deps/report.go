package deps

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Markdown renders a linked index suitable for human or AI review.
func (report Report) Markdown() string {
	var output strings.Builder
	fmt.Fprintf(&output, "# WB exact dependency set: %s@%s\n\n", report.Target.Dependency, report.Target.Version)
	fmt.Fprintf(&output, "- Ecosystem: `%s`\n", report.Target.Ecosystem)
	fmt.Fprintf(&output, "- Resolved reference: `%s`\n", report.Target.Resolved)
	fmt.Fprintf(&output, "- Status: `%s`\n", report.Status)
	fmt.Fprintf(&output, "- Base ref: `%s`\n", report.BaseRef)
	fmt.Fprintf(&output, "- Parallelism: `%d`\n\n", report.Parallel)
	output.WriteString("## Repository index\n\n")
	output.WriteString("| Repository | Status | Reason | Changed | Commit | PR | Merged |\n")
	output.WriteString("|---|---|---|---:|---|---|---|\n")
	for _, repository := range report.Repositories {
		pr := ""
		if repository.PR != "" {
			pr = "[PR](" + repository.PR + ")"
		}
		fmt.Fprintf(&output, "| `%s` | `%s` | %s | `%d` | `%s` | %s | `%t` |\n",
			repository.Repository, repository.Status, escapeTable(repository.Reason), len(repository.ChangedFiles), repository.Commit, pr, repository.Merged)
	}
	for _, repository := range report.Repositories {
		fmt.Fprintf(&output, "\n## %s\n\n", repository.Repository)
		fmt.Fprintf(&output, "- Status: `%s` — %s\n", repository.Status, repository.Reason)
		fmt.Fprintf(&output, "- Base: `origin/%s`\n", repository.Ref)
		if repository.CanonicalDir != "" {
			fmt.Fprintf(&output, "- Canonical clone: [%s](%s)\n", repository.CanonicalDir, localURL(repository.CanonicalDir))
		}
		if repository.WorktreeDir != "" {
			fmt.Fprintf(&output, "- Operation worktree: [%s](%s) on `%s`\n", repository.WorktreeDir, localURL(repository.WorktreeDir), repository.Branch)
			fmt.Fprintf(&output, "- Inspect diff: `git -C %s diff origin/%s`\n", shellQuote(repository.WorktreeDir), repository.Ref)
		}
		if len(repository.Decisions) > 0 {
			output.WriteString("\n### Dependency decisions\n\n")
			output.WriteString("| File | Before | Observed version | Target | Resolved | After | Action | Reason |\n")
			output.WriteString("|---|---|---|---|---|---|---|---|\n")
			for _, decision := range repository.Decisions {
				file := "`" + decision.File + "`"
				if repository.WorktreeDir != "" {
					file = "[" + decision.File + "](" + localURL(filepath.Join(repository.WorktreeDir, filepath.FromSlash(decision.File))) + ")"
				}
				observed := decision.BeforeVersion
				if observed == "" {
					observed = "unknown"
				}
				fmt.Fprintf(&output, "| %s | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | %s |\n",
					file, decision.BeforeRef, observed, decision.TargetVersion, decision.ResolvedRef, decision.AfterRef, decision.Action, escapeTable(decision.Reason))
			}
		}
		if len(repository.ChangedFiles) > 0 {
			output.WriteString("\n### Changed files\n\n")
			for _, file := range repository.ChangedFiles {
				fmt.Fprintf(&output, "- [%s](%s)\n", file, localURL(filepath.Join(repository.WorktreeDir, filepath.FromSlash(file))))
			}
		}
		if len(repository.Verifications) > 0 {
			output.WriteString("\n### Local verification\n\n")
			for _, verification := range repository.Verifications {
				fmt.Fprintf(&output, "- `%s`: `%s`", verification.Command, verification.Status)
				if verification.Detail != "" {
					fmt.Fprintf(&output, " — %s", verification.Detail)
				}
				output.WriteByte('\n')
			}
		}
		if len(repository.Checks) > 0 {
			output.WriteString("\n### GitHub checks\n\n")
			for _, check := range repository.Checks {
				if check.Link != "" {
					fmt.Fprintf(&output, "- [%s](%s): `%s`\n", check.Name, check.Link, check.Bucket)
				} else {
					fmt.Fprintf(&output, "- `%s`: `%s`\n", check.Name, check.Bucket)
				}
			}
		}
	}
	return output.String()
}

// YAML renders the same deterministic report for tooling.
func (report Report) YAML() ([]byte, error) { return yaml.Marshal(report) }

// WriteReports writes deps-set.md and deps-set.yaml to the requested directory.
func WriteReports(directory string, report Report) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "deps-set.md"), []byte(report.Markdown()), 0o644); err != nil {
		return err
	}
	raw, err := report.YAML()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "deps-set.yaml"), raw, 0o644)
}

func localURL(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func escapeTable(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "|", "\\|"), "\n", " ")
}
