package gitrepo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/testenv"
)

// lockBranchRef plants a stale lock on the clone's main branch ref, so the
// next commit's ref update fails.
func lockBranchRef(t *testing.T, clonePath string) {
	t.Helper()
	dir := filepath.Join(clonePath, ".git", "refs", "heads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeCloneFile(t *testing.T, clonePath, relative, content string) {
	t.Helper()
	path := filepath.Join(clonePath, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// storeMachine is machine for the configured `wb remote` store, the only
// provider that self-heals (Options.RemoteStore).
func storeMachine(t *testing.T, origin string) *Provider {
	t.Helper()
	p := machine(t, origin)
	p.opts.RemoteStore = true
	return p
}

// installWBStyleGuardHooks installs core.hooksPath hooks modelled on WB's
// managed guard in the remote-store clone, and returns a marker path the
// pre-commit hook touches every time it runs. pre-commit refuses while the
// shared index holds a staged change (the wb#899 wedge); pre-push refuses
// while the clone has any staged or modified tracked file.
func installWBStyleGuardHooks(t *testing.T, clonePath string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("hook scripts rely on a POSIX shell")
	}
	hooks := filepath.Join(clonePath, ".git", "wb-hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	ran := filepath.Join(t.TempDir(), "pre-commit-ran")
	refuse := "  echo 'error: canonical clone has local changes; canonical clones must remain clean' >&2\n  exit 1\n"
	scripts := map[string]string{
		"pre-commit": "#!/bin/sh\ntouch '" + ran + "'\nif ! (unset GIT_INDEX_FILE; git diff --cached --quiet); then\n" + refuse + "fi\n",
		"pre-push":   "#!/bin/sh\nif [ -n \"$(git status --porcelain --untracked-files=no)\" ]; then\n" + refuse + "fi\n",
	}
	for name, script := range scripts {
		if err := testenv.WriteExecutableFile(filepath.Join(hooks, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, clonePath, "config", "core.hooksPath", hooks)
	return ran
}

// The exact wb#899 wedge: a claim publish staged its claim and the
// machine's snapshot and never committed them. The next claim recovers —
// it discards that residue and lands its own claim — while an untracked file
// that is not wb's is left alone.
func TestClaimRecoversFromAPublishLeftStagedButUncommitted(t *testing.T) {
	origin := bareOrigin(t)
	p := storeMachine(t, origin)
	at := time.Date(2026, 10, 21, 7, 2, 0, 0, time.UTC)
	if _, err := p.Publish(context.Background(), snap("me", "mac", at)); err != nil {
		t.Fatal(err)
	}
	writeCloneFile(t, p.opts.ClonePath, ClaimPath("stranded"), "task: stranded\n")
	writeCloneFile(t, p.opts.ClonePath, SnapshotPath("me", "mac"), "half-written\n")
	gitIn(t, p.opts.ClonePath, "add", "claims", "machines")
	writeCloneFile(t, p.opts.ClonePath, "scratch-notes.txt", "untracked, not wb's\n")

	outcome, err := p.Claim(context.Background(), mkClaim("me", "mac", "next-task", at.Add(time.Hour)), remotestate.ClaimNormal, "")
	if err != nil {
		t.Fatalf("Claim after an interrupted publish = %v, want recovery", err)
	}
	if outcome.Kind != remotestate.ClaimAcquired {
		t.Fatalf("outcome = %+v, want acquired", outcome)
	}
	files := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main")
	if !strings.Contains(files, ClaimPath("next-task")) || strings.Contains(files, ClaimPath("stranded")) {
		t.Fatalf("origin files = %q, want next-task and not the stranded claim", files)
	}
	if status := gitIn(t, p.opts.ClonePath, "status", "--porcelain"); status != "?? scratch-notes.txt" {
		t.Fatalf("clone status = %q, want only the untouched untracked file", status)
	}
	if _, err := os.Stat(filepath.Join(p.opts.ClonePath, filepath.FromSlash(ClaimPath("stranded")))); !os.IsNotExist(err) {
		t.Fatalf("stranded claim file still on disk: %v", err)
	}
}

// The remote-store clone with WB-style guard hooks installed (core.hooksPath)
// still publishes claims, releases and snapshots: its hooks run for every
// commit, the provider never stages the shared index, and every push leaves
// from a clean clone (wb#899's root cause).
func TestClaimAndReleasePublishWithWBGuardHooksInstalled(t *testing.T) {
	origin := bareOrigin(t)
	p := storeMachine(t, origin)
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	if _, err := p.Publish(context.Background(), snap("me", "mac", at)); err != nil {
		t.Fatal(err)
	}
	ran := installWBStyleGuardHooks(t, p.opts.ClonePath)

	if _, err := p.Claim(context.Background(), mkClaim("me", "mac", "hooked", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatalf("Claim with guard hooks installed = %v", err)
	}
	if _, err := os.Stat(ran); err != nil {
		t.Fatalf("pre-commit hook did not run for the claim commit: %v", err)
	}
	if files := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main"); !strings.Contains(files, ClaimPath("hooked")) {
		t.Fatalf("origin files = %q, want the claim published", files)
	}
	if _, err := p.Release(context.Background(), "hooked", "me", "mac", false); err != nil {
		t.Fatalf("Release with guard hooks installed = %v", err)
	}
	if files := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main"); strings.Contains(files, ClaimPath("hooked")) {
		t.Fatalf("origin files = %q, want the claim released", files)
	}
	if _, err := p.Publish(context.Background(), snap("me", "mac", at.Add(time.Minute))); err != nil {
		t.Fatalf("Publish with guard hooks installed = %v", err)
	}
	if status := gitIn(t, p.opts.ClonePath, "status", "--porcelain"); status != "" {
		t.Fatalf("clone status = %q, want clean", status)
	}
}

// A tracked change outside wb-owned paths is someone's work: the sync is
// refused with a SyncError naming the path and the remedy, and the change
// is left exactly as it was.
func TestFetchRefusesAndKeepsChangesOutsideWBOwnedPaths(t *testing.T) {
	origin := bareOrigin(t)
	p := storeMachine(t, origin)
	if _, err := p.Publish(context.Background(), snap("me", "mac", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	writeCloneFile(t, p.opts.ClonePath, "notes.md", "committed\n")
	gitIn(t, p.opts.ClonePath, "add", "notes.md")
	gitIn(t, p.opts.ClonePath, "commit", "-q", "-m", "notes")
	writeCloneFile(t, p.opts.ClonePath, "notes.md", "someone's edit\n")
	writeCloneFile(t, p.opts.ClonePath, ClaimPath("residue"), "task: residue\n")
	gitIn(t, p.opts.ClonePath, "add", "claims")

	err := p.Fetch(context.Background())
	var syncErr *remotestate.SyncError
	if !errors.As(err, &syncErr) || syncErr.Store != p.opts.ClonePath {
		t.Fatalf("Fetch = %v, want a SyncError for the clone", err)
	}
	for _, want := range []string{"notes.md", "git -C " + p.opts.ClonePath + " status", "will not discard"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Fetch error %q does not mention %q", err, want)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(p.opts.ClonePath, "notes.md")); string(data) != "someone's edit\n" {
		t.Fatalf("notes.md = %q, want the edit kept", data)
	}
	if staged := gitIn(t, p.opts.ClonePath, "diff", "--cached", "--name-only"); staged != ClaimPath("residue") {
		t.Fatalf("staged = %q, want the owned residue untouched too while refusing", staged)
	}
}

// A clone whose state cannot be read, or whose wb-owned residue cannot be
// discarded, reports a SyncError instead of syncing on top of it.
func TestFetchReportsUnhealableCloneAsSyncError(t *testing.T) {
	t.Run("unreadable index", func(t *testing.T) {
		origin := bareOrigin(t)
		p := storeMachine(t, origin)
		if _, err := p.Publish(context.Background(), snap("me", "mac", time.Now().UTC())); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p.opts.ClonePath, ".git", "index"), []byte("not an index"), 0o644); err != nil {
			t.Fatal(err)
		}
		var syncErr *remotestate.SyncError
		if err := p.Fetch(context.Background()); !errors.As(err, &syncErr) {
			t.Fatalf("Fetch = %v, want a SyncError", err)
		}
	})
	t.Run("undeletable residue", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("needs POSIX directory permissions enforced")
		}
		origin := bareOrigin(t)
		p := storeMachine(t, origin)
		if _, err := p.Publish(context.Background(), snap("me", "mac", time.Now().UTC())); err != nil {
			t.Fatal(err)
		}
		writeCloneFile(t, p.opts.ClonePath, ClaimPath("stuck"), "task: stuck\n")
		gitIn(t, p.opts.ClonePath, "add", "claims")
		claims := filepath.Join(p.opts.ClonePath, "claims")
		if err := os.Chmod(claims, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(claims, 0o755) })
		err := p.Fetch(context.Background())
		var syncErr *remotestate.SyncError
		if !errors.As(err, &syncErr) || !strings.Contains(err.Error(), "discard uncommitted wb-owned files") {
			t.Fatalf("Fetch = %v, want a discard SyncError", err)
		}
	})
}

// A pull that keeps failing is a SyncError naming the clone and the remedy.
func TestFetchReportsPersistentPullFailureAsSyncError(t *testing.T) {
	origin := bareOrigin(t)
	p := storeMachine(t, origin)
	if _, err := p.Publish(context.Background(), snap("me", "mac", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(origin, origin+".away"); err != nil {
		t.Fatal(err)
	}
	p.opts.Sleep = func(time.Duration) {}
	err := p.Fetch(context.Background())
	var syncErr *remotestate.SyncError
	if !errors.As(err, &syncErr) || !strings.Contains(err.Error(), "wb remote status") {
		t.Fatalf("Fetch = %v, want a SyncError with the retry remedy", err)
	}
}

func TestOwnedPathCoversOnlyWhatTheProviderWrites(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]bool{
		"claims/a.yaml":                 true,
		"machines/me/mac/snapshot.yaml": true,
		"README.md":                     true,
		".ingitdb/settings.yaml":        false,
		"sync-reports/$records/x.md":    false,
		"README.md.bak":                 false,
		"claimsx/a.yaml":                false,
		"notes.md":                      false,
		"docs/README.md":                false,
	} {
		if got := ownedPath(path); got != want {
			t.Errorf("ownedPath(%q) = %v, want %v", path, got, want)
		}
	}
}

// Healing belongs to the remote store alone. A provider over a user's own
// repository (`wb sync-report publish` into a workbench clone) leaves their
// uncommitted README edit and staged .ingitdb file exactly as they were and
// refuses to sync on top of them, as before wb#899's fix.
func TestFetchOutsideTheRemoteStoreKeepsUserChanges(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("me", "mac", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	writeCloneFile(t, p.opts.ClonePath, "README.md", "# my workbench\nhours of unsaved notes\n")
	writeCloneFile(t, p.opts.ClonePath, ".ingitdb/settings.yaml", "user: setting\n")
	gitIn(t, p.opts.ClonePath, "add", ".ingitdb")
	p.opts.Sleep = func(time.Duration) {}

	if err := p.Fetch(context.Background()); err == nil {
		t.Fatal("Fetch over uncommitted user changes succeeded, want the refusal")
	}
	if data, _ := os.ReadFile(filepath.Join(p.opts.ClonePath, "README.md")); string(data) != "# my workbench\nhours of unsaved notes\n" {
		t.Fatalf("README.md = %q, want the user's edit kept", data)
	}
	if staged := gitIn(t, p.opts.ClonePath, "diff", "--cached", "--name-only"); staged != ".ingitdb/settings.yaml" {
		t.Fatalf("staged = %q, want the user's staged file kept", staged)
	}
}
