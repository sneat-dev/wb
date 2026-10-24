package gitrepo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/repopath"
)

// migrationFixture is a projects root with the remote store's new home inside
// .wb and the two legacy canonical-clone locations it may be migrated from.
type migrationFixture struct {
	origin  string
	root    string
	store   string
	hostDir string
	legacy  string
	notices *noticeLog
}

type noticeLog struct {
	mu   sync.Mutex
	list []string
}

func (n *noticeLog) add(message string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.list = append(n.list, message)
}

func (n *noticeLog) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.list...)
}

func newMigrationFixture(t *testing.T) migrationFixture {
	t.Helper()
	root := t.TempDir()
	return migrationFixture{
		origin:  bareOrigin(t),
		root:    root,
		store:   filepath.Join(root, ".wb", "remote-store", "team", "wb-state"),
		hostDir: filepath.Join(root, "github.com", "team", "wb-state"),
		legacy:  filepath.Join(root, "team", "wb-state"),
		notices: &noticeLog{},
	}
}

func (f migrationFixture) provider() *Provider {
	return New(Options{
		ClonePath: f.store, CloneURL: f.origin, RemoteStore: true,
		LegacyClonePaths: []string{f.hostDir, f.legacy},
		Notify:           f.notices.add,
	})
}

// legacyCloneWithUnpushedWork clones the store at the legacy path the way an
// earlier release left it: one committed-but-unpushed claim, one claim staged
// but never committed (the wb#899 residue), the generated .worktree.md, and
// the fleet hooks' core.hooksPath pointing into the clone.
func (f migrationFixture) legacyCloneWithUnpushedWork(t *testing.T, path string) {
	t.Helper()
	dqCovPreClone(t, f.origin, path)
	writeCloneFile(t, path, "claims/unpushed.yaml", "task: unpushed\n")
	gitIn(t, path, "add", "claims/unpushed.yaml")
	gitIn(t, path, "commit", "-q", "-m", "wb: claim unpushed")
	writeCloneFile(t, path, "claims/stranded.yaml", "task: stranded\n")
	gitIn(t, path, "add", "claims/stranded.yaml")
	writeCloneFile(t, path, ".worktree.md", "kind: canonical\n")
	gitIn(t, path, "config", "core.hooksPath", filepath.Join(path, ".git", "wb-hooks"))
}

func originFiles(t *testing.T, origin string) string {
	t.Helper()
	return gitIn(t, origin, "ls-tree", "-r", "--name-only", "main")
}

// A machine with no clone anywhere clones the store straight into .wb.
func TestFreshRemoteStoreClonesIntoTheStateDirectory(t *testing.T) {
	f := newMigrationFixture(t)
	if _, err := f.provider().Claims(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !repopath.IsClone(f.store) {
		t.Fatalf("no clone at %s", f.store)
	}
	for _, legacy := range []string{f.hostDir, f.legacy} {
		if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy path %s was created: %v", legacy, err)
		}
	}
	if notices := f.notices.all(); len(notices) != 0 {
		t.Fatalf("fresh clone printed notices %q, want none", notices)
	}
}

// The legacy clone is moved, not copied: its unpushed claim commit survives
// and is pushed by the next write, the stranded staged claim is healed away,
// and the canonical-clone leftovers (.worktree.md, the fleet hooksPath) are
// dropped. The legacy path no longer exists, so no fleet tool sees it.
func TestLegacyRemoteStoreCloneMovesIntoTheStateDirectoryKeepingUnpushedWork(t *testing.T) {
	for name, legacyOf := range map[string]func(migrationFixture) string{
		"owner/name":            func(f migrationFixture) string { return f.legacy },
		"github.com/owner/name": func(f migrationFixture) string { return f.hostDir },
	} {
		t.Run(name, func(t *testing.T) {
			f := newMigrationFixture(t)
			legacy := legacyOf(f)
			f.legacyCloneWithUnpushedWork(t, legacy)

			p := f.provider()
			if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC))); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("legacy clone %s still exists after the move: %v", legacy, err)
			}
			files := originFiles(t, f.origin)
			if !strings.Contains(files, "claims/unpushed.yaml") || !strings.Contains(files, "machines/alice/laptop/snapshot.yaml") {
				t.Fatalf("origin tree %q lacks the migrated unpushed claim or the new snapshot", files)
			}
			if strings.Contains(files, "claims/stranded.yaml") {
				t.Fatalf("origin tree %q carries the never-committed stranded claim", files)
			}
			if status := gitIn(t, f.store, "status", "--porcelain"); status != "" {
				t.Fatalf("migrated clone is not clean: %q", status)
			}
			if _, err := os.Stat(filepath.Join(f.store, ".worktree.md")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the canonical-clone marker survived the move: %v", err)
			}
			if out, err := exec.Command("git", "-C", f.store, "config", "--get", "core.hooksPath").Output(); err == nil {
				t.Fatalf("core.hooksPath still set to %q after the move", out)
			}
			notices := f.notices.all()
			if len(notices) != 1 || !strings.Contains(notices[0], "moved the remote store clone from "+legacy) {
				t.Fatalf("notices = %q, want one move notice", notices)
			}
		})
	}
}

