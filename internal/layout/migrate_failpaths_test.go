package layout

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestApplyOneCloneReportsEachPreMoveFailure(t *testing.T) {
	t.Parallel()
	boom := errors.New("injected clone move failure")
	for _, step := range []struct{ name, status, reason string }{
		{"plan", "failed", "re-verify"},
		{"refuse", "skipped", "refused immediately before move"},
		{"intent", "failed", boom.Error()},
		{"apply", "failed", boom.Error()},
		{"receipt", "done", "receipt failed"},
	} {
		t.Run(step.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			clone := &MigrateClone{Repository: "acme/repo", Source: filepath.Join(root, "acme", "repo"), Destination: filepath.Join(root, "github.com", "acme", "repo")}
			plan := worktrees.CloneMoveResult{Worktrees: []worktrees.CloneMoveWorktree{{Source: clone.Source, Destination: clone.Destination}}}
			deps := cloneMoveDeps{
				plan: func(context.Context, string, string) (worktrees.CloneMoveResult, error) { return plan, nil },
				refuse: func(context.Context, string, string, string, []string, migrateInclude) (string, []string) {
					return "", []string{"included-task"}
				},
				intent: func(string, []worktrees.CloneMoveWorktree, time.Time) (func(time.Time) error, error) {
					return func(time.Time) error { return nil }, nil
				},
				apply: func(context.Context, string, string) (worktrees.CloneMoveResult, error) { return plan, nil },
			}
			switch step.name {
			case "plan":
				deps.plan = func(context.Context, string, string) (worktrees.CloneMoveResult, error) {
					return worktrees.CloneMoveResult{}, boom
				}
			case "refuse":
				deps.refuse = func(context.Context, string, string, string, []string, migrateInclude) (string, []string) {
					return "changed Git state", nil
				}
			case "intent":
				deps.intent = func(string, []worktrees.CloneMoveWorktree, time.Time) (func(time.Time) error, error) {
					return nil, boom
				}
			case "apply":
				deps.apply = func(context.Context, string, string) (worktrees.CloneMoveResult, error) {
					return worktrees.CloneMoveResult{}, boom
				}
			case "receipt":
				deps.intent = func(string, []worktrees.CloneMoveWorktree, time.Time) (func(time.Time) error, error) {
					return func(time.Time) error { return boom }, nil
				}
			}
			applyOneCloneWithDeps(context.Background(), root, clone, time.Now(), migrateInclude{}, deps)
			if clone.Status != step.status || !strings.Contains(clone.Reason, step.reason) {
				t.Fatalf("%s outcome = %+v", step.name, clone)
			}
			if step.name == "receipt" && len(clone.IncludedTasks) != 1 {
				t.Fatalf("completed move lost included task: %+v", clone)
			}
		})
	}
}

