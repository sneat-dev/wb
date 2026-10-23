package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/landingcontext"
	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestMergeBindingsPreserveActualLinkGuardErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	worktree := filepath.Join(root, "consumer")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	bindings := mergeBindings()
	if err := bindings.RefusePaths(root, []string{worktree}); err != nil {
		t.Fatalf("unlinked checkout: %v", err)
	}
	store, err := streams.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	// Persist actual stream custody; no filesystem link operation is simulated.
	if _, err := store.Create(streams.Stream{
		Name: "binding-contract", Phase: streams.PhaseOpen,
		LinkedConsumers: []streams.LinkedConsumerBinding{{
			Repository: "acme/consumer", Worktree: worktree,
			Links: []streams.Link{{Library: filepath.Join(root, "library"),
				LibraryRepository: "acme/library", Mechanism: streams.MechanismPnpmLink,
				Identity: "@acme/library", State: streams.LinkStateApplied}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	var refusal *landingcontext.Refusal
	if err := landingcontext.CheckWorktrees(root, []string{worktree}); !errors.As(err, &refusal) {
		t.Fatalf("actual domain guard must refuse persisted live link: %v", err)
	}
	receipt := filepath.Join(root, "receipt.json")
	if err := os.WriteFile(receipt, []byte(`{"sources":[{"worktree":"`+filepath.ToSlash(worktree)+`"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"paths", func() error { return bindings.RefusePaths(root, []string{worktree}) }},
		{"receipt", func() error { return bindings.RefuseReceipt(root, receipt) }},
		{"directory", func() error { return bindings.RefuseReceipt(root, worktree) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var coded *exitError
			if err := tc.call(); !errors.As(err, &coded) || coded.code != exitUsage || coded.message != refusal.Message {
				t.Fatalf("guard error lost original usage classification/message: %v", err)
			}
		})
	}
	missing := filepath.Join(root, "absent.json")
	err = bindings.RefuseReceipt(root, missing)
	var coded *exitError
	if !errors.Is(err, os.ErrNotExist) || errors.As(err, &coded) {
		t.Fatalf("missing receipt must remain an ordinary filesystem error: %v", err)
	}
}

func TestMergeBindingsUseActualProcessOwnerAndReleasePrivateLane(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home, err := wbhome.Root(root)
	if err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(home, session.DirName)
	record, err := session.Register(sessionDir, session.Record{
		PID: os.Getpid(), WBSessionID: "wbs-merge-binding", Runtime: "test", Model: "contract",
	})
	if err != nil {
		t.Fatal(err)
	}
	bindings := mergeBindings()
	request := bindings.LaneRequest(root, "worktree merge", "private takeover reason", true)
	if request.Owner.WBSessionID != record.WBSessionID || request.Owner.PID != os.Getpid() ||
		request.Owner.Runtime != record.Runtime || request.Owner.Model != record.Model ||
		request.Owner.Command != "worktree merge" || !request.TakeOver || request.TakeoverReason != "private takeover reason" {
		t.Fatalf("lane request lost actual process identity or caller arguments: %+v", request)
	}
	if _, err := landinglane.Acquire(home, landinglane.AcquireRequest{
		Repository: "acme/app", Target: "main", Self: request.Owner, SessionDir: sessionDir,
	}); err != nil {
		t.Fatalf("acquire actual private lane: %v", err)
	}
	if _, held, err := landinglane.Read(home, "acme/app", "main"); err != nil || !held {
		t.Fatalf("lane before release: held=%v err=%v", held, err)
	}
	bindings.ReleaseLane(root, orchestrate.WorktreeMergeReceipt{
		Repository: "acme/app", Target: "main", Status: orchestrate.WorktreeMergeLanded,
	})
	if _, held, err := landinglane.Read(home, "acme/app", "main"); err != nil || held {
		t.Fatalf("actual owner's lane after release: held=%v err=%v", held, err)
	}
}