// A hooksPath that does not point into the legacy clone and still exists is
// the operator's own setting, and the move keeps it.
func TestMovedRemoteStoreKeepsAHooksPathOutsideTheLegacyClone(t *testing.T) {
	f := newMigrationFixture(t)
	dqCovPreClone(t, f.origin, f.legacy)
	own := filepath.Join(f.root, "my-hooks")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.legacy, "config", "core.hooksPath", own)
	if _, err := f.provider().Claims(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, f.store, "config", "--get", "core.hooksPath"); got != own {
		t.Fatalf("core.hooksPath = %q, want %q kept", got, own)
	}
}

// Two providers racing for the first remote operation after an upgrade both
// succeed: the clone lock serializes them, one moves the clone, and the other
// finds it already in place.
func TestConcurrentProvidersMigrateTheLegacyCloneOnce(t *testing.T) {
	f := newMigrationFixture(t)
	f.legacyCloneWithUnpushedWork(t, f.legacy)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = f.provider().Claims(context.Background())
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("provider %d: %v", i, err)
		}
	}
	if notices := f.notices.all(); len(notices) != 1 || !strings.Contains(notices[0], "moved") {
		t.Fatalf("notices = %q, want exactly one move", notices)
	}
	if _, err := os.Stat(f.legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy clone still exists: %v", err)
	}
	if got := gitIn(t, f.store, "log", "-1", "--format=%s"); got != "wb: claim unpushed" {
		t.Fatalf("migrated clone HEAD = %q, want the unpushed claim commit", got)
	}
}

// A clone of some other repository at a legacy path is not the store: it is
// left alone and the store is cloned fresh.
func TestMigrationLeavesAnUnrelatedCloneAtTheLegacyPathAlone(t *testing.T) {
	f := newMigrationFixture(t)
	dqCovPreClone(t, bareOrigin(t), f.legacy)
	if _, err := f.provider().Claims(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !repopath.IsClone(f.legacy) || !repopath.IsClone(f.store) {
		t.Fatalf("want the unrelated clone kept and the store cloned fresh")
	}
	if notices := f.notices.all(); len(notices) != 0 {
		t.Fatalf("notices = %q, want none", notices)
	}
}

// A legacy clone with linked worktrees cannot move without orphaning them, so
// it is copied: the copy keeps the unpushed commit and tracks the real origin,
// and the legacy clone stays in place with a notice naming how to retire it.
func TestLegacyCloneWithLinkedWorktreesIsCopiedAndLeftInPlace(t *testing.T) {
	f := newMigrationFixture(t)
	f.legacyCloneWithUnpushedWork(t, f.legacy)
	gitIn(t, f.legacy, "worktree", "add", "-q", "-b", "side", filepath.Join(t.TempDir(), "side"))

	p := f.provider()
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC))); err != nil {
		t.Fatal(err)
	}
	if !repopath.IsClone(f.legacy) {
		t.Fatal("legacy clone with linked worktrees was not left in place")
	}
	if got := gitIn(t, f.store, "config", "--get", "remote.origin.url"); got != f.origin {
		t.Fatalf("copied clone origin = %q, want %q", got, f.origin)
	}
	if files := originFiles(t, f.origin); !strings.Contains(files, "claims/unpushed.yaml") {
		t.Fatalf("origin tree %q lacks the unpushed claim carried by the copy", files)
	}
	notices := f.notices.all()
	if len(notices) != 1 || !strings.Contains(notices[0], "copied") || !strings.Contains(notices[0], "linked worktrees") || !strings.Contains(notices[0], "retire it") {
		t.Fatalf("notices = %q, want one copy notice naming the reason and the retirement", notices)
	}
}