func TestLayoutErrorBranchesKeepSafetyFailuresVisible(t *testing.T) {
	t.Parallel()
	if !CleanFailed(CleanReport{Actions: []CleanAction{{Status: "error"}}}) {
		t.Fatal("cleanup error was ignored")
	}
	root := t.TempDir()
	if _, err := Migrate(context.Background(), filepath.Join(root, "missing"), MigrateOptions{}); err == nil {
		t.Fatal("migration accepted a missing projects root")
	}
	findings := inspectHost(context.Background(), root, filepath.Join(root, "missing"), "example.com")
	if len(findings) != 1 || findings[0].Kind != KindUnreadable {
		t.Fatalf("unreadable host findings = %+v", findings)
	}
	owner := filepath.Join(root, "owner")
	if err := os.Mkdir(owner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owner, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(owner, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := inspectLegacyOwner(context.Background(), root, owner, "owner"); len(got) != 0 {
		t.Fatalf("non-checkout owner children became findings: %+v", got)
	}
	regenerateMarkers(root, filepath.Join(root, "missing"))
}

func TestRefuseCloneReportsUninspectableLinkedWorktree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	clone := initRemoteClone(t, root, "acme", "repo", "acme/repo")
	reason, _ := refuseClone(context.Background(), root, "acme/repo", clone, []string{filepath.Join(root, "missing-worktree")}, migrateInclude{})
	if !strings.Contains(reason, "linked worktree") || !strings.Contains(reason, "cannot inspect Git state") {
		t.Fatalf("linked worktree refusal = %q", reason)
	}
}

func TestRelocateOneCheckoutClassifiesDependencyOutcomes(t *testing.T) {
	t.Parallel()
	boom := errors.New("injected relocation failure")
	for _, step := range []struct{ name, status string }{
		{"identify", "skipped"}, {"path", "skipped"}, {"same", ""},
		{"relocate", "failed"}, {"ineligible", "skipped"},
		{"dry-run", "planned"}, {"applied", "done"}, {"incomplete", "failed"},
	} {
		t.Run(step.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			checkout := filepath.Join(root, "old")
			destination := filepath.Join(root, "new")
			deps := checkoutRelocateDeps{
				identify: func(string, string) (string, bool, bool, error) { return "task", false, true, nil },
				path:     func(worktrees.WorktreePlacement, string, string) (string, error) { return destination, nil },
				relocate: func(context.Context, worktrees.RelocateCheckoutOptions) (worktrees.RelocateCheckoutResult, error) {
					return worktrees.RelocateCheckoutResult{Eligible: true, Applied: true}, nil
				},
			}
			apply := true
			switch step.name {
			case "identify":
				deps.identify = func(string, string) (string, bool, bool, error) { return "", false, false, boom }
			case "path":
				deps.path = func(worktrees.WorktreePlacement, string, string) (string, error) { return "", boom }
			case "same":
				deps.path = func(worktrees.WorktreePlacement, string, string) (string, error) { return checkout, nil }
			case "relocate":
				deps.relocate = func(context.Context, worktrees.RelocateCheckoutOptions) (worktrees.RelocateCheckoutResult, error) {
					return worktrees.RelocateCheckoutResult{}, boom
				}
			case "ineligible":
				deps.relocate = func(context.Context, worktrees.RelocateCheckoutOptions) (worktrees.RelocateCheckoutResult, error) {
					return worktrees.RelocateCheckoutResult{Reason: "unsafe"}, nil
				}
			case "dry-run":
				apply = false
			case "incomplete":
				deps.relocate = func(context.Context, worktrees.RelocateCheckoutOptions) (worktrees.RelocateCheckoutResult, error) {
					return worktrees.RelocateCheckoutResult{Eligible: true}, nil
				}
			}
			got := relocateOneCheckoutWithDeps(context.Background(), root, root, "acme/repo", checkout, worktrees.WorktreePlacement{}, apply, time.Now(), migrateInclude{}, deps)
			if got.Status != step.status {
				t.Fatalf("%s relocation = %+v", step.name, got)
			}
			if step.name == "same" && got != (MigrateRelocation{}) {
				t.Fatalf("already placed checkout should produce no record: %+v", got)
			}
		})
	}
}

func TestRelocateClonesSkipsUnplaceableAndUnchangedCheckouts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	newClone := func() []MigrateClone {
		return []MigrateClone{{Repository: "acme/repo", Status: "done", Destination: filepath.Join(root, "github.com", "acme", "repo"), Worktrees: []MigrateWorktree{{Source: "old", Destination: "new"}}}}
	}
	resolve := func(string, string) (worktrees.WorktreePlacement, error) { return worktrees.WorktreePlacement{}, nil }
	unchanged := func(context.Context, string, string, string, string, worktrees.WorktreePlacement, bool, time.Time, migrateInclude) MigrateRelocation {
		return MigrateRelocation{}
	}
	clones := newClone()
	if err := relocateClonesWithDeps(context.Background(), root, clones, false, time.Now(), nil, migrateInclude{}, func(string, string) (worktrees.WorktreePlacement, error) {
		return worktrees.WorktreePlacement{}, errors.New("placement unavailable")
	}, unchanged); err != nil || len(clones[0].Relocations) != 0 {
		t.Fatalf("unplaceable clone = %+v, %v", clones, err)
	}
	clones = newClone()
	if err := relocateClonesWithDeps(context.Background(), root, clones, false, time.Now(), nil, migrateInclude{}, resolve, unchanged); err != nil || len(clones[0].Relocations) != 0 {
		t.Fatalf("unchanged checkout = %+v, %v", clones, err)
	}
	clones = newClone()
	boom := errors.New("persist failed")
	changed := func(context.Context, string, string, string, string, worktrees.WorktreePlacement, bool, time.Time, migrateInclude) MigrateRelocation {
		return MigrateRelocation{Task: "task", Status: "planned"}
	}
	if err := relocateClonesWithDeps(context.Background(), root, clones, false, time.Now(), func(*MigrateClone) error { return boom }, migrateInclude{}, resolve, changed); !errors.Is(err, boom) || len(clones[0].Relocations) != 1 {
		t.Fatalf("persist failure = %+v, %v", clones, err)
	}
}

