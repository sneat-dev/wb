package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// TestBranchArchiveTargetRequiresRepo drives the RunE `repository == ""`
// branch: a call with no --repo must refuse before touching the preflight.
func TestBranchArchiveTargetRequiresRepo(t *testing.T) {
	t.Parallel()
	command := newBranchArchiveTargetCmd()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs(nil)
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "--repo is required") {
		t.Fatalf("error = %v; want a --repo is required refusal", err)
	}
}

// TestBranchArchiveTargetPropagatesWriteFailure drives the RunE branch that
// returns the fmt.Fprintf error instead of swallowing it, reusing the
// package's shared failingWriter (dispatch_test.go).
func TestBranchArchiveTargetPropagatesWriteFailure(t *testing.T) {
	original := branchArchiveTargetPreflight
	t.Cleanup(func() { branchArchiveTargetPreflight = original })
	branchArchiveTargetPreflight = func(_ context.Context, repository string) (worktrees.RetiredArchivePlan, error) {
		return worktrees.RetiredArchivePlan{SourceRepository: repository, ArchiveRepository: "sneat-co/backstage-retired", Outcome: "ok"}, nil
	}
	command := newBranchArchiveTargetCmd()
	command.SetOut(failingWriter{err: errors.New("write refused")})
	command.SetArgs([]string{"--repo", "sneat-co/app"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "write refused") {
		t.Fatalf("error = %v; want the underlying write failure to propagate", err)
	}
}

// TestCampaignProgressReportsWaveNumber drives report()'s `event.Wave > 0`
// branch: the rendered line must name the wave.
func TestCampaignProgressReportsWaveNumber(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	p := newCampaignProgressWithHeartbeat(&out, true, "sync", 0)
	p.report(progress.Event{Wave: 3, State: progress.Running})
	if !strings.Contains(out.String(), "wave 3") {
		t.Fatalf("output = %q; want it to name wave 3", out.String())
	}
}

// TestCampaignProgressIgnoresReportsAfterFinish drives report()'s
// `p.finished` guard: an event reported after finish() must not overwrite
// the finished line.
func TestCampaignProgressIgnoresReportsAfterFinish(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	p := newCampaignProgressWithHeartbeat(&out, true, "sync", 0)
	p.report(progress.Event{Detail: "starting up"})
	p.finish("done")
	afterFinish := out.String()
	p.report(progress.Event{Detail: "late event, must be dropped"})
	if out.String() != afterFinish {
		t.Fatalf("output changed after finish: before=%q after=%q", afterFinish, out.String())
	}
	if strings.Contains(out.String(), "late event") {
		t.Fatalf("output = %q; a post-finish report must not render", out.String())
	}
}

// TestCIWaitProgressOperationReporterRendersCounts drives
// operationReporter's `event.Completed > 0 || event.Total > 0` branch.
func TestCIWaitProgressOperationReporterRendersCounts(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	p := newCIWaitProgressWithHeartbeat(&out, true, 0)
	reporter := p.operationReporter("ci audit")
	reporter(progress.Event{Completed: 2, Total: 5})
	if !strings.Contains(out.String(), "2/5") {
		t.Fatalf("output = %q; want it to render the 2/5 count", out.String())
	}
}

// TestShortRevisionTruncatesLongRevisions drives shortRevision's truncation
// branch and its pass-through branch.
func TestShortRevisionTruncatesLongRevisions(t *testing.T) {
	t.Parallel()
	if got := shortRevision("abcdef0123456789"); got != "abcdef012345" {
		t.Fatalf("shortRevision(long) = %q; want the first 12 characters", got)
	}
	if got := shortRevision("abc123"); got != "abc123" {
		t.Fatalf("shortRevision(short) = %q; want it unchanged", got)
	}
}
