package locallink

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/streams"
)

// newLinkedConsumerFixture is newFixture's counterpart for a repository
// admitted as a LinkedConsumer rather than a stream Member — the shape `wb
// deps propagate local ... --register-consumer` writes (github.com/sneat-dev/wb
// commit e0c6f16 "feat: register local-only stream consumers", never merged
// to main; see internal/streams/testdata/stream_schema_v2.json for a real
// stream-state file carrying one). Resolving `--undo` only against Members
// was the defect: a registered consumer's link could be recorded but never
// undone, so the merge guard blocked landing forever.
func newLinkedConsumerFixture(t *testing.T, libraryFiles, consumerFiles map[string]string) fixture {
	t.Helper()
	base := t.TempDir()
	library := writeTree(t, filepath.Join(base, "library"), libraryFiles)
	consumer := writeTree(t, filepath.Join(base, "consumer"), consumerFiles)
	store := streams.OpenAt(filepath.Join(base, "wb-home", "streams"))
	if _, err := store.Create(streams.Stream{
		Name: "fixture",
		Members: []streams.Member{
			{Repository: "acme/library", Role: streams.RoleLibrary, Worktree: library, Branch: "stream/fixture", Base: "main"},
		},
		LinkedConsumers: []streams.LinkedConsumerBinding{
			{Repository: "acme/app", Worktree: consumer},
		},
	}); err != nil {
		t.Fatal(err)
	}
	git, node, verifier := newFakeGit(), newFakeNode(), newFakeVerifier()
	fixed := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	return fixture{
		engine: &Engine{
			Store: store, Git: git, Node: node, Verifier: verifier,
			CacheRoot: filepath.Join(base, "cache"),
			Now:       func() time.Time { return fixed },
		},
		store: store, git: git, node: node, verifier: verifier,
		library: library, consumer: consumer,
	}
}

// Undo of a registered-consumer pnpm-link record, normal path: the link
// resolves and records against LinkedConsumers (proving the link()-side fix,
// since a consumer resolvable only as a Member would have refused to record
// it as link-not-recordable), and --undo clears both the filesystem and the
// LinkedConsumers record.
func TestUndoOfRegisteredConsumerPnpmLinkRecord(t *testing.T) {
	fixture := newLinkedConsumerFixture(t,
		map[string]string{
			"libs/core/package.json": `{"name":"@acme/core"}`,
			"package.json":           `{"private":true}`,
		},
		map[string]string{
			"package.json":   `{"dependencies":{"@acme/core":"1.0.0"}}`,
			"pnpm-lock.yaml": "lockfileVersion: '9.0'\n",
		})

	link, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	})
	if err != nil || link.Failed() {
		t.Fatalf("link = %#v, err = %v", link.Consumers, err)
	}
	if link.Stream != "fixture" {
		t.Fatalf("resolved stream = %q, want the stream holding the LinkedConsumer", link.Stream)
	}

	stream, err := fixture.store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	consumerBinding, ok := stream.LinkedConsumer("acme/app")
	if !ok || len(consumerBinding.Links) != 1 {
		t.Fatalf("LinkedConsumers = %#v, want one recorded pnpm-link", stream.LinkedConsumers)
	}
	if len(stream.Members) != 1 {
		t.Fatalf("a LinkedConsumer link must never be written to Members: %#v", stream.Members)
	}

	// The merge guard fires before undo.
	before, err := fixture.store.LiveLinksForWorktree(fixture.consumer)
	if err != nil || len(before) != 1 {
		t.Fatalf("LiveLinksForWorktree before undo = %#v (err %v), want the recorded link", before, err)
	}

	undo, err := fixture.engine.Run(context.Background(), Options{Consumers: []string{fixture.consumer}, Undo: true})
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if undo.Failed() {
		t.Fatalf("undo reported errors: %#v", undo.Consumers)
	}
	if undo.Consumers[0].Skipped {
		t.Fatalf("undo skipped a registered consumer's recorded link: %#v", undo.Consumers[0])
	}
	if len(fixture.node.unlinked) != 1 || fixture.node.unlinked[0] != fixture.consumer+" @acme/core" {
		t.Fatalf("unlinked = %v, want the registered consumer's package unlinked", fixture.node.unlinked)
	}

	stream, err = fixture.store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	consumerBinding, _ = stream.LinkedConsumer("acme/app")
	if len(consumerBinding.Links) != 0 {
		t.Fatalf("LinkedConsumers link survived undo: %#v", consumerBinding.Links)
	}

	// Guard test: LiveLinksForWorktree must no longer report the worktree.
	after, err := fixture.store.LiveLinksForWorktree(fixture.consumer)
	if err != nil || len(after) != 0 {
		t.Fatalf("LiveLinksForWorktree after undo = %#v (err %v), want none — the merge guard must clear", after, err)
	}
	if live, err := HasLiveLink(fixture.store, fixture.consumer); err != nil || len(live) != 0 {
		t.Fatalf("HasLiveLink after undo = %#v (err %v), want the guard satisfied", live, err)
	}
}

