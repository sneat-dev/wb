package locallink

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
)

// lgCovGit is the existing fakeGit with injectable failures, so the Engine's
// error paths are reachable without a repository.
type lgCovGit struct {
	*fakeGit
	trackedErr       error
	trackedErrOnCall int
	trackedCalls     int
	excludeErr       error
}

func lgCovNewGit() *lgCovGit { return &lgCovGit{fakeGit: newFakeGit()} }

func (git *lgCovGit) ExcludePath(_ context.Context, _, _ string) error { return git.excludeErr }

func (git *lgCovGit) TrackedChanges(ctx context.Context, dir string) ([]string, error) {
	git.trackedCalls++
	if git.trackedErr != nil && (git.trackedErrOnCall == 0 || git.trackedCalls == git.trackedErrOnCall) {
		return nil, git.trackedErr
	}
	return git.fakeGit.TrackedChanges(ctx, dir)
}

// lgCovEngineFixture builds an engine over a caller-supplied store, which is
// what the stream-state failure paths need.
func lgCovEngineFixture(t *testing.T, store *streams.Store, git Git, node Node) *Engine {
	t.Helper()
	if git == nil {
		git = newFakeGit()
	}
	if node == nil {
		node = newFakeNode()
	}
	return &Engine{Store: store, Git: git, Node: node, Verifier: newFakeVerifier(), CacheRoot: t.TempDir()}
}

func TestLgCovResultFailedCoversVerification(t *testing.T) {
	t.Parallel()
	if (Result{Consumers: []ConsumerResult{{Verification: &Verification{Passed: false}}}}).Failed() != true {
		t.Fatal("a consumer with a failed verification did not fail the result")
	}
	if (Result{Consumers: []ConsumerResult{{Verification: &Verification{Passed: true}}}}).Failed() {
		t.Fatal("a consumer with a passing verification failed the result")
	}
	if (Result{}).Failed() {
		t.Fatal("an empty result failed")
	}
}

func TestLgCovLinkValidatesItsInputs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	engine := lgCovEngineFixture(t, nil, nil, nil)

	if _, err := engine.Run(ctx, Options{Consumers: []string{t.TempDir()}}); err == nil ||
		!strings.Contains(err.Error(), "a library worktree is required") {
		t.Fatalf("error = %v, want the missing-library refusal", err)
	}
	if _, err := engine.Run(ctx, Options{Library: t.TempDir()}); err == nil ||
		!strings.Contains(err.Error(), "at least one --to") {
		t.Fatalf("error = %v, want the missing-consumer refusal", err)
	}
}

