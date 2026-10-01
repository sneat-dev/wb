package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/remotestate"
)

func newRemotePublishCmd(inv *invocation) *cobra.Command {
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
			return runRemotePublishWithProgress(defaultRemoteDeps(), inv.projectsRoot, inv.filterFlag, parallel, dryRun, jsonOut, os.Stdout, cmd.ErrOrStderr(), inv)
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "print the snapshot; publish nothing")
	addJSONFormatFlags(cmd, &jsonOut)
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

func runRemotePublishWithProgress(deps remoteDeps, projectsRoot, filter string, parallel int, dryRun, jsonOut bool, out, progressOut io.Writer, inv *invocation) error {
	cfg, provider, err := loadRemote(deps, projectsRoot)
	if err != nil {
		return err
	}
	login, err := deps.login()
	if err != nil || login == "" {
		return &exitError{code: exitUsage, message: fmt.Sprintf("wb remote needs the GitHub login to key this machine's entry (gh auth status): %v", err)}
	}
	identity := publishIdentity(cfg, login, deps.now())
	progress := newRemotePublishProgress(progressOut, console.Interactive(progressOut, inv.nonInteractive))
	snapshot, err := collectSnapshot(context.Background(), projectsRoot, filter, parallel, identity, cfg.Publish.Unpushed, progress)
	if err != nil {
		progress.fail(err)
		return err
	}
	report := remotePublishReport{Key: snapshot.Key(), RepositoriesScanned: snapshot.RepositoriesScanned, Attention: len(snapshot.Repositories), Worktrees: len(snapshot.Worktrees)}
	if dryRun {
		progress.finish("snapshot prepared")
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
	progress.phase("publishing snapshot")
	result, diagnostic, err := remotestate.PublishWithFallback(context.Background(), provider, snapshot)
	if err != nil {
		progress.fail(err)
		return &exitError{code: exitFindings, message: "publish: " + err.Error()}
	}
	report.Location = result.Location
	if diagnostic != nil && progressOut != nil {
		_, _ = fmt.Fprintf(progressOut, "wb: %v\n", diagnostic)
	}
	progress.finish(fmt.Sprintf("published %d repositories and %d worktrees", report.RepositoriesScanned, report.Worktrees))
	if jsonOut {
		return json.NewEncoder(out).Encode(report)
	}
	_, err = fmt.Fprintf(out, "published %s: %d repositories scanned, %d need attention, %d worktrees → %s\n",
		report.Key, report.RepositoriesScanned, report.Attention, report.Worktrees, report.Location)
	return err
}

// publishIdentity is the part of a snapshot that is this machine's own rather
// than the scan's: who and where it is, when it publishes, which wb, and the
// hardware facts of its machine entry (cockpit-views#req:remote-snapshot-
// agents-and-metrics). `wb remote publish` and the daemon's periodic publish
// both start from it.
func publishIdentity(cfg remotestate.Config, login string, now time.Time) remotestate.Snapshot {
	hardware := cockpitfleet.LocalHardware()
	return remotestate.Snapshot{
		Login: login, Machine: cfg.Machine, PublishedAt: now, WBVersion: collectVersion().Version, RemoteStore: cfg.StoreID(),
		OS: hardware.OS, Arch: hardware.Arch, CPUCount: hardware.CPUCount, BootTime: hardware.BootTime,
	}
}
