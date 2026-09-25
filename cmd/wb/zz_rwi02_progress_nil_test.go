package main

import (
	"bytes"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// TestQualityProgressNilReceiverIsANoOp drives the "progress == nil"
// early-return branches of start/report/finish: calling any method on a nil
// *qualityProgress must not panic and must write nothing.
func TestQualityProgressNilReceiverIsANoOp(t *testing.T) {
	t.Parallel()
	var progress *qualityProgress
	progress.start()
	progress.report(quality.Progress{State: quality.ProgressStarted})
	progress.finish()
}

// TestQualityProgressZeroTotalIsANoOp drives the "progress.total == 0"
// early-return branch alongside a non-nil receiver.
func TestQualityProgressZeroTotalIsANoOp(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newQualityProgress(&out, true, "verify", 0)
	progress.start()
	progress.report(quality.Progress{State: quality.ProgressStarted})
	progress.finish()
	if out.Len() != 0 {
		t.Fatalf("zero-total progress wrote %q, want nothing", out.String())
	}
}

// TestQualityProgressReportDefaultsEmptyModuleToDot drives the
// `if module == "" { module = "." }` substitution on the plain (non-sharded,
// non-repository-completed) report path.
func TestQualityProgressReportDefaultsEmptyModuleToDot(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newQualityProgress(&out, true, "verify", 1)
	progress.start()
	progress.report(quality.Progress{
		Repository: "acme/one", Command: "go vet", State: quality.ProgressStarted,
	})
	rendered := out.String()
	const want = "acme/one . — go vet: started"
	if !bytes.Contains([]byte(rendered), []byte(want)) {
		t.Fatalf("progress output missing %q: %q", want, rendered)
	}
}

// TestRemotePublishProgressNilReceiverIsANoOp drives the "progress == nil"
// early-return branch of every remotePublishProgress method.
func TestRemotePublishProgressNilReceiverIsANoOp(t *testing.T) {
	t.Parallel()
	var progress *remotePublishProgress
	progress.start(3)
	progress.repositoryComplete("acme/one", nil)
	progress.phase("scanning")
	progress.worktree(worktrees.ListProgress{Path: "/tmp/acme/one", Done: true})
	progress.finish("done")
	progress.fail(nil)
}
