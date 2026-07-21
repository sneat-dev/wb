package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/gitops"
)

// Status is fleet-first: without a path it examines every local repository
// below --projects-root. Supplying a path intentionally narrows it to one
// checkout, so a separate --fleet flag would only add ambiguity.
func newStatusCmd() *cobra.Command {
	options := qualityOptions{parallel: 4}
	var details bool
	command := &cobra.Command{
		Use:   "status [repository-path]",
		Short: "Report local Git state across all local repositories by default",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
				options.fleet = false
			} else {
				options.fleet = true
			}
			targets, err := qualityTargets(path, projectsRoot, filterFlag, options)
			if err != nil {
				return err
			}
			report := statusIndex{SchemaVersion: 1, Repositories: runStatusTargets(targets, options.parallel)}
			if err := writeStatusOutput(report, options.format, options.reportDir, details); err != nil {
				return err
			}
			if statusFailed(report) {
				return &exitError{code: 1}
			}
			return nil
		},
	}
	command.Flags().StringVar(&options.match, "match", "", "glob matched against org/repo, e.g. sneat-co/*")
	command.Flags().StringVar(&options.regex, "regex", "", "regular expression matched against org/repo")
	command.Flags().IntVar(&options.parallel, "parallel", 4, "maximum repositories to inspect concurrently")
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "write status.md and status.yaml to this directory")
	command.Flags().BoolVar(&details, "details", false, "include individual changed, untracked, conflict, stash, and unpushed entries in Markdown")
	return command
}

type statusIndex struct {
	SchemaVersion int                    `yaml:"schema_version" json:"schema_version"`
	Repositories  []repositoryStatusInfo `yaml:"repositories" json:"repositories"`
}

type repositoryStatusInfo struct {
	Repository string   `yaml:"repository" json:"repository"`
	Path       string   `yaml:"path" json:"path"`
	Status     string   `yaml:"status" json:"status"`
	Summary    string   `yaml:"summary,omitempty" json:"summary,omitempty"`
	Modified   []string `yaml:"modified,omitempty" json:"modified,omitempty"`
	Untracked  []string `yaml:"untracked,omitempty" json:"untracked,omitempty"`
	Conflicted []string `yaml:"conflicted,omitempty" json:"conflicted,omitempty"`
	Unpushed   []string `yaml:"unpushed,omitempty" json:"unpushed,omitempty"`
	Stashed    []string `yaml:"stashed,omitempty" json:"stashed,omitempty"`
	Error      string   `yaml:"error,omitempty" json:"error,omitempty"`
}

func runStatusTargets(targets []qualityTarget, parallel int) []repositoryStatusInfo {
	reports := make([]repositoryStatusInfo, len(targets))
	runTargets(len(targets), parallel, func(index int) {
		target := targets[index]
		state, err := gitops.Status(target.path)
		if err != nil {
			reports[index] = repositoryStatusInfo{Repository: target.repository, Path: target.path, Status: "error", Error: err.Error()}
			return
		}
		status := "clean"
		if state.Dirty() {
			status = "attention"
		}
		reports[index] = repositoryStatusInfo{
			Repository: target.repository,
			Path:       target.path,
			Status:     status,
			Summary:    state.Summary(),
			Modified:   state.Modified,
			Untracked:  state.Untracked,
			Conflicted: state.Conflicted,
			Unpushed:   state.Unpushed,
			Stashed:    state.Stashed,
		}
	})
	return reports
}

func statusFailed(report statusIndex) bool {
	for _, repository := range report.Repositories {
		if repository.Status == "error" {
			return true
		}
	}
	return false
}

func writeStatusOutput(report statusIndex, format, reportDir string, details bool) error {
	if reportDir != "" {
		if err := os.MkdirAll(reportDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(reportDir, "status.md"), []byte(statusMarkdown(report, details)), 0o644); err != nil {
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
		_, err := fmt.Print(statusMarkdown(report, details))
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

func statusMarkdown(report statusIndex, details bool) string {
	var out strings.Builder
	out.WriteString("# WB local repository status\n\n")
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
	return out.String()
}

func writeStatusDetails(out *strings.Builder, repository repositoryStatusInfo) {
	for _, group := range []struct {
		name  string
		items []string
	}{
		{"Modified", repository.Modified},
		{"Untracked", repository.Untracked},
		{"Conflicted", repository.Conflicted},
		{"Unpushed", repository.Unpushed},
		{"Stashed", repository.Stashed},
	} {
		if len(group.items) == 0 {
			continue
		}
		fmt.Fprintf(out, "\n%s — %s:\n", repository.Repository, group.name)
		for _, item := range group.items {
			fmt.Fprintf(out, "- `%s`\n", item)
		}
	}
}
