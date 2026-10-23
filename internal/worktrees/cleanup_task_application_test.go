package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	unixcompat "github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestCleanupTaskApplicationPreflightsAllMembersBeforeSealing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		want string
	}{
		{name: "success"},
		{name: "second preflight", want: "preflight denied"},
		{name: "artifact preparation", want: "artifact denied"},
		{name: "member apply", want: "member denied"},
		{name: "artifact archive", want: "archive denied"},
		{name: "opened archive"},
		{name: "ineligible"},
		{name: "pending backlog", want: "member denied"},
		{name: "inventory logical root"},
		{name: "recovered ineligible"},
		{name: "recovered lock changed", want: "lock"},
		{name: "recovered member failure", want: "member denied"},
		{name: "recovered success"},
		{name: "recovered quarantine failure", want: "quarantine recovered cleanup lock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			store := filepath.Join(root, "store")
			if tc.name == "inventory logical root" {
				store = filepath.Join(root, "home", "worktrees")
			}
			if err := os.MkdirAll(store, 0o700); err != nil {
				t.Fatal(err)
			}
			run := &cleanupRun{
				ctx:        context.Background(),
				normalized: CleanupOptions{ProjectsRoot: root},
				resolution: wbhome.Resolution{Write: wbhome.Layout{Home: filepath.Join(root, "home"), WorktreesRoot: store}},
				outcome: CleanupOutcome{Results: []CleanupResult{
					{ListResult: ListResult{Task: "task", Repository: "acme/app", WorktreesRoot: store}, Eligible: true},
					{ListResult: ListResult{Task: "task", Repository: "acme/other", WorktreesRoot: store}, Eligible: true},
				}},
			}
			entry := cleanupApplyEntry{selection: cleanupTaskSelection{WorktreesRoot: store, Task: "task"}, resultIndices: []int{0, 1}, canApply: true, hasEligibleWorktree: true}
			if tc.name == "ineligible" {
				entry.canApply = false
			}
			if tc.name == "recovered ineligible" {
				entry.hasEligibleWorktree = false
			}
			if strings.HasPrefix(tc.name, "recovered") {
				recovered, err := acquireCleanupTaskAtOrCreate(store, "task")
				if err != nil {
					t.Fatal(err)
				}
				run.recoveredTask = recovered
				run.recovery = &InterruptedLockRecovery{WorktreesRoot: store, Task: "task", Path: filepath.Join(store, "task", ".lock")}
				if tc.name == "recovered lock changed" {
					recovered.lock.identity.inode++
				}
				if tc.name == "recovered ineligible" {
					defer func() { _ = recovered.lock.release(); recovered.close() }()
				}
				if tc.name == "recovered quarantine failure" {
					run.normalized.beforeRecoveredLockQuarantine = func(string) {
						if err := unixcompat.Unlinkat(int(recovered.lock.directory.Fd()), ".lock", 0); err != nil {
							t.Errorf("remove test lock: %v", err)
						}
					}
				}
			}
			var steps []string
			ports := cleanupTaskApplicationPorts{
				Inventory: func(context.Context, ListOptions) (ListOutcome, error) { return ListOutcome{}, nil },
				Preflight: func(_ context.Context, _ CleanupOptions, _ time.Time, _ *cleanupTaskHandle, entry CleanupResult, _ string) (ListResult, error) {
					steps = append(steps, "preflight "+entry.Repository)
					if tc.name == "second preflight" && entry.Repository == "acme/other" {
						return ListResult{}, errors.New("preflight denied")
					}
					return entry.ListResult, nil
				},
				PrepareArtifacts: func(_ string, _ *cleanupTaskHandle, _ []int, _ []LifecycleArtifact) (*os.File, string, []cleanupLifecycleArtifactHandle, error) {
					steps = append(steps, "prepare artifacts")
					if tc.name == "artifact preparation" {
						return nil, "", nil, errors.New("artifact denied")
					}
					if tc.name == "opened archive" {
						file, err := os.CreateTemp(t.TempDir(), "archive")
						return file, file.Name(), nil, err
					}
					return nil, "", nil, nil
				},
				CloseArtifacts: func([]cleanupLifecycleArtifactHandle) { steps = append(steps, "close artifacts") },
				ApplyMember: func(_ *cleanupRun, _ *cleanupTaskHandle, index int, _ *remoteBranchDeletionGate, _ bool, pending *int) error {
					steps = append(steps, "apply "+run.outcome.Results[index].Repository)
					if tc.name == "pending backlog" {
						*pending = 1
						return errors.New("member denied")
					}
					if tc.name == "member apply" || tc.name == "recovered member failure" {
						return errors.New("member denied")
					}
					return nil
				},
				ArchiveArtifacts: func(*cleanupTaskHandle, *os.File, string, []cleanupLifecycleArtifactHandle, []LifecycleArtifact) error {
					steps = append(steps, "archive artifacts")
					if tc.name == "artifact archive" {
						return errors.New("archive denied")
					}
					return nil
				},
			}
			err := run.applyCleanupTaskWithPorts(entry, nil, ports)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("task apply: %v", err)
				}
				if tc.name == "recovered success" && (run.recovery == nil || !run.recovery.Applied) {
					t.Fatal("recovered lock was not quarantined")
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("task apply error %v, want %q", err, tc.want)
			}
			for index, step := range steps {
				if strings.HasPrefix(step, "apply ") && index < 3 {
					t.Fatalf("member mutated before all preflights and artifact preparation: %q", steps)
				}
			}
		})
	}
}
