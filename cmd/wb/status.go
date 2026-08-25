package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/gitops"
)

// newStatusCmd keeps the historical entry point. Prefer the explicit nouns:
// wb fleet status / wb fleet stats / wb fleet, and wb repo status.
func newStatusCmd() *cobra.Command {
	options := qualityOptions{parallel: 4}
	var details bool
	var all bool
	command := &cobra.Command{
		Use:   "status [repository-path]",
		Short: "Report local Git state (fleet worklist, or one repository when a path is given)",
		Long: `Report local Git attention for the fleet or one repository.

Prefer the explicit commands:
  wb fleet status   fleet attention worklist
  wb fleet stats    inventory and attention counts
  wb fleet          overview (stats + attention)
  wb repo status    one repository

Without a path this command matches wb fleet status. With a path it matches
wb repo status. It reads local Git state only and never fetches or contacts
GitHub. Fleet scans show a live completion counter on stderr when attached to
a terminal; --non-interactive disables it.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			fleet := true
			if len(args) == 1 {
				path = args[0]
				fleet = false
			}
			return runRepositoryStatus(repositoryStatusRequest{
				path:      path,
				fleet:     fleet,
				all:       all,
				details:   details,
				options:   options,
				filter:    filterFlag,
				projects:  projectsRoot,
				titleKind: statusTitleAuto,
				progress:  cmd.ErrOrStderr(),
			})
		},
	}
	bindRepositoryStatusFlags(command, &options, &details, &all, true)
	return command
}

type statusTitleKind int

const (
	statusTitleAuto statusTitleKind = iota
	statusTitleFleet
	statusTitleRepo
)

type repositoryStatusRequest struct {
	path      string
	fleet     bool
	all       bool
	details   bool
	options   qualityOptions
	filter    string
	projects  string
	titleKind statusTitleKind
	progress  io.Writer
}

func bindRepositoryStatusFlags(command *cobra.Command, options *qualityOptions, details, all *bool, fleetFilters bool) {
	if fleetFilters {
		command.Flags().StringVar(&options.match, "match", "", "fleet-only glob matched against org/repo, e.g. sneat-co/*")
		command.Flags().StringVar(&options.regex, "regex", "", "fleet-only regular expression matched against org/repo")
	}
	command.Flags().IntVar(&options.parallel, "parallel", 4, "maximum repositories to inspect concurrently")
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "write status.md and status.yaml to this directory")
	command.Flags().BoolVar(details, "details", false, "include individual changed, untracked, conflict, stash, and unpushed entries in Markdown")
	if all != nil {
		command.Flags().BoolVar(all, "all", false, "report clean repositories too; a single repository-path always reports its own status")
	}
}

func runRepositoryStatus(request repositoryStatusRequest) error {
	request.options.fleet = request.fleet
	targets, err := qualityTargets(request.path, request.projects, request.filter, request.options)
	if err != nil {
		return err
	}
	progress := newStatusProgress(
		request.progress,
		console.Interactive(request.progress, nonInteractive),
	)
	progress.start(len(targets))
	repositories := runStatusTargetsWithProgress(targets, request.options.parallel, progress.complete)
	progress.finish()
	report := statusIndex{SchemaVersion: 1, Repositories: repositories}
	// A fleet report is a worklist, so clean checkouts are noise unless they
	// were asked for. One named repository is a direct question about that
	// checkout, where "clean" is the answer, not nothing.
	if request.fleet && !request.all {
		report = hideCleanRepositories(report)
	}
	title := statusMarkdownTitle(request.titleKind, request.fleet)
	if err := writeStatusOutput(report, request.options.format, request.options.reportDir, request.details, title); err != nil {
		return err
	}
	if statusFailed(report) {
		return &exitError{
			code:    exitFindings,
			message: "one or more repositories could not be inspected; see the `error` field of each `error` row above",
		}
	}
	return nil
}

func statusMarkdownTitle(kind statusTitleKind, fleet bool) string {
	switch kind {
	case statusTitleFleet:
		return "# WB fleet status\n\n"
	case statusTitleRepo:
		return "# WB repository status\n\n"
	default:
		if fleet {
			return "# WB local repository status\n\n"
		}
		return "# WB repository status\n\n"
	}
}

type statusIndex struct {
	SchemaVersion int `yaml:"schema_version" json:"schema_version"`
	// HiddenClean counts the clean repositories left out of Repositories, so
	// every consumer of this index can tell a filtered report from a fleet
	// where nothing was inspected.
	HiddenClean  int                    `yaml:"hidden_clean,omitempty" json:"hidden_clean,omitempty"`
	Repositories []repositoryStatusInfo `yaml:"repositories" json:"repositories"`
}

type repositoryStatusInfo struct {
	Repository       string                  `yaml:"repository" json:"repository"`
	Path             string                  `yaml:"path" json:"path"`
	Status           string                  `yaml:"status" json:"status"`
	Summary          string                  `yaml:"summary,omitempty" json:"summary,omitempty"`
	Modified         []string                `yaml:"modified,omitempty" json:"modified,omitempty"`
	Untracked        []string                `yaml:"untracked,omitempty" json:"untracked,omitempty"`
	Conflicted       []string                `yaml:"conflicted,omitempty" json:"conflicted,omitempty"`
	Unpushed         []string                `yaml:"unpushed,omitempty" json:"unpushed,omitempty"`
	UnpushedBranches []gitops.UnpushedBranch `yaml:"unpushed_branches,omitempty" json:"unpushed_branches,omitempty"`
	Stashed          []string                `yaml:"stashed,omitempty" json:"stashed,omitempty"`
	Error            string                  `yaml:"error,omitempty" json:"error,omitempty"`
}

func runStatusTargets(targets []qualityTarget, parallel int) []repositoryStatusInfo {
	return runStatusTargetsWithProgress(targets, parallel, nil)
}

func runStatusTargetsWithProgress(
	targets []qualityTarget,
	parallel int,
	complete func(qualityTarget, repositoryStatusInfo),
) []repositoryStatusInfo {
	reports := make([]repositoryStatusInfo, len(targets))
	runTargets(len(targets), parallel, func(index int) {
		target := targets[index]
		state, err := gitops.Status(target.path)
		if err != nil {
			reports[index] = repositoryStatusInfo{Repository: target.repository, Path: target.path, Status: "error", Error: err.Error()}
			if complete != nil {
				complete(target, reports[index])
			}
			return
		}
		status := "clean"
		if state.Dirty() {
			status = "attention"
		}
		reports[index] = repositoryStatusInfo{
			Repository:       target.repository,
			Path:             target.path,
			Status:           status,
			Summary:          state.Summary(),
			Modified:         state.Modified,
			Untracked:        state.Untracked,
			Conflicted:       state.Conflicted,
			Unpushed:         state.Unpushed,
			UnpushedBranches: state.UnpushedBranches,
			Stashed:          state.Stashed,
		}
		if complete != nil {
			complete(target, reports[index])
		}
	})
	return reports
}

// hideCleanRepositories drops the clean rows and records how many were
// dropped. Errors and attention rows survive, so the exit code and the
// worklist stay the same whether or not the report was filtered.
func hideCleanRepositories(report statusIndex) statusIndex {
	kept := make([]repositoryStatusInfo, 0, len(report.Repositories))
	for _, repository := range report.Repositories {
		if repository.Status == "clean" {
			continue
		}
		kept = append(kept, repository)
	}
	report.HiddenClean = len(report.Repositories) - len(kept)
	report.Repositories = kept
	return report
}

func statusFailed(report statusIndex) bool {
	for _, repository := range report.Repositories {
		if repository.Status == "error" {
			return true
		}
	}
	return false
}

func writeStatusOutput(report statusIndex, format, reportDir string, details bool, title string) error {
	if title == "" {
		title = "# WB local repository status\n\n"
	}
	if reportDir != "" {
		if err := os.MkdirAll(reportDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(reportDir, "status.md"), []byte(statusMarkdown(report, details, title)), 0o644); err != nil {
			return err
		}
		raw, err := yaml.Marshal(report)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(reportDir, "status.yaml"), raw, 0o644); err != nil {
			return err
		}
	}
	switch format {
	case "markdown":
		_, err := fmt.Print(statusMarkdown(report, details, title))
		return err
	case "yaml":
		raw, err := yaml.Marshal(report)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(raw)
		return err
	case "json":
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
}

func statusMarkdown(report statusIndex, details bool, title string) string {
	var out strings.Builder
	out.WriteString(title)
	if len(report.Repositories) == 0 && report.HiddenClean > 0 {
		if report.HiddenClean == 1 {
			out.WriteString("The inspected repository is clean.\n")
		} else {
			fmt.Fprintf(&out, "All %d inspected repositories are clean.\n", report.HiddenClean)
		}
		return out.String()
	}
	out.WriteString("| Repository | Status | Summary |\n|---|---|---|\n")
	for _, repository := range report.Repositories {
		summary := repository.Summary
		if repository.Error != "" {
			summary = repository.Error
		}
		if summary == "" {
			summary = "—"
		}
		fmt.Fprintf(&out, "| `%s` | `%s` | %s |\n", repository.Repository, repository.Status, summary)
		if details {
			writeStatusDetails(&out, repository)
		}
	}
	if report.HiddenClean > 0 {
		fmt.Fprintf(&out, "\n%s\n", statusHiddenNote(report.HiddenClean))
	}
	return out.String()
}

// statusHiddenNote keeps the default filter honest: a report that left rows
// out says so, and says which flag brings them back.
func statusHiddenNote(count int) string {
	if count == 1 {
		return "_1 clean repository hidden; pass `--all` to include it._"
	}
	return fmt.Sprintf("_%d clean repositories hidden; pass `--all` to include them._", count)
}

func writeStatusDetails(out *strings.Builder, repository repositoryStatusInfo) {
	for _, group := range []struct {
		name  string
		items []string
	}{
		{"Modified", repository.Modified},
		{"Untracked", repository.Untracked},
		{"Conflicted", repository.Conflicted},
	} {
		writeStatusDetailGroup(out, repository.Repository, group.name, group.items)
	}
	if len(repository.UnpushedBranches) == 0 {
		writeStatusDetailGroup(out, repository.Repository, "Unpushed", repository.Unpushed)
	} else {
		fmt.Fprintf(out, "\n%s — Unpushed:\n", repository.Repository)
		for _, branch := range repository.UnpushedBranches {
			if branch.Worktree == "" {
				fmt.Fprintf(out, "- Branch `%s`:\n", branch.Branch)
			} else {
				fmt.Fprintf(out, "- Branch `%s` in worktree `%s`:\n", branch.Branch, branch.Worktree)
			}
			for _, commit := range branch.Commits {
				fmt.Fprintf(out, "  - `%s`\n", commit)
			}
		}
	}
	writeStatusDetailGroup(out, repository.Repository, "Stashed", repository.Stashed)
}

func writeStatusDetailGroup(out *strings.Builder, repository, name string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(out, "\n%s — %s:\n", repository, name)
	for _, item := range items {
		fmt.Fprintf(out, "- `%s`\n", item)
	}
}
