package wbupdate

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/strongo/cli-helpers/selfupdate"
)

type Output struct {
	Out, Err io.Writer
	JSON     bool
}
type ChildRequest struct {
	Path        string
	Args        []string
	MergeOutput bool
}
type ChildResult struct{ Combined, Stdout, Stderr []byte }
type Service struct {
	RunChild       func(context.Context, ChildRequest) (ChildResult, error)
	HandoffTimeout func() time.Duration
}

func (service Service) AfterUpdate(ctx context.Context, update selfupdate.AfterUpdate, output Output) error {
	service.RestartDaemon(output, ctx, update)
	return service.SyncSkills(output, ctx, update)
}
func (service Service) RestartDaemon(output Output, parent context.Context, update selfupdate.AfterUpdate) {
	if update.Outcome.Action == selfupdate.ActionAlreadyCurrent || update.Outcome.PostSwapWarning != nil {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, service.HandoffTimeout())
	defer cancel()
	result, err := service.RunChild(ctx, ChildRequest{Path: update.Executable.Path, Args: []string{"daemon", "restart", "--if-running", "--format", "json"}, MergeOutput: true})
	if err != nil {
		if ctx.Err() != nil {
			_, _ = fmt.Fprintf(output.Err, "warning: verified WB update completed, but the daemon handoff did not finish within %s: %v; check `wb daemon status` and run `wb daemon restart --if-running` if it is not back\n", service.HandoffTimeout(), err)
		} else {
			_, _ = fmt.Fprintf(output.Err, "warning: verified WB update completed, but the daemon handoff reported a failure; check `wb daemon status` and run `wb daemon restart --if-running` if needed: %v\n", err)
		}
		_, _ = output.Err.Write(result.Combined)
		return
	}
	// The self-update command owns stdout (especially in JSON mode), so the
	// child lifecycle receipt is diagnostic-only.
	_, _ = output.Err.Write(result.Combined)
}

func writeVerifiedVersion(output Output, previous, installed string) {
	out := output.Out
	if output.JSON {
		out = output.Err
	}
	_, _ = fmt.Fprintf(out, "Verified installed wb version: %s (was %s).\n", installed, previous)
}

// syncSkillsAfterSelfUpdate runs `skills sync` against the exact installed
// executable identity resolved by the shared self-update provider.
//
// It re-execs the installed binary rather than calling the shared skillsync adapter in
// this process: when self-update actually swapped the executable, this
// process is still running the OLD build in memory (replacing the file on
// disk does not reload an already-running process), so only a fresh child
// process sees the newly embedded skills. It resolves the configured binary
// name on PATH first: Homebrew's stable launcher survives a cask upgrade,
// whereas os.Executable can still name the deleted old Caskroom target.
//
// A failure here is never fatal to self-update: the update itself already
// succeeded (or there was nothing to do), so a sync that cannot run --
// offline, a permissions issue, no harness present yet -- is reported as a
// warning on stderr rather than turned into a self-update failure.
func (service Service) SyncSkills(output Output, parent context.Context, update selfupdate.AfterUpdate) error {
	if update.Outcome.Action == selfupdate.ActionAlreadyCurrent {
		return nil
	}
	if update.Outcome.PostSwapWarning != nil {
		return fmt.Errorf("skip skills sync because the installed WB version was not verified: %w", update.Outcome.PostSwapWarning)
	}
	installedVersion := update.Outcome.Target
	if installedVersion == "" {
		installedVersion = update.Outcome.Result.Latest
	}
	if installedVersion != "" {
		writeVerifiedVersion(output, update.Outcome.Result.Current, installedVersion)
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	result, err := service.RunChild(ctx, ChildRequest{Path: update.Executable.Path, Args: []string{"skills", "sync"}})
	if err != nil {
		return fmt.Errorf("skills sync failed (%v); run `wb skills sync` to install/update WB's Agent Skills manually: %s", err, string(result.Stderr))
	}
	// cobracmd deliberately keeps stdout to one JSON document. A successful
	// nested skills sync is informational, so send it to stderr in JSON mode
	// rather than corrupting the caller's machine-readable update outcome.
	if output.JSON {
		_, _ = output.Err.Write(result.Stdout)
		return nil
	}
	_, _ = output.Out.Write(result.Stdout)
	return nil
}
