package main

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/streamsync"
)

// AC: cov-rwi-03 unit03 seam list, cmd/wb/stream_sync.go printStreamSync.
// Every text-mode line it prints returns immediately on a write error
// instead of continuing to the next line. Each case below shapes the
// streamsync.Result so exactly one optional block is present, and lets a
// failAfterWriter succeed through every earlier call before failing on the
// one this case targets -- isolating each error-return branch in turn.
func TestPrintStreamSyncPropagatesEachLineWriteFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		result        streamsync.Result
		allowedWrites int
	}{
		{
			name:          "the base repository/branch line",
			result:        streamsync.Result{Repository: "sneat-dev/wb"},
			allowedWrites: 0,
		},
		{
			name:          "the remote-advanced line",
			result:        streamsync.Result{Repository: "sneat-dev/wb", RemoteAdvanced: true},
			allowedWrites: 1,
		},
		{
			name: "an agent-rebase line",
			result: streamsync.Result{Repository: "sneat-dev/wb", AgentRebases: []streamsync.RebaseResult{
				{Branch: "agent/x", Agent: "claude", Rebased: true},
			}},
			allowedWrites: 1,
		},
		{
			name: "a dependency-bump line",
			result: streamsync.Result{Repository: "sneat-dev/wb", Bumps: []streamsync.BumpResult{
				{Action: "bump", Library: streamsync.Library{Name: "foo", Target: "v2.0.0"}},
			}},
			allowedWrites: 1,
		},
		{
			name:          "the unpushed-commits line",
			result:        streamsync.Result{Repository: "sneat-dev/wb"},
			allowedWrites: 1,
		},
		{
			name:          "the push-skipped line",
			result:        streamsync.Result{Repository: "sneat-dev/wb", PushSkipped: "remote already current"},
			allowedWrites: 2,
		},
		{
			name: "the pushed line",
			result: streamsync.Result{Repository: "sneat-dev/wb", Push: &streamsync.PushDecision{
				SHA: "deadbeef", Trigger: streamsync.PushTrigger("landing"), Reason: "batch passed",
			}},
			allowedWrites: 2,
		},
		{
			name:          "an error line",
			result:        streamsync.Result{Repository: "sneat-dev/wb", Errors: []string{"lint failed"}},
			allowedWrites: 2,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			command := &cobra.Command{}
			w := &failAfterWriter{allowedWrites: testCase.allowedWrites}
			command.SetOut(w)
			err := printStreamSync(command, "text", []streamsync.Result{testCase.result})
			if err == nil {
				t.Fatalf("expected the write failure on %s to propagate", testCase.name)
			}
		})
	}
}

// AC: cov-rwi-03 unit03 seam list, cmd/wb/stream_sync.go printBatch. Same
// isolation technique as printStreamSync above, one BatchResult field at a
// time, for each of its eight Fprintf call sites.
func TestPrintBatchPropagatesEachLineWriteFailure(t *testing.T) {
	t.Parallel()
	element := streamsync.Element{Name: "commit-a"}
	cases := []struct {
		name          string
		batch         streamsync.BatchResult
		allowedWrites int
	}{
		{name: "the base verdict line", batch: streamsync.BatchResult{Passed: true}, allowedWrites: 0},
		{
			name:          "the culprit line",
			batch:         streamsync.BatchResult{Culprit: &element, FailingCheck: "go test"},
			allowedWrites: 1,
		},
		{
			name:          "the proven-good line",
			batch:         streamsync.BatchResult{Culprit: &element, ProvenGood: []streamsync.Element{element}},
			allowedWrites: 2,
		},
		{
			name:          "the interaction-failure line",
			batch:         streamsync.BatchResult{InteractionFailure: true},
			allowedWrites: 1,
		},
		{
			name:          "a skipped-mechanism line",
			batch:         streamsync.BatchResult{Skipped: []string{"-race"}},
			allowedWrites: 1,
		},
		{
			name:          "an unguarded-mechanism line",
			batch:         streamsync.BatchResult{Unguarded: []string{"lint"}},
			allowedWrites: 1,
		},
		{
			name:          "an unverified-mechanism line",
			batch:         streamsync.BatchResult{Unverified: []string{"e2e"}},
			allowedWrites: 1,
		},
		{
			name:          "the unexamined-elements line",
			batch:         streamsync.BatchResult{UnexaminedElements: 3},
			allowedWrites: 1,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			w := &failAfterWriter{allowedWrites: testCase.allowedWrites}
			err := printBatch(w, testCase.batch)
			if err == nil {
				t.Fatalf("expected the write failure on %s to propagate", testCase.name)
			}
		})
	}
}
