package landingcontext

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/locallink"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestOwnerResolutionAndReleaseBoundaries(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("root failed")
	badRoot := func(string) (string, error) { return "", sentinel }
	if dir, err := sessionDirectoryWithRoot("root", badRoot); dir != "" || err != sentinel {
		t.Fatal(dir, err)
	}
	called := false
	resolve := func(string, int) (session.Record, bool) { called = true; return session.Record{}, false }
	if owner := resolveOwner("root", "cmd", 4, badRoot, resolve); owner.WBSessionID != "" || called {
		t.Fatal(owner, called)
	}
	owner := resolveOwner("root", "cmd", 4, func(string) (string, error) { return "sessions", nil }, func(dir string, pid int) (session.Record, bool) {
		if dir != "sessions" || pid != 4 {
			t.Fatal(dir, pid)
		}
		return session.Record{WBSessionID: "id", PID: 4, Runtime: "runtime", Model: "model"}, true
	})
	if owner != (landinglane.Owner{WBSessionID: "id", PID: 4, Runtime: "runtime", Model: "model", Command: "cmd"}) {
		t.Fatal(owner)
	}
	if lane := LaneRequest(t.TempDir(), "command", "because", true, -123); !lane.TakeOver || lane.TakeoverReason != "because" || lane.Owner.WBSessionID != "" {
		t.Fatal(lane)
	}
	receipt := orchestrate.WorktreeMergeReceipt{Repository: "acme/app", Target: "main", Status: orchestrate.WorktreeMergeLanded}
	ownerFor := func(root, command string, pid int) landinglane.Owner { return owner }
	releaseCalls := 0
	release := func(home, repository, target, id string) error {
		releaseCalls++
		if home != "home" || repository != receipt.Repository || target != "main" || id != "id" {
			t.Fatal(home, repository, target, id)
		}
		return sentinel
	}
	releaseWorktreeLane("root", receipt, 4, ownerFor, badRoot, release)
	if releaseCalls != 0 {
		t.Fatal("released without root")
	}
	releaseWorktreeLane("root", receipt, 4, ownerFor, func(string) (string, error) { return "home", nil }, release)
	if releaseCalls != 1 {
		t.Fatal(releaseCalls)
	}
	ReleaseWorktreeLane("root", orchestrate.WorktreeMergeReceipt{}, -1)
}
func TestGuardReceiptSourcesAndReadFailures(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "receipt.json")
	if err := os.WriteFile(path, []byte(`{"sources":[{"worktree":"first"},{"worktree":"first"},{"worktree":""}],"candidate":{"worktree":"last"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := worktreeMergeReceiptWorktrees(path); err != nil || !reflect.DeepEqual(got, []string{"first", "last"}) {
		t.Fatal(got, err)
	}
	if err := CheckReceipt(root, path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"sources":[{"worktree":"same"}],"candidate":{"worktree":"same"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := worktreeMergeReceiptWorktrees(path); err != nil || !reflect.DeepEqual(got, []string{"same"}) {
		t.Fatal(got, err)
	}
	if err := os.WriteFile(path, []byte(`bad`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckReceipt(root, path); err == nil || !strings.Contains(err.Error(), "read merge receipt ") {
		t.Fatal(err)
	}
	if err := CheckReceipt(root, filepath.Join(root, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	sentinel := errors.New("store failed")
	openFail := func(string) (*streams.Store, error) { return nil, sentinel }
	if err := checkWorktrees(root, nil, openFail, locallink.HasLiveLink); err != sentinel {
		t.Fatal(err)
	}
	if err := checkRepository(root, "acme/app", openFail); err != sentinel {
		t.Fatal(err)
	}
	open := func(string) (*streams.Store, error) { return streams.OpenAt(filepath.Join(root, "empty")), nil }
	if err := checkWorktrees(root, []string{"path"}, open, func(locallink.LiveLinkStore, string) ([]locallink.LiveLink, error) { return nil, sentinel }); err != sentinel {
		t.Fatal(err)
	}
	if err := checkWorktrees(root, []string{"path"}, open, func(locallink.LiveLinkStore, string) ([]locallink.LiveLink, error) {
		return []locallink.LiveLink{{Source: "go.work", Sanctioned: "undo"}}, nil
	}); err == nil || !strings.Contains(err.Error(), "undo") {
		t.Fatal(err)
	}
	badStore := filepath.Join(root, "bad-store")
	if err := os.WriteFile(badStore, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkRepository(root, "acme/app", func(string) (*streams.Store, error) { return streams.OpenAt(badStore), nil }); err == nil {
		t.Fatal("store list failure swallowed")
	}
	if appender, name := eventsWith(root, "acme/app", openFail); name != "" {
		t.Fatal(name)
	} else if err := appender.Append(streams.Event{}); err != nil {
		t.Fatal(err)
	}
	if err := checkRepositoryWorktreesWith(root, "acme/app", func(context.Context, worktrees.ListOptions) (worktrees.ListOutcome, error) {
		return worktrees.ListOutcome{}, sentinel
	}); err != sentinel {
		t.Fatal(err)
	}
	if err := checkRepositoryWorktreesWith(root, "acme/app", func(_ context.Context, o worktrees.ListOptions) (worktrees.ListOutcome, error) {
		if o.ProjectsRoot != root || o.Filter != "acme/app" {
			t.Fatal(o)
		}
		return worktrees.ListOutcome{Results: []worktrees.ListResult{{Repository: "acme/app", WorktreeDir: filepath.Join(root, "worktree")}, {Repository: "other/app"}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestGuardIncludesMembersWithExactRepositoryCase(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, err := streams.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(root, "member")
	if err := os.MkdirAll(worktree, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(streams.Stream{Name: "known", Phase: streams.PhaseOpen, Members: []streams.Member{{Repository: "acme/app", Worktree: worktree}, {Repository: "other/app", Worktree: "ignored"}}}); err != nil {
		t.Fatal(err)
	}
	if err := CheckRepository(root, "acme/app"); err != nil {
		t.Fatal(err)
	}
}
func TestSuggestionFailureSuccessAndOptions(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	sentinel := errors.New("missing")
	loadCalls := 0
	load := func(context.Context, worktrees.LoadWorkLogOptions) (worktrees.WorkLogView, error) {
		loadCalls++
		return worktrees.WorkLogView{}, nil
	}
	if got := SuggestCloses(ctx, t.TempDir(), "missing"); got != nil {
		t.Fatal(got)
	}
	if got := suggestCloses(ctx, "root", "arg", func(context.Context, string, string) (string, error) { return "", sentinel }, load); got != nil || loadCalls != 0 {
		t.Fatal(got, loadCalls)
	}
	resolve := func(c context.Context, root, arg string) (string, error) {
		if c != ctx || root != "root" || arg != "." {
			t.Fatal(c, root, arg)
		}
		return "checkout", nil
	}
	if got := suggestCloses(ctx, "root", " ", resolve, func(_ context.Context, o worktrees.LoadWorkLogOptions) (worktrees.WorkLogView, error) {
		if o.ProjectsRoot != "root" || o.Worktree != "checkout" || !o.IncludePromptBodies {
			t.Fatal(o)
		}
		return worktrees.WorkLogView{}, sentinel
	}); got != nil {
		t.Fatal(got)
	}
	if got := suggestCloses(ctx, "root", "", resolve, load); got != nil {
		t.Fatal(got)
	}
	if got := suggestCloses(ctx, "root", "", resolve, func(context.Context, worktrees.LoadWorkLogOptions) (worktrees.WorkLogView, error) {
		return worktrees.WorkLogView{OriginalPrompt: &worktrees.OriginalPromptView{Body: "fix #42 and #43"}}, nil
	}); !reflect.DeepEqual(got, []int{42, 43}) {
		t.Fatal(got)
	}
	if got := worktreeArgOrCurrent("task"); got != "task" {
		t.Fatal(got)
	}
}
