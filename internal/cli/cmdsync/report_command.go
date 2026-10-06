package cmdsync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
	"github.com/sneat-dev/wb/internal/syncreport"
	"github.com/spf13/cobra"
)

type syncReportPublishOutput struct {
	ReportID   string   `json:"report_id"`
	Repository string   `json:"repository"`
	CommitSHA  string   `json:"commit_sha"`
	Records    int      `json:"records"`
	URL        string   `json:"url"`
	Paths      []string `json:"paths"`
}

type ReportOperations struct {
	Load     func(string) (syncreport.Report, error)
	Validate func(context.Context, syncreport.Report) error
	Publish  func(context.Context, string, string, syncreport.Report) (gitrepo.SyncReportPublishResult, error)
}

func NewReport(runtime shared.Runtime, deps ReportOperations, annotate func(*cobra.Command, string)) *cobra.Command {
	command := &cobra.Command{
		Use:   "sync-report",
		Short: "Validate and publish per-repository sync analyses",
		Example: `# Check the agent-authored batch without changing a repository
wb sync-report validate ./sync-analysis

# Persist the batch in the user's workbench repository
wb sync-report publish ./sync-analysis --repo alice/workbench`,
	}
	annotate(command, "sync report analysis findings repository ingitdb markdown persist publish web view")
	command.AddCommand(newSyncReportValidateCmd(deps, annotate), newSyncReportPublishCmd(runtime, deps, annotate))
	return command
}
func newSyncReportValidateCmd(deps ReportOperations, annotate func(*cobra.Command, string)) *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "validate <records-directory>",
		Short: "Validate agent-authored Markdown records with WB and InGitDB",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := deps.Load(args[0])
			if err != nil {
				return err
			}
			if err := deps.Validate(cmd.Context(), report); err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"report_id": report.ID, "records": len(report.Records), "valid": true})
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Validated sync report %s (%d repository records)\n", report.ID, len(report.Records))
			return err
		},
	}
	annotate(command, "sync report validate check agent markdown ingitdb schema records")
	shared.AddJSONFormatFlags(command, &jsonOut)
	return command
}
func newSyncReportPublishCmd(runtime shared.Runtime, deps ReportOperations, annotate func(*cobra.Command, string)) *cobra.Command {
	var repository string
	var jsonOut bool
	command := &cobra.Command{
		Use:   "publish <records-directory>",
		Short: "Validate, commit, and push sync analyses to a workbench repository",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := syncreport.ValidateRepository(repository); err != nil {
				return err
			}
			report, err := deps.Load(args[0])
			if err != nil {
				return err
			}
			if err := deps.Validate(cmd.Context(), report); err != nil {
				return err
			}
			result, err := deps.Publish(cmd.Context(), repository, runtime.Flags().ProjectsRoot, report)
			if err != nil {
				return err
			}
			values := url.Values{"repo": {repository}, "ref": {result.CommitSHA}, "report": {report.ID}}
			output := syncReportPublishOutput{
				ReportID: report.ID, Repository: repository, CommitSHA: result.CommitSHA,
				Records: len(report.Records), URL: "https://sneat.work/bench/app/sync-report?" + values.Encode(), Paths: result.Paths,
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(output)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Published sync report %s (%d repository records)\n%s\n", output.ReportID, output.Records, output.URL)
			return err
		},
	}
	annotate(command, "sync report publish persist commit push workbench repository ingitdb URL web view")
	command.Flags().StringVar(&repository, "repo", "", "target user workbench repository (owner/name)")
	_ = command.MarkFlagRequired("repo")
	shared.AddJSONFormatFlags(command, &jsonOut)
	return command
}