func TestLgCovLinkReportsDiscoveryHashAndDeclarationFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("the library cannot be discovered", func(t *testing.T) {
		t.Parallel()
		engine := lgCovEngineFixture(t, nil, nil, nil)
		_, err := engine.Run(ctx, Options{Library: filepath.Join(t.TempDir(), "missing"), Consumers: []string{t.TempDir()}})
		if err == nil {
			t.Fatal("linking a missing library reported success")
		}
	})

	t.Run("the content hash fails", func(t *testing.T) {
		t.Parallel()
		library, consumer := t.TempDir(), t.TempDir()
		lgCovWriteFile(t, filepath.Join(library, "backend", "go.mod"), goLibraryModule)
		git := lgCovNewGit()
		git.hashErr = errors.New("git is unavailable")
		engine := lgCovEngineFixture(t, nil, git, nil)
		_, err := engine.Run(ctx, Options{Library: library, Consumers: []string{consumer}})
		if err == nil || !strings.Contains(err.Error(), "git is unavailable") {
			t.Fatalf("error = %v, want the content-hash failure", err)
		}
	})

	t.Run("the consumer has no stream to record against", func(t *testing.T) {
		t.Parallel()
		library, consumer := t.TempDir(), t.TempDir()
		lgCovWriteFile(t, filepath.Join(library, "backend", "go.mod"), goLibraryModule)
		engine := lgCovEngineFixture(t, nil, nil, nil)
		_, err := engine.Run(ctx, Options{Library: library, Consumers: []string{consumer}})
		refusal, ok := Refused(err)
		if !ok || refusal.Code != RefusalNotRecordable {
			t.Fatalf("err = %v, want a link-not-recordable refusal", err)
		}
	})

	t.Run("the consumer's declarations cannot be read", func(t *testing.T) {
		t.Parallel()
		fixture := newFixture(t,
			map[string]string{"backend/go.mod": goLibraryModule},
			map[string]string{"consumer-mod.txt": "not a module\n"})
		// A dangling go.mod symlink makes module discovery fail with a real
		// read error rather than "no modules".
		if err := os.Symlink(filepath.Join(fixture.consumer, "nowhere"), filepath.Join(fixture.consumer, "go.mod")); err != nil {
			t.Fatal(err)
		}
		result, err := fixture.engine.Run(ctx, Options{Library: fixture.library, Consumers: []string{fixture.consumer}})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Consumers) != 1 || len(result.Consumers[0].Errors) == 0 {
			t.Fatalf("consumers = %#v, want a reported declaration failure", result.Consumers)
		}
	})

	t.Run("tracked changes cannot be read before linking", func(t *testing.T) {
		t.Parallel()
		fixture := newFixture(t,
			map[string]string{"backend/go.mod": goLibraryModule},
			map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})
		git := lgCovNewGit()
		git.trackedErr = errors.New("status failed")
		fixture.engine.Git = git
		result, err := fixture.engine.Run(ctx, Options{Library: fixture.library, Consumers: []string{fixture.consumer}})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Consumers[0].Errors) == 0 || !strings.Contains(result.Consumers[0].Errors[0], "status failed") {
			t.Fatalf("errors = %#v, want the tracked-changes failure", result.Consumers[0].Errors)
		}
	})

	t.Run("tracked changes cannot be read after linking", func(t *testing.T) {
		t.Parallel()
		fixture := newFixture(t,
			map[string]string{"backend/go.mod": goLibraryModule},
			map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})
		git := lgCovNewGit()
		git.trackedErr = errors.New("status failed late")
		git.trackedErrOnCall = 2
		fixture.engine.Git = git
		result, err := fixture.engine.Run(ctx, Options{Library: fixture.library, Consumers: []string{fixture.consumer}})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Consumers[0].Errors) == 0 || !strings.Contains(result.Consumers[0].Errors[0], "status failed late") {
			t.Fatalf("errors = %#v, want the post-link tracked-changes failure", result.Consumers[0].Errors)
		}
		if _, statErr := os.Stat(filepath.Join(fixture.consumer, goWorkFile)); statErr != nil {
			t.Fatalf("the link was not applied before the failure was noticed: %v", statErr)
		}
	})
}

func TestLgCovLinkNpmWithoutAToolchainAndSkippedInstall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("no Node toolchain", func(t *testing.T) {
		t.Parallel()
		fixture := newFixture(t,
			map[string]string{"libs/core/package.json": `{"name":"@acme/core"}`},
			map[string]string{"package.json": `{"dependencies":{"@acme/core":"1.0.0"}}`})
		fixture.engine.Node = nil
		result, err := fixture.engine.Run(ctx, Options{Library: fixture.library, Consumers: []string{fixture.consumer}})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Consumers[0].Errors) == 0 || !strings.Contains(result.Consumers[0].Errors[0], "no Node toolchain available") {
			t.Fatalf("errors = %#v, want the missing-toolchain report", result.Consumers[0].Errors)
		}
	})

	t.Run("a frozen install that cannot be evaluated is skipped", func(t *testing.T) {
		t.Parallel()
		fixture := newFixture(t,
			map[string]string{"libs/core/package.json": `{"name":"@acme/core"}`},
			map[string]string{"package.json": `{"dependencies":{"@acme/core":"1.0.0"}}`})
		fixture.node.installErr[fixture.consumer] = &SkippedCheck{Check: "frozen-install", Reason: "no lockfile baseline to prove"}
		result, err := fixture.engine.Run(ctx, Options{Library: fixture.library, Consumers: []string{fixture.consumer}})
		if err != nil {
			t.Fatal(err)
		}
		consumer := result.Consumers[0]
		if len(consumer.Errors) != 0 {
			t.Fatalf("errors = %#v, a skipped check is not a failure", consumer.Errors)
		}
		if len(consumer.SkippedChecks) != 1 || !strings.Contains(consumer.SkippedChecks[0], "no lockfile baseline to prove") {
			t.Fatalf("skipped checks = %#v, want the unproven check named", consumer.SkippedChecks)
		}
	})
}