// Undo of a registered-consumer pnpm-link record whose filesystem link was
// already superseded by a published package before --undo ran (a governed
// `pnpm install` mid-stream is the common trigger, per execports.go's
// resolvePublishedPackage). Node.Unlink reports the supersession note instead
// of erroring, and the LinkedConsumers record still clears exactly as a
// normal undo would.
func TestUndoOfRegisteredConsumerPnpmLinkRecordSuperseded(t *testing.T) {
	fixture := newLinkedConsumerFixture(t,
		map[string]string{
			"libs/core/package.json": `{"name":"@acme/core"}`,
			"package.json":           `{"private":true}`,
		},
		map[string]string{
			"package.json":   `{"dependencies":{"@acme/core":"1.0.0"}}`,
			"pnpm-lock.yaml": "lockfileVersion: '9.0'\n",
		})

	link, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	})
	if err != nil || link.Failed() {
		t.Fatalf("link = %#v, err = %v", link.Consumers, err)
	}

	fixture.node.unlinkNote = map[string]string{
		fixture.consumer + " @acme/core": "link superseded by published @acme/core@2.0.0; record cleared",
	}

	undo, err := fixture.engine.Run(context.Background(), Options{Consumers: []string{fixture.consumer}, Undo: true})
	if err != nil || undo.Failed() {
		t.Fatalf("undo = %#v, err = %v", undo.Consumers, err)
	}
	if len(undo.Consumers) != 1 || !containsSubstring(undo.Consumers[0].Notes, "superseded by published") {
		t.Fatalf("undo notes = %#v, want the supersession note threaded through", undo.Consumers)
	}

	stream, err := fixture.store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	consumerBinding, _ := stream.LinkedConsumer("acme/app")
	if len(consumerBinding.Links) != 0 {
		t.Fatalf("superseded link's record survived undo: %#v", consumerBinding.Links)
	}

	// Guard test: LiveLinksForWorktree must no longer report the worktree.
	after, err := fixture.store.LiveLinksForWorktree(fixture.consumer)
	if err != nil || len(after) != 0 {
		t.Fatalf("LiveLinksForWorktree after undo = %#v (err %v), want none", after, err)
	}
}

// Undo of a registered-consumer go.work record: the same registration defect
// applied to the Go mechanism, since resolveConsumerStreams gated recording a
// go.work link exactly the way it gated an npm one.
func TestUndoOfRegisteredConsumerGoWorkRecord(t *testing.T) {
	fixture := newLinkedConsumerFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})

	link, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	})
	if err != nil || link.Failed() {
		t.Fatalf("link = %#v, err = %v", link.Consumers, err)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.consumer, "go.work")); statErr != nil {
		t.Fatalf("go.work was not written for the registered consumer: %v", statErr)
	}

	stream, err := fixture.store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	consumerBinding, ok := stream.LinkedConsumer("acme/app")
	if !ok || len(consumerBinding.Links) != 1 || consumerBinding.Links[0].Mechanism != streams.MechanismGoWork {
		t.Fatalf("LinkedConsumers = %#v, want one recorded go.work link", stream.LinkedConsumers)
	}

	undo, err := fixture.engine.Run(context.Background(), Options{Consumers: []string{fixture.consumer}, Undo: true})
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if undo.Failed() {
		t.Fatalf("undo reported errors: %#v", undo.Consumers)
	}
	if undo.Consumers[0].Skipped {
		t.Fatalf("undo skipped a registered consumer's recorded go.work link: %#v", undo.Consumers[0])
	}
	if _, statErr := os.Stat(filepath.Join(fixture.consumer, "go.work")); !os.IsNotExist(statErr) {
		t.Errorf("go.work survived undo of a registered consumer: %v", statErr)
	}

	stream, err = fixture.store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	consumerBinding, _ = stream.LinkedConsumer("acme/app")
	if len(consumerBinding.Links) != 0 {
		t.Fatalf("go.work record survived undo: %#v", consumerBinding.Links)
	}

	// Guard test: LiveLinksForWorktree must no longer report the worktree.
	after, err := fixture.store.LiveLinksForWorktree(fixture.consumer)
	if err != nil || len(after) != 0 {
		t.Fatalf("LiveLinksForWorktree after undo = %#v (err %v), want none", after, err)
	}
	if live, err := HasLiveLink(fixture.store, fixture.consumer); err != nil || len(live) != 0 {
		t.Fatalf("HasLiveLink after undo = %#v (err %v), want the guard satisfied", live, err)
	}
}

func containsSubstring(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}