// When the clone can neither be moved nor copied, the store is cloned fresh
// and the notice tells the operator the legacy clone may hold unpushed work.
func TestUnmovableUncopyableLegacyCloneFallsBackToAFreshClone(t *testing.T) {
	f := newMigrationFixture(t)
	dqCovPreClone(t, f.origin, f.legacy)
	p := f.provider()
	p.rename = func(string, string) error { return errors.New("cross-device link") }
	p.clone = func(string, string) error { return errors.New("copy refused") }
	if _, err := p.Claims(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !repopath.IsClone(f.store) || !repopath.IsClone(f.legacy) {
		t.Fatal("want a fresh store clone and the legacy clone left in place")
	}
	notices := f.notices.all()
	if len(notices) != 1 || !strings.Contains(notices[0], "cross-device link") || !strings.Contains(notices[0], "cloning it fresh") {
		t.Fatalf("notices = %q, want one fresh-clone notice naming the move failure", notices)
	}
}

// A copy that cannot be pointed at the configured origin fails the operation
// and is removed, so the next run retries instead of finding a store clone
// that tracks the legacy path and refusing it forever.
func TestCopiedCloneThatCannotTrackTheOriginIsRemovedAndRetried(t *testing.T) {
	f := newMigrationFixture(t)
	dqCovPreClone(t, f.origin, f.legacy)
	p := f.provider()
	p.rename = func(string, string) error { return errors.New("cross-device link") }
	// A "copy" git cannot even run in (a file, not a directory), so set-url
	// fails without git searching upward into any enclosing repository.
	p.clone = func(_, dest string) error { return os.WriteFile(dest, nil, 0o600) }
	_, err := p.Claims(context.Background())
	if err == nil || !strings.Contains(err.Error(), "point the copied remote store clone") {
		t.Fatalf("Claims = %v, want the set-url failure", err)
	}
	if _, statErr := os.Stat(f.store); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("half-made copy left at %s: %v", f.store, statErr)
	}
	if _, err := f.provider().Claims(context.Background()); err != nil {
		t.Fatalf("retry after the failed copy = %v, want the migration to succeed", err)
	}
	if !repopath.IsClone(f.store) || repopath.IsClone(f.legacy) {
		t.Fatal("retry did not move the legacy clone into the store")
	}
}

// A hooksPath naming a directory that no longer exists (spelled through a
// symlink or another case, so the lexical legacy check misses it) is dropped.
func TestMovedRemoteStoreDropsAHooksPathToAMissingDirectory(t *testing.T) {
	f := newMigrationFixture(t)
	dqCovPreClone(t, f.origin, f.legacy)
	gitIn(t, f.legacy, "config", "core.hooksPath", filepath.Join(f.root, "gone", "wb-hooks"))
	if _, err := f.provider().Claims(context.Background()); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", f.store, "config", "--get", "core.hooksPath").Output(); err == nil {
		t.Fatalf("core.hooksPath still set to %q", out)
	}
}

// The legacy clone's own lock is what an older WB binary on this machine
// holds; failing to take it fails the operation instead of moving the clone
// out from under that process.
func TestMigrationFailsWhenTheLegacyCloneLockCannotBeTaken(t *testing.T) {
	f := newMigrationFixture(t)
	dqCovPreClone(t, f.origin, f.legacy)
	if err := os.MkdirAll(f.legacy+cloneLockSuffix, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := f.provider().Claims(context.Background()); err == nil || !strings.Contains(err.Error(), "open wb-state clone lock") {
		t.Fatalf("Claims = %v, want the legacy lock failure", err)
	}
	if !repopath.IsClone(f.legacy) {
		t.Fatal("legacy clone moved without its lock")
	}
}

// Without an explicit Notify, notices go to stderr rather than nowhere.
func TestProviderNoticesDefaultToStderr(t *testing.T) {
	t.Parallel()
	p := New(Options{})
	if p.opts.Notify == nil {
		t.Fatal("New left Notify nil")
	}
	p.opts.Notify("wb: test notice from TestProviderNoticesDefaultToStderr")
}

func TestWithinPathMatchesTheRootAndItsDescendantsOnly(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "p", "team", "wb-state")
	for path, want := range map[string]bool{
		root:                                 true,
		filepath.Join(root, ".git", "hooks"): true,
		root + "-other":                      false,
		filepath.Join(string(filepath.Separator), "p", "elsewhere"): false,
	} {
		if got := withinPath(path, root); got != want {
			t.Errorf("withinPath(%q) = %v, want %v", path, got, want)
		}
	}
}