func TestUndoRecordsDependencyFailuresWithoutLosingItsManifest(t *testing.T) {
	t.Parallel()
	boom := errors.New("injected undo failure")
	for _, step := range []struct {
		name, wantStatus string
		wantError        bool
	}{
		{"lock", "", true},
		{"reverse", "skipped", false},
		{"reverse-write", "skipped", true},
		{"relocation-write", "", true},
		{"refuse", "skipped", false},
		{"refuse-write", "skipped", true},
		{"move", "failed", false},
		{"move-write", "failed", true},
		{"success-write", "reversed", true},
	} {
		t.Run(step.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			id := "undo-fault"
			path := filepath.Join(root, ".wb", migrationsDirName, id, "manifest.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			entry := manifestClone{Repository: "acme/repo", Source: filepath.Join(root, "acme", "repo"), Destination: filepath.Join(root, "github.com", "acme", "repo"), Status: "done"}
			if strings.HasPrefix(step.name, "reverse") || step.name == "relocation-write" {
				entry.Relocations = []manifestRelocation{{Task: "task", Source: filepath.Join(root, "old"), Destination: filepath.Join(root, "new"), Status: "done"}}
			}
			if err := writeManifest(path, &migrationManifest{Clones: []manifestClone{entry}}); err != nil {
				t.Fatal(err)
			}
			deps := undoDeps{
				lock: func(string) (*migrationLock, error) { return &migrationLock{}, nil },
				refuse: func(context.Context, string, string, string, []string, migrateInclude) (string, []string) {
					return "", nil
				},
				reverse: func(context.Context, string, string, string, string, time.Time) error { return nil },
				apply: func(context.Context, string, string) (worktrees.CloneMoveResult, error) {
					return worktrees.CloneMoveResult{}, nil
				},
				write: writeManifest,
			}
			switch step.name {
			case "lock":
				deps.lock = func(string) (*migrationLock, error) { return nil, boom }
			case "reverse", "reverse-write":
				deps.reverse = func(context.Context, string, string, string, string, time.Time) error { return boom }
			case "refuse", "refuse-write":
				deps.refuse = func(context.Context, string, string, string, []string, migrateInclude) (string, []string) {
					return "Git changed", nil
				}
			case "move", "move-write":
				deps.apply = func(context.Context, string, string) (worktrees.CloneMoveResult, error) {
					return worktrees.CloneMoveResult{}, boom
				}
			}
			if strings.HasSuffix(step.name, "-write") {
				deps.write = func(string, *migrationManifest) error { return boom }
			}
			report, err := migrateUndoWithDeps(context.Background(), root, MigrateOptions{UndoID: id, Apply: true, Now: time.Now}, deps)
			if step.wantError && !errors.Is(err, boom) {
				t.Fatalf("%s error = %v, want injected error", step.name, err)
			}
			if !step.wantError && err != nil {
				t.Fatalf("%s unexpected error: %v", step.name, err)
			}
			if step.wantStatus != "" && (len(report.Clones) != 1 || report.Clones[0].Status != step.wantStatus) {
				t.Fatalf("%s report = %+v", step.name, report.Clones)
			}
			if _, err := readManifest(path); err != nil {
				t.Fatalf("%s left unreadable manifest: %v", step.name, err)
			}
		})
	}
}
