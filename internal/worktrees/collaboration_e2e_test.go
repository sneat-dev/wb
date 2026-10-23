//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktreecollab"
)

//nolint:paralleltest // newGitFixture configures process-wide Git and WB environment.
func TestE2ECollaborationCheckoutBindsPathAndIDToOneLinkedGitWorktree(t *testing.T) {
	fixture := newGitFixture(t)
	worktree := filepath.Join(t.TempDir(), "linked")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "collab", worktree, "main")
	ctx := context.Background()
	checkout, err := ResolveCollaborationCheckout(ctx, fixture.projectsRoot, worktree)
	if err != nil || !strings.HasPrefix(checkout.ID, "wt-") || checkout.Root == "" {
		t.Fatalf("linked checkout = %+v, %v", checkout, err)
	}
	if _, err := ResolveCollaborationCheckout(ctx, fixture.projectsRoot, fixture.canonical); err == nil || !strings.Contains(err.Error(), "linked worktree") {
		t.Fatalf("canonical clone accepted: %v", err)
	}
	if beforeOptIn, err := ResolveCollaborationCheckout(ctx, fixture.projectsRoot, checkout.ID); err != nil || beforeOptIn != checkout {
		t.Fatalf("ID from read-only info cannot select initial takeover: %+v, %v", beforeOptIn, err)
	}
	store := worktreecollab.NewStore(fixture.home)
	_, err = store.WithLocked(ctx, checkout, func(state *worktreecollab.State, found bool) error {
		if found {
			t.Fatal("unexpected existing coordination state")
		}
		return state.Take(worktreecollab.TakeRequest{Caller: "owner", ExpectedOwner: worktreecollab.NoOwner, At: time.Now()})
	})
	if err != nil {
		t.Fatal(err)
	}
	byID, err := ResolveCollaborationCheckout(ctx, fixture.projectsRoot, checkout.ID)
	if err != nil || byID != checkout {
		t.Fatalf("ID resolved %+v, %v; want %+v", byID, err, checkout)
	}
	if _, err := ResolveCollaborationCheckout(ctx, fixture.projectsRoot, "wt-"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("unknown coordination ID resolved")
	}
	if _, err := ResolveCollaborationCheckout(ctx, fixture.projectsRoot, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing checkout resolved")
	}
}

//nolint:paralleltest // newGitFixture configures process-wide Git and WB environment.
func TestE2ECollaborationLegacyOwnerUsesLockedJournalAndExactSession(t *testing.T) {
	fixture := newGitFixture(t)
	worktree := filepath.Join(t.TempDir(), "linked")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "collab", worktree, "main")
	sessions := filepath.Join(fixture.home, session.DirName)
	record, err := session.Register(sessions, session.Record{PID: os.Getpid(), Runtime: "codex", NativeHarnessID: "test-agent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recordOwner(worktree, "task", "codex/test-agent", "model", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	observed, err := ObserveCollaborationLegacyOwner(worktree, sessions)
	readOnly, readErr := ObserveCollaborationLegacyOwnerReadOnly(worktree, sessions)
	if readErr != nil || readOnly != observed {
		t.Fatalf("read-only held journal observation = %+v, %v; locked = %+v", readOnly, readErr, observed)
	}
	if err != nil || observed.ID == "" || observed.SessionID != record.WBSessionID || observed.Status != "live" {
		t.Fatalf("legacy registered owner = %+v, %v", observed, err)
	}
	if _, err := ObserveCollaborationLegacyOwner(worktree, filepath.Join(t.TempDir(), "missing-sessions")); err != nil {
		t.Fatalf("unresolvable registration should be opaque, not absent: %v", err)
	}
	if _, err := recordOwner(worktree, "task", "gone-agent", "model", 1<<30); err != nil {
		t.Fatal(err)
	}
	observed, err = ObserveCollaborationLegacyOwner(worktree, sessions)
	if err != nil || observed.ID == "" || observed.Status != "inactive" || observed.SessionID != "" {
		t.Fatalf("gone owner = %+v, %v", observed, err)
	}
}

//nolint:paralleltest // newGitFixture configures process-wide Git and WB environment.
func TestE2ECollaborationLegacyInspectionDoesNotCreateJournal(t *testing.T) {
	fixture := newGitFixture(t)
	worktree := filepath.Join(t.TempDir(), "read-only-linked")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "read-only-collab", worktree, "main")
	exclude := strings.TrimSpace(gitTestOutput(t, worktree, "rev-parse", "--git-path", "info/exclude"))
	if err := os.Remove(exclude); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	journal := filepath.Join(worktree, ".wb", "local", "worklog")
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatalf("journal existed before inspection: %v", err)
	}
	observed, err := ObserveCollaborationLegacyOwnerReadOnly(worktree, filepath.Join(fixture.home, session.DirName))
	if err != nil || observed.ID != "" {
		t.Fatalf("empty observation = %+v, %v", observed, err)
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatalf("inspection created journal: %v", err)
	}
	if _, err := os.Stat(exclude); !os.IsNotExist(err) {
		t.Fatalf("inspection created Git exclude: %v", err)
	}
}