func TestLgCovLinkReportsAGoWorkspaceThatCannotBeWritten(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})
	// A directory named go.work makes the workspace write fail after the
	// exclude was already written, so the consumer reports a real error.
	if err := os.Mkdir(filepath.Join(fixture.consumer, goWorkFile), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.engine.Run(context.Background(), Options{Library: fixture.library, Consumers: []string{fixture.consumer}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Consumers[0].Errors) == 0 || !strings.Contains(result.Consumers[0].Errors[0], "write go.work") {
		t.Fatalf("errors = %#v, want the go.work write failure", result.Consumers[0].Errors)
	}
}

func TestLgCovOpenStreamsFiltersAndFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := t.TempDir()
	library := writeTree(t, filepath.Join(base, "library"), map[string]string{"backend/go.mod": goLibraryModule})
	consumer := writeTree(t, filepath.Join(base, "consumer"), map[string]string{
		"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})

	t.Run("stream state cannot be listed", func(t *testing.T) {
		t.Parallel()
		notADirectory := filepath.Join(t.TempDir(), "store")
		lgCovWriteFile(t, notADirectory, "not a directory\n")
		engine := lgCovEngineFixture(t, streams.OpenAt(notADirectory), nil, nil)
		_, err := engine.Run(ctx, Options{Library: library, Consumers: []string{consumer}})
		if err == nil || !strings.Contains(err.Error(), "read stream store") {
			t.Fatalf("error = %v, want the unreadable store reported", err)
		}
	})

	t.Run("an unreadable stream refuses the link", func(t *testing.T) {
		t.Parallel()
		store := streams.OpenAt(filepath.Join(t.TempDir(), "streams"))
		lgCovWriteFile(t, filepath.Join(store.Root, "broken", "stream.json"), "{not json")
		engine := lgCovEngineFixture(t, store, nil, nil)
		_, err := engine.Run(ctx, Options{Library: library, Consumers: []string{consumer}})
		if err == nil || !strings.Contains(err.Error(), "stream state is unreadable for broken") {
			t.Fatalf("error = %v, want the unreadable-stream refusal", err)
		}
	})

	t.Run("ended streams and a name filter are honoured", func(t *testing.T) {
		t.Parallel()
		store := streams.OpenAt(filepath.Join(t.TempDir(), "streams"))
		if _, err := store.Create(streams.Stream{
			Name:    "ended",
			Phase:   streams.PhaseEnded,
			Members: []streams.Member{{Repository: "acme/app", Role: streams.RoleConsumer, Worktree: consumer}},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Create(streams.Stream{
			Name: "fixture",
			Members: []streams.Member{
				{Repository: "acme/library", Role: streams.RoleLibrary, Worktree: library},
				{Repository: "acme/app", Role: streams.RoleConsumer, Worktree: consumer},
			},
		}); err != nil {
			t.Fatal(err)
		}
		engine := lgCovEngineFixture(t, store, nil, nil)
		result, err := engine.Run(ctx, Options{Library: library, Consumers: []string{consumer}})
		if err != nil {
			t.Fatal(err)
		}
		if result.Stream != "fixture" {
			t.Fatalf("resolved stream = %q, want the open stream", result.Stream)
		}

		// A name filter that matches nothing must leave every consumer
		// unrecordable rather than silently linking.
		filtered := lgCovEngineFixture(t, store, nil, nil)
		_, err = filtered.Run(ctx, Options{Library: library, Consumers: []string{consumer}, Stream: "other"})
		refusal, ok := Refused(err)
		if !ok || refusal.Code != RefusalNotRecordable {
			t.Fatalf("err = %v, want the not-recordable refusal under a filter", err)
		}
	})
}

func TestLgCovRecordLinksRequiresStateAndAMatch(t *testing.T) {
	t.Parallel()
	engine := &Engine{}
	if err := engine.recordLinks("fixture", "/tmp/consumer", nil); err != nil {
		t.Fatalf("recording no links must be a no-op, got %v", err)
	}

	store := streams.OpenAt(filepath.Join(t.TempDir(), "streams"))
	consumer, other := t.TempDir(), t.TempDir()
	if _, err := store.Create(streams.Stream{
		Name: "fixture",
		Members: []streams.Member{
			{Repository: "acme/app", Role: streams.RoleConsumer, Worktree: other},
		},
		LinkedConsumers: []streams.LinkedConsumerBinding{
			{Repository: "acme/elsewhere", Worktree: other},
			{Repository: "acme/app", Worktree: consumer},
		},
	}); err != nil {
		t.Fatal(err)
	}
	links := []streams.Link{{Identity: "@acme/core", Mechanism: streams.MechanismPnpmLink, State: streams.LinkStateApplied}}
	engine = &Engine{Store: store}
	if err := engine.recordLinks("fixture", consumer, links); err != nil {
		t.Fatalf("recordLinks into a LinkedConsumer: %v", err)
	}
	stream, err := store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	binding, ok := stream.LinkedConsumer("acme/app")
	if !ok || len(binding.Links) != 1 {
		t.Fatalf("LinkedConsumers = %#v, want the link recorded against the matching binding", stream.LinkedConsumers)
	}
	if len(stream.Members[0].Links) != 0 {
		t.Fatalf("a non-matching member received the link: %#v", stream.Members[0].Links)
	}

	if err := engine.recordLinks("fixture", filepath.Join(t.TempDir(), "stranger"), links); err == nil ||
		!strings.Contains(err.Error(), "has no member at") {
		t.Fatalf("error = %v, want the unmatched-consumer refusal", err)
	}
	if err := engine.recordLinks("", consumer, links); err == nil ||
		!strings.Contains(err.Error(), "no stream was resolved") {
		t.Fatalf("error = %v, want the unresolved-stream refusal", err)
	}
}

func TestLgCovSmallHelpers(t *testing.T) {
	t.Parallel()
	if sameWorktree("", t.TempDir()) || sameWorktree(t.TempDir(), "") {
		t.Fatal("sameWorktree treated an empty path as a match")
	}

	if got := npmLinkGroups([]streams.Link{{Identity: "@acme/core", Workspace: ""}, {Identity: "@acme/core", Workspace: "."}}); len(got["."]) != 1 {
		t.Fatalf("npmLinkGroups = %v, want empty and dot workspaces merged and deduped", got)
	}
	if got := declarationWorkspaces([]streams.Declaration{{Workspace: ""}, {Workspace: "."}, {Workspace: "libs/app"}}); strings.Join(got, ",") != ".,libs/app" {
		t.Fatalf("declarationWorkspaces = %v, want dot-rooted and nested workspaces", got)
	}
	if got := newPaths([]string{"kept.txt"}, []string{"kept.txt", "added.txt"}); len(got) != 1 || got[0] != "added.txt" {
		t.Fatalf("newPaths = %v, want only the introduced path", got)
	}
}

// An Engine with no stream store at all still refuses rather than writing a
// link nothing can undo.
func TestLgCovLinkWithNoStoreRefuses(t *testing.T) {
	t.Parallel()
	library, consumer := t.TempDir(), t.TempDir()
	lgCovWriteFile(t, filepath.Join(library, "backend", "go.mod"), goLibraryModule)
	lgCovWriteFile(t, filepath.Join(consumer, "backend", "go.mod"), "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n")
	engine := lgCovEngineFixture(t, nil, nil, nil)
	_, err := engine.Run(context.Background(), Options{Library: library, Consumers: []string{consumer}})
	refusal, ok := Refused(err)
	if !ok || refusal.Code != RefusalNotRecordable {
		t.Fatalf("err = %v, want the not-recordable refusal with no store", err)
	}
}

// A relative path can only be made absolute through the process working
// directory, so a working directory that no longer resolves is the one state
// in which filepath.Abs fails. On systems that still resolve it the test
// reports nothing rather than asserting a platform fact it does not have.
func TestLgCovRelativePathsWhenTheWorkingDirectoryIsGone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("removing the process working directory is a POSIX behaviour")
	}
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	doomed := t.TempDir()
	if err := os.Chdir(doomed); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if restoreErr := os.Chdir(original); restoreErr != nil {
			t.Errorf("restore working directory %s: %v", original, restoreErr)
		}
	})
	if err := os.RemoveAll(doomed); err != nil {
		t.Fatal(err)
	}
	if _, err := filepath.Abs("relative-probe"); err == nil {
		t.Skip("this operating system still resolves a removed working directory")
	}
	ctx := context.Background()

	t.Run("a relative library cannot be resolved", func(t *testing.T) {
		t.Parallel()
		engine := &Engine{}
		if _, err := engine.Run(ctx, Options{Library: "relative-library", Consumers: []string{"relative-consumer"}}); err == nil {
			t.Fatal("linking a relative library with no working directory reported success")
		}
	})

	t.Run("a relative consumer cannot be resolved", func(t *testing.T) {
		t.Parallel()
		store := streams.OpenAt(filepath.Join(t.TempDir(), "streams"))
		engine := &Engine{Store: store, Git: newFakeGit(), Node: newFakeNode()}
		library := t.TempDir()
		lgCovWriteFile(t, filepath.Join(library, "backend", "go.mod"), goLibraryModule)
		if _, err := engine.Run(ctx, Options{Library: library, Consumers: []string{"relative-consumer"}}); err == nil {
			t.Fatal("linking a relative consumer with no working directory reported success")
		}
	})

	t.Run("a relative consumer cannot be undone", func(t *testing.T) {
		t.Parallel()
		store := streams.OpenAt(filepath.Join(t.TempDir(), "streams"))
		engine := &Engine{Store: store}
		if _, err := engine.Run(ctx, Options{Undo: true, Consumers: []string{"relative-consumer"}}); err == nil {
			t.Fatal("undoing a relative consumer with no working directory reported success")
		}
	})
}
