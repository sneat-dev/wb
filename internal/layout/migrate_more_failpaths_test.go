package layout

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/repopath"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestLayoutInjectedParsingAndPathFailures(t *testing.T) {
	t.Parallel()
	boom := errors.New("injected failure")
	if got := inspectTopLevelWithOrigin(context.Background(), t.TempDir(), "path", "name", func(context.Context, string) (repopath.Address, error) {
		return repopath.Address{Host: "github.com", Org: "..", Repo: "repo"}, nil
	}); len(got) != 1 || got[0].Kind != KindUnreadable {
		t.Fatalf("unsafe origin: %+v", got)
	}
	if _, err := absoluteRootWithAbs("root", func(string) (string, error) { return "", boom }); !errors.Is(err, boom) {
		t.Fatalf("absolute root: %v", err)
	}
	if err := removeContainedPathWithAbs("root", "target", func(string) (string, error) { return "", boom }); !errors.Is(err, boom) {
		t.Fatalf("root abs: %v", err)
	}
	calls := 0
	if err := removeContainedPathWithAbs("root", "target", func(s string) (string, error) {
		calls++
		if calls == 2 {
			return "", boom
		}
		return s, nil
	}); !errors.Is(err, boom) {
		t.Fatalf("target abs: %v", err)
	}
	if _, err := originAddressFromRaw("bad", func(string) (gitremote.Remote, error) { return gitremote.Remote{}, boom }); !errors.Is(err, boom) {
		t.Fatalf("parse: %v", err)
	}
	if _, err := originAddressFromRaw("bad", func(string) (gitremote.Remote, error) { return gitremote.Remote{}, nil }); err == nil || !strings.Contains(err.Error(), "owner/repository") {
		t.Fatalf("missing repository: %v", err)
	}
	// A transport-valid single-label host is not a literal forge hostname.
	portRemote, err := gitremote.Parse("https://localhost/acme/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := originAddressFromRaw(portRemote.Raw, func(string) (gitremote.Remote, error) { return portRemote, nil }); err == nil || !strings.Contains(err.Error(), "forge hostname") {
		t.Fatalf("invalid forge host: %v", err)
	}
}

func TestMigrationInjectedClaimPlanAndLockFailures(t *testing.T) {
	t.Parallel()
	boom := errors.New("injected failure")
	failClaims := func(string, string) ([]worktrees.ActiveClaimSummary, error) { return nil, boom }
	if _, err := resolveMigrateIncludeWithClaims(t.TempDir(), MigrateOptions{IncludeTasks: []string{"task"}}, failClaims); !errors.Is(err, boom) {
		t.Fatalf("include claims: %v", err)
	}
	if reason, _ := liveClaimReasonWithList(t.TempDir(), "acme/repo", migrateInclude{}, failClaims); reason != "" {
		t.Fatalf("claim fallback: %q", reason)
	}
	root := t.TempDir()
	origin := func(context.Context, string) (repopath.Address, error) {
		return repopath.Address{Org: "acme", Repo: "repo"}, nil
	}
	plan := func(context.Context, string, string) (worktrees.CloneMoveResult, error) {
		return worktrees.CloneMoveResult{}, boom
	}
	clone := planLegacyCloneWithDeps(context.Background(), root, "clone", "acme", "repo", migrateInclude{}, origin, plan)
	if clone.Status != "skipped" || !strings.Contains(clone.Reason, "forge host") {
		t.Fatalf("local origin: %+v", clone)
	}
	origin = func(context.Context, string) (repopath.Address, error) {
		return repopath.Address{Host: "github.com", Org: "acme", Repo: "repo"}, nil
	}
	clone = planLegacyCloneWithDeps(context.Background(), root, "clone", "acme", "repo", migrateInclude{}, origin, plan)
	if clone.Status != "skipped" || !strings.Contains(clone.Reason, "cannot enumerate") {
		t.Fatalf("move plan: %+v", clone)
	}
	gitOperation := func(context.Context, string) (string, error) { return "", nil }
	claimReason := func(string, string, migrateInclude) (string, []string) { return "", nil }
	if reason, _ := refuseCloneWithDeps(context.Background(), root, "acme/repo", "clone", []string{"linked"}, migrateInclude{}, func(_ context.Context, path string) (string, error) {
		if path == "linked" {
			return "rebase in progress", nil
		}
		return "", nil
	}, claimReason, false, nil); !strings.Contains(reason, "rebase in progress") {
		t.Fatalf("linked operation: %q", reason)
	}
	if reason, _ := refuseCloneWithDeps(context.Background(), root, "acme/repo", "clone", nil, migrateInclude{}, gitOperation, claimReason, true, func([]string) string { return "busy" }); reason != "busy" {
		t.Fatalf("busy process: %q", reason)
	}
	if reason, _ := refuseCloneWithDeps(context.Background(), root, "acme/repo", "clone", nil, migrateInclude{}, gitOperation, claimReason, true, func([]string) string { return "" }); reason != "" {
		t.Fatalf("idle process: %q", reason)
	}
	if _, err := acquireMigrationLockWithFlock(root, func(int, int) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("flock: %v", err)
	}
	file := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireMigrationLock(file); err == nil {
		t.Fatal("accepted a file as projects root")
	}
}

func TestMigrationManifestAndCleanupInjectedFailures(t *testing.T) {
	t.Parallel()
	boom := errors.New("injected failure")
	root := t.TempDir()
	if _, _, err := createMigrationManifestWithDeps(root, nil, time.Now(), func(time.Time) (string, error) { return "", boom }, writeManifest); !errors.Is(err, boom) {
		t.Fatalf("manifest ID: %v", err)
	}
	if _, _, err := createMigrationManifestWithDeps(root, nil, time.Now(), func(time.Time) (string, error) { return "valid-id", nil }, func(string, *migrationManifest) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("manifest write: %v", err)
	}
	if _, err := newMigrationIDWithRead(time.Now(), func([]byte) (int, error) { return 0, boom }); !errors.Is(err, boom) {
		t.Fatalf("random ID: %v", err)
	}
	owner := filepath.Join(root, "empty-owner")
	if err := os.Mkdir(owner, 0755); err != nil {
		t.Fatal(err)
	}
	if removed, reason := removeEmptyLegacyOwnerWithRemove(owner, func(string) error { return boom }); removed || !strings.Contains(reason, "could not remove") {
		t.Fatalf("owner removal: %v %q", removed, reason)
	}
	invalidateLocalIndex("")
	if daemonRunning("") {
		t.Fatal("daemon unexpectedly running")
	}
}
