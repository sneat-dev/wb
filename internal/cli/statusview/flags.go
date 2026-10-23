package statusview

import "github.com/spf13/cobra"

// Options contains only the status flags shared with fleet overview.
type Options struct {
	Parallel                        int
	Match, Regex, Format, ReportDir string
}

func BindFlags(command *cobra.Command, options *Options, details, all *bool, fleetFilters bool) {
	if fleetFilters {
		command.Flags().StringVar(&options.Match, "match", "", "fleet-only glob matched against org/repo, e.g. sneat-co/*")
		command.Flags().StringVar(&options.Regex, "regex", "", "fleet-only regular expression matched against org/repo")
	}
	command.Flags().IntVar(&options.Parallel, "parallel", 4, "maximum repositories to inspect concurrently")
	command.Flags().StringVar(&options.Format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&options.ReportDir, "report-dir", "", "write status.md and status.yaml to this directory")
	command.Flags().BoolVar(details, "details", false, "include individual changed, untracked, conflict, stash, and unpushed entries in Markdown")
	if all != nil {
		command.Flags().BoolVar(all, "all", false, "report clean repositories too; a single repository-path always reports its own status")
	}
}
