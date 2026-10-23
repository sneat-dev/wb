package layout

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/repopath"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestMigrateApplyRetainsReadAndReconcileFailures(t *testing.T) {
	t.Parallel()
	boom := errors.New("injected reconciliation failure")
	for _, mode := range []string{"host-read", "reconcile", "legacy-read"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			hostOwner := repopath.Owner{Host: "github.com", Name: "acme", Path: filepath.Join(root, "github.com", "acme")}
			legacyOwner := repopath.Owner{Name: "acme", Path: filepath.Join(root, "acme")}
			owner := hostOwner
			if mode == "legacy-read" {
				owner = legacyOwner
			}
			repoPath := filepath.Join(owner.Path, "repo")
			if err := os.MkdirAll(filepath.Join(repoPath, ".git"), 0755); err != nil {
				t.Fatal(err)
			}
			deps := defaultMigrateApplyDeps()
			deps.owners = func(string) ([]repopath.Owner, []string) { return []repopath.Owner{owner}, nil }
			if mode != "reconcile" {
				deps.readDir = func(string) ([]os.DirEntry, error) { return nil, boom }
			}
			deps.reconcile = func(context.Context, string, string, bool) (string, []string, error) { return "", nil, boom }
			report, err := migrateApplyWithDeps(context.Background(), root, MigrateOptions{Now: time.Now, ClonesOnly: true}, deps)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "reconcile" {
				if len(report.Clones) != 1 || report.Clones[0].Status != "failed" || !strings.Contains(report.Clones[0].Reason, boom.Error()) {
					t.Fatalf("reconcile report: %+v", report.Clones)
				}
			} else if len(report.Clones) != 0 {
				t.Fatalf("unreadable owner added clones: %+v", report.Clones)
			}
		})
	}
}

func TestMigrateApplyManifestFailuresPreserveOutcome(t *testing.T) {
	t.Parallel()
	boom := errors.New("injected manifest failure")
	for _, mode := range []string{"create", "clone-write", "relocation-write", "relocation-after-clone"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			source := filepath.Join(root, "acme", "repo")
			if err := os.MkdirAll(filepath.Join(source, ".git"), 0755); err != nil {
				t.Fatal(err)
			}
			deps := defaultMigrateApplyDeps()
			if mode == "relocation-write" {
				deps.owners = func(string) ([]repopath.Owner, []string) { return nil, nil }
			} else {
				deps.plan = func(context.Context, string, string, string, string, migrateInclude) MigrateClone {
					return MigrateClone{Repository: "acme/repo", Source: source, Destination: filepath.Join(root, "github.com", "acme", "repo"), Status: "planned"}
				}
				deps.applyClone = func(_ context.Context, _ string, clone *MigrateClone, _ time.Time, _ migrateInclude) {
					clone.Status = "done"
				}
			}
			switch mode {
			case "create":
				deps.createManifest = func(string, []MigrateClone, time.Time) (*migrationManifest, string, error) { return nil, "", boom }
			case "clone-write", "relocation-write":
				deps.writeManifest = func(string, *migrationManifest) error { return boom }
			}
			switch mode {
			case "relocation-write":
				deps.relocate = func(_ context.Context, _ string, _ []MigrateClone, _ bool, _ time.Time, persist func(*MigrateClone) error, _ migrateInclude) error {
					clone := &MigrateClone{Repository: "acme/repo"}
					return persist(clone)
				}
			case "relocation-after-clone":
				deps.relocate = func(context.Context, string, []MigrateClone, bool, time.Time, func(*MigrateClone) error, migrateInclude) error {
					return boom
				}
			}
			report, err := migrateApplyWithDeps(context.Background(), root, MigrateOptions{Apply: true, Now: time.Now}, deps)
			if !errors.Is(err, boom) {
				t.Fatalf("%s error: %v", mode, err)
			}
			if mode != "create" {
				if report.ManifestID == "" || report.ManifestPath == "" {
					t.Fatalf("%s lost manifest: %+v", mode, report)
				}
			}
			if mode == "clone-write" && (len(report.Clones) != 1 || report.Clones[0].Status != "done") {
				t.Fatalf("lost moved clone outcome: %+v", report.Clones)
			}
		})
	}
}

func TestWBHomeFailuresLeaveLayoutStateUntouched(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	loop := filepath.Join(root, "loop")
	if err := os.Symlink("loop", loop); err != nil {
		t.Fatal(err)
	}
	invalidateLocalIndex(loop)
	if daemonRunning(loop) {
		t.Fatal("looping WB root reported a ready daemon")
	}
	_, err := defaultCloneMoveDeps().intent(loop, []worktrees.CloneMoveWorktree{{Source: "source", Destination: "destination"}}, time.Now())
	if err == nil {
		t.Fatal("clone move intent accepted looping WB root")
	}
}
