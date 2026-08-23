package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/remotestate"
)

func newRemotePublishCmd() *cobra.Command {
	var dryRun, jsonOut bool
	var parallel int
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Scan this machine's fleet and publish the snapshot to the remote store",
		Long: `Scans every clone under --projects-root (honouring --filter), lists live
task worktrees, and publishes one snapshot keyed <login>/<machine>.
--dry-run prints the snapshot and writes nothing, locally or remotely.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRemotePublish(defaultRemoteDeps(), projectsRoot, filterFlag, parallel, dryRun, jsonOut, os.Stdout)
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "print the snapshot; publish nothing")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the publish report as JSON")
	cmd.Flags().IntVar(&parallel, "parallel", 8, "max concurrent repository scans")
	return cmd
}

type remotePublishReport struct {
	Key                 string `json:"key"`
	RepositoriesScanned int    `json:"repositories_scanned"`
	Attention           int    `json:"attention"`
	Worktrees           int    `json:"worktrees"`
	Location            string `json:"location,omitempty"`
}

func runRemotePublish(deps remoteDeps, projectsRoot, filter string, parallel int, dryRun, jsonOut bool, out io.Writer) error {
	cfg, provider, err := loadRemote(deps, projectsRoot)
	if err != nil {
		return err
	}
	login, err := deps.login()
	if err != nil || login == "" {
		return &exitError{code: exitUsage, message: fmt.Sprintf("wb remote needs the GitHub login to key this machine's entry (gh auth status): %v", err)}
	}
	identity := remotestate.Snapshot{Login: login, Machine: cfg.Machine, PublishedAt: deps.now(), WBVersion: collectVersion().Version}
	snapshot, err := collectSnapshot(projectsRoot, filter, parallel, identity, cfg.Publish.Unpushed)
	if err != nil {
		return err
	}
	report := remotePublishReport{Key: snapshot.Key(), RepositoriesScanned: snapshot.RepositoriesScanned, Attention: len(snapshot.Repositories), Worktrees: len(snapshot.Worktrees)}
	if dryRun {
		if jsonOut {
			return json.NewEncoder(out).Encode(snapshot)
		}
		data, err := remotestate.Encode(snapshot)
		if err != nil {
			return err
		}
		_, err = out.Write(data)
		return err
	}
	result, err := provider.Publish(context.Background(), snapshot)
	if err != nil {
		return &exitError{code: exitFindings, message: "publish: " + err.Error()}
	}
	report.Location = result.Location
	if jsonOut {
		return json.NewEncoder(out).Encode(report)
	}
	_, err = fmt.Fprintf(out, "published %s: %d repositories scanned, %d need attention, %d worktrees → %s\n",
		report.Key, report.RepositoriesScanned, report.Attention, report.Worktrees, report.Location)
	return err
}
