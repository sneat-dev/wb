package migrate

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Report is the portable index of a migration plan. It intentionally records
// files and operations, rather than embedding a large diff: reviewers and AI
// agents can ask Git for the exact diff at the repository and path named here.
type Report struct {
	SchemaVersion int             `yaml:"schema_version"`
	Migration     ReportMigration `yaml:"migration"`
	Status        string          `yaml:"status"`
	Files         []ReportFile    `yaml:"files"`
	ReviewItems   []ReportFinding `yaml:"review_items,omitempty"`
}

// ReportMigration identifies the migration that produced a report.
type ReportMigration struct {
	ID      string `yaml:"id"`
	Title   string `yaml:"title,omitempty"`
	Version int    `yaml:"version"`
}

// ReportFile identifies one changed file and the Git command that reveals its
// detailed patch after an apply operation.
type ReportFile struct {
	Root           string   `yaml:"root"`
	Path           string   `yaml:"path"`
	AbsolutePath   string   `yaml:"absolute_path"`
	Language       string   `yaml:"language"`
	Operations     []string `yaml:"operations"`
	OriginalSHA256 string   `yaml:"original_sha256"`
	GitDiffCommand string   `yaml:"git_diff_command"`
}

// ReportFinding is a semantic-review item linked to a local source file.
type ReportFinding struct {
	Root         string `yaml:"root"`
	Path         string `yaml:"path"`
	AbsolutePath string `yaml:"absolute_path"`
	Language     string `yaml:"language"`
	RuleID       string `yaml:"rule_id"`
	Message      string `yaml:"message"`
	Lines        []int  `yaml:"lines"`
}

// NewReport converts a plan into a sorted, deterministic report. Status is
// normally "planned" or "applied".
func NewReport(spec Spec, plan Plan, roots []string, status string) Report {
	absRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		if abs, err := filepath.Abs(root); err == nil {
			absRoots = append(absRoots, abs)
		}
	}
	sort.Slice(absRoots, func(i, j int) bool { return len(absRoots[i]) > len(absRoots[j]) })
	report := Report{
		SchemaVersion: 1,
		Migration:     ReportMigration{ID: spec.ID, Title: spec.Title, Version: spec.Version},
		Status:        status,
		Files:         make([]ReportFile, 0, len(plan.Changes)),
		ReviewItems:   make([]ReportFinding, 0, len(plan.Findings)),
	}
	for _, finding := range plan.Findings {
		root, rel := reportPath(finding.Path, absRoots)
		report.ReviewItems = append(report.ReviewItems, ReportFinding{
			Root: root, Path: rel, AbsolutePath: finding.Path, Language: finding.Language,
			RuleID: finding.RuleID, Message: finding.Message, Lines: append([]int(nil), finding.Lines...),
		})
	}
	for _, change := range plan.Changes {
		root, rel := reportPath(change.Path, absRoots)
		report.Files = append(report.Files, ReportFile{
			Root:           root,
			Path:           rel,
			AbsolutePath:   change.Path,
			Language:       change.Language,
			Operations:     append([]string(nil), change.Steps...),
			OriginalSHA256: change.OriginalSHA256,
			GitDiffCommand: fmt.Sprintf("git -C %s diff -- %s", shellQuote(root), shellQuote(rel)),
		})
	}
	sort.Slice(report.Files, func(i, j int) bool {
		if report.Files[i].Root == report.Files[j].Root {
			return report.Files[i].Path < report.Files[j].Path
		}
		return report.Files[i].Root < report.Files[j].Root
	})
	return report
}

// Markdown renders a readable report with local-file links and commands that
// show each file's detailed diff after the migration is applied.
func (r Report) Markdown() string {
	var out strings.Builder
	fmt.Fprintf(&out, "# WB migration: %s\n\n", r.Migration.ID)
	if r.Migration.Title != "" {
		fmt.Fprintf(&out, "%s\n\n", r.Migration.Title)
	}
	fmt.Fprintf(&out, "- Schema: `%d`\n- Version: `%d`\n- Status: `%s`\n- Changed files: `%d`\n- Review items: `%d`\n\n", r.SchemaVersion, r.Migration.Version, r.Status, len(r.Files), len(r.ReviewItems))
	if len(r.Files) == 0 {
		out.WriteString("No files require a change.\n")
	} else {
		out.WriteString("## Change index\n\n")
		out.WriteString("| File | Language | Operations | Original SHA-256 |\n")
		out.WriteString("|---|---|---|---|\n")
		for _, file := range r.Files {
			fmt.Fprintf(&out, "| [%s](%s) | `%s` | `%s` | `%s` |\n", file.Path, fileURL(file.AbsolutePath), file.Language, strings.Join(file.Operations, "`, `"), file.OriginalSHA256)
		}
		out.WriteString("\n## Inspect detailed diffs\n\n")
		out.WriteString("After an applied migration, run the matching command below; it is deliberately left to Git so reviewers and agents see the repository's normal diff.\n\n")
		for _, file := range r.Files {
			fmt.Fprintf(&out, "- `%s`: `%s`\n", file.Path, file.GitDiffCommand)
		}
	}
	if len(r.ReviewItems) > 0 {
		out.WriteString("\n## Required review\n\n")
		for _, finding := range r.ReviewItems {
			fmt.Fprintf(&out, "- [`%s`](%s) lines %s — **%s**: %s\n", finding.Path, fileURL(finding.AbsolutePath), formatLines(finding.Lines), finding.RuleID, finding.Message)
		}
	}
	return out.String()
}

// YAML renders the same report as stable machine-readable YAML.
func (r Report) YAML() ([]byte, error) {
	return yaml.Marshal(r)
}

// WriteReports writes the Markdown and YAML representations to dir. It is
// opt-in so a dry-run never creates files unless the caller asks for a report.
func WriteReports(dir string, report Report) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "migration.md"), []byte(report.Markdown()), 0o644); err != nil {
		return err
	}
	raw, err := report.YAML()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "migration.yaml"), raw, 0o644)
}

func reportPath(file string, roots []string) (root, relative string) {
	for _, candidate := range roots {
		rel, err := filepath.Rel(candidate, file)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return candidate, filepath.ToSlash(rel)
		}
	}
	return filepath.Dir(file), filepath.Base(file)
}

func fileURL(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func formatLines(lines []int) string {
	values := make([]string, len(lines))
	for i, line := range lines {
		values[i] = fmt.Sprintf("%d", line)
	}
	return strings.Join(values, ", ")
}
