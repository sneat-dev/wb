package remotepublish

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
)

type Report struct {
	Key                 string `json:"key"`
	RepositoriesScanned int    `json:"repositories_scanned"`
	Attention           int    `json:"attention"`
	Worktrees           int    `json:"worktrees"`
	Location            string `json:"location,omitempty"`
}

func (service *Service) Publish(request Request, progress Progress, notes io.Writer) (Result, error) {
	deps := service.deps
	projectsRoot, filter, parallel, dryRun := request.ProjectsRoot, request.Filter, request.Parallel, request.DryRun
	cfg, provider, err := Load(deps.ConfigPath, projectsRoot, deps.Open, deps.ExitError)
	if err != nil {
		return Result{}, err
	}
	login, err := deps.Login()
	if err != nil || login == "" {
		return Result{}, deps.ExitError(2, fmt.Sprintf("wb remote needs the GitHub login to key this machine's entry (gh auth status): %v", err))
	}
	identity := service.publishIdentity(cfg, login, deps.Now())
	snapshot, err := service.collectSnapshot(context.Background(), projectsRoot, filter, parallel, identity, cfg.Publish.Unpushed, progress, nil)
	if err != nil {
		progress.fail(err)
		return Result{}, err
	}
	report := Report{Key: snapshot.Key(), RepositoriesScanned: snapshot.RepositoriesScanned, Attention: len(snapshot.Repositories), Worktrees: len(snapshot.Worktrees)}
	if dryRun {
		progress.finish("snapshot prepared")
		return Result{DryRun: true, Snapshot: snapshot, Report: report}, nil
	}
	// What a publish must say does not depend on whether it shows progress: a
	// caller with no progress writer is told on stderr.
	marker := ""
	if notes != nil {
		marker = noteHardware(deps.ConfigPath, notes)
	}
	progress.phase("publishing snapshot")
	result, diagnostic, err := remotestate.PublishWithFallback(context.Background(), provider, snapshot, deps.Now())
	if err != nil {
		progress.fail(err)
		return Result{}, deps.ExitError(1, "publish: "+err.Error())
	}
	report.Location = result.Location
	recordHardwareNoted(marker)
	if diagnostic != nil && notes != nil {
		_, _ = fmt.Fprintf(notes, "wb: %v\n", diagnostic)
	}
	progress.finish(fmt.Sprintf("published %d repositories and %d worktrees", report.RepositoriesScanned, report.Worktrees))
	return Result{Snapshot: snapshot, Report: report}, nil
}

// publishIdentity is the part of a snapshot that is this machine's own rather
// than the scan's: who and where it is, when it publishes, which wb, and the
// hardware facts of its machine entry (cockpit-views#req:remote-snapshot-
// agents-and-metrics). `wb remote publish` and the daemon's periodic publish
// both start from it.
func (service *Service) publishIdentity(cfg remotestate.Config, login string, now time.Time) remotestate.Snapshot {
	hardware := service.deps.Hardware()
	return remotestate.Snapshot{
		Login: login, Machine: cfg.Machine, PublishedAt: now, WBVersion: service.deps.Version(), RemoteStore: cfg.StoreID(),
		OS: hardware.OS, Arch: hardware.Arch, CPUCount: hardware.CPUCount, BootTime: hardware.BootTime,
	}.CleanHardware()
}

// hardwareNote is the one line the first real publish prints after the machine's
// hardware facts joined the snapshot: on stderr, by hand or from `wb sync`,
// with or without a progress writer, and in the daemon's log when the first
// publish that sends them is the periodic one.
const hardwareNote = "wb: this publish also includes this machine's os, arch, cpu_count and boot_time (new in this version); agents and metrics are never sent by hand\n"

// noteHardware prints hardwareNote unless it was printed on an earlier
// successful publish, and returns the marker to write once this publish has
// succeeded ("" when there is nothing to record), so a publish that fails does
// not use the note up.
func noteHardware(configPath string, out io.Writer) (marker string) {
	if marker = hardwareNoteMarker(configPath); marker != "" {
		_, _ = io.WriteString(out, hardwareNote)
	}
	return marker
}

// hardwareNoteMarker is the file that records that the hardware note was said,
// or "" when it was (or when there is no configuration to keep it beside).
func hardwareNoteMarker(configPath string) string {
	if configPath == "" {
		return ""
	}
	marker := filepath.Join(filepath.Dir(configPath), ".wb-remote-publish-hardware-noted")
	if _, err := os.Stat(marker); err == nil {
		return ""
	}
	return marker
}

// periodicHardwareNote is hardwareNote as the daemon's log says it, when the
// first publish that sends the hardware facts is the periodic one.
const periodicHardwareNote = "remote publish: the snapshot now also carries this machine's os, arch, cpu_count and boot_time (new in this version)"

// notePeriodicHardware says periodicHardwareNote in the daemon's log after a
// periodic publish that reached the store, unless the note was already said by
// an earlier publish, by hand or periodic.
func notePeriodicHardware(configPath string, logf func(string, ...any)) {
	marker := hardwareNoteMarker(configPath)
	if marker == "" {
		return
	}
	if logf != nil {
		logf("%s", periodicHardwareNote)
	}
	recordHardwareNoted(marker)
}

// recordHardwareNoted writes the marker noteHardware returned. A marker that
// cannot be written costs only a repeat of the line.
func recordHardwareNoted(marker string) {
	if marker != "" {
		_ = os.WriteFile(marker, nil, 0o600)
	}
}
