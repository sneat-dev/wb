package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

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

type syncReportCommandDeps struct {
	validate func(context.Context, syncreport.Report) error
	publish  func(context.Context, string, string, syncreport.Report) (gitrepo.SyncReportPublishResult, error)
}

func defaultSyncReportCommandDeps() syncReportCommandDeps {
	return syncReportCommandDeps{
		validate: syncreport.ValidateInGitDB,
		publish: func(ctx context.Context, repository, root string, report syncreport.Report) (gitrepo.SyncReportPublishResult, error) {
			owner, name, _ := strings.Cut(repository, "/")
			provider := gitrepo.New(gitrepo.Options{
				ClonePath: filepath.Join(root, owner, name),
				CloneURL:  "git@github.com:" + repository + ".git",
			})
			return provider.PublishSyncReport(ctx, report, syncreport.ValidatePath)
		},
	}
}

func newSyncReportCmd() *cobra.Command {
	return newSyncReportCmdWithDeps(defaultSyncReportCommandDeps())
}

func newSyncReportCmdWithDeps(deps syncReportCommandDeps) *cobra.Command {
	command := &cobra.Command{
		Use:   "sync-report",
		Short: "Validate and publish per-repository sync analyses",
		Example: `# Check the agent-authored batch without changing a repository
wb sync-report validate ./sync-analysis

# Persist the batch in the user's workbench repository
wb sync-report publish ./sync-analysis --repo alice/workbench`,
	}
	setDiscoveryTerms(command, "sync report analysis findings repository ingitdb markdown persist publish web view")
	command.AddCommand(newSyncReportValidateCmd(deps), newSyncReportPublishCmd(deps))
	return command
}

func newSyncReportValidateCmd(deps syncReportCommandDeps) *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "validate <records-directory>",
		Short: "Validate agent-authored Markdown records with WB and InGitDB",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := syncreport.LoadDirectory(args[0])
			if err != nil {
				return err
			}
			if err := deps.validate(cmd.Context(), report); err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"report_id": report.ID, "records": len(report.Records), "valid": true})
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Validated sync report %s (%d repository records)\n", report.ID, len(report.Records))
			return err
		},
	}
	setDiscoveryTerms(command, "sync report validate check agent markdown ingitdb schema records")
	addJSONFormatFlags(command, &jsonOut)
	return command
}

func newSyncReportPublishCmd(deps syncReportCommandDeps) *cobra.Command {
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
			report, err := syncreport.LoadDirectory(args[0])
			if err != nil {
				return err
			}
			if err := deps.validate(cmd.Context(), report); err != nil {
				return err
			}
			result, err := deps.publish(cmd.Context(), repository, projectsRoot, report)
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
	setDiscoveryTerms(command, "sync report publish persist commit push workbench repository ingitdb URL web view")
	command.Flags().StringVar(&repository, "repo", "", "target user workbench repository (owner/name)")
	_ = command.MarkFlagRequired("repo")
	addJSONFormatFlags(command, &jsonOut)
	return command
}
