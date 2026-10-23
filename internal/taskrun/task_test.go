package taskrun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/secretscan"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"github.com/sneat-dev/wb/internal/taskoffload"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func fixture(t *testing.T) (Dependencies, taskoffload.Store) {
	t.Helper()
	store := taskoffload.NewStore(t.TempDir())
	deps := DefaultDependencies(func(context.Context, sessionrun.MoveRequest, func([]secretscan.Finding)) (sessionrun.MoveResult, error) {
		return sessionrun.MoveResult{}, nil
	})
	deps.Create = func(context.Context, []string, worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
		return []worktrees.CreateResult{{Repository: "acme/app", WorktreeDir: "/tmp/review-auth"}}, nil
	}
	deps.PrepareWorkLog = func(_ string, _ string, options worktrees.WorkLogOptions) (worktrees.WorkLogOptions, error) {
		return options, nil
	}
	deps.Store = func(string) (taskoffload.Store, error) { return store, nil }
	deps.NewID = func() (string, error) { return "task-abc123", nil }
	deps.Now = func() time.Time { return time.Unix(10, 0) }
	return deps, store
}
func request() Request {
	return Request{Task: "review-auth", Repositories: []string{"acme/app"}, Continuation: []byte("Review auth."), ContextFile: "brief.md"}
}
func render(sessionrun.MoveResult) error { return nil }
func parked(t *testing.T, store taskoffload.Store) taskoffload.Record {
	t.Helper()
	record := taskoffload.Record{SchemaVersion: 1, TaskID: "task-abc123", Task: "review-auth", WorktreeDir: "/tmp/review-auth", Repository: "acme/app", Status: taskoffload.StatusParked, CreatedAt: time.Unix(10, 0).UTC()}
	if err := store.Save(record, "Review auth."); err != nil {
		t.Fatal(err)
	}
	return record
}
func TestTaskParkCreatesWorktreeWithoutLaunch(t *testing.T) {
	t.Parallel()
	deps, _ := fixture(t)
	launched := false
	deps.Create = func(_ context.Context, repositories []string, options worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
		if options.Operation != "review-auth" || len(repositories) != 1 {
			t.Fatalf("create options = %#v repos=%v", options, repositories)
		}
		return []worktrees.CreateResult{{Repository: repositories[0], WorktreeDir: "/tmp/review-auth"}}, nil
	}
	deps.Move = func(context.Context, sessionrun.MoveRequest, func([]secretscan.Finding)) (sessionrun.MoveResult, error) {
		launched = true
		return sessionrun.MoveResult{}, nil
	}
	req := request()
	req.ParkOnly = true
	result, err := New(deps).Offload(t.Context(), req, nil, render)
	if err != nil {
		t.Fatal(err)
	}
	if launched {
		t.Fatal("park must not launch a successor")
	}
	if result.Task != "review-auth" || result.Status != "parked" {
		t.Fatal(result)
	}
}
func TestTaskOffloadLaunchesSuccessor(t *testing.T) {
	t.Parallel()
	deps, _ := fixture(t)
	var launch sessionrun.MoveRequest
	deps.Move = func(_ context.Context, req sessionrun.MoveRequest, _ func([]secretscan.Finding)) (sessionrun.MoveResult, error) {
		launch = req
		return sessionrun.MoveResult{}, nil
	}
	req := request()
	req.Harness = "claude"
	req.Model = "opus"
	if _, err := New(deps).Offload(t.Context(), req, nil, render); err != nil {
		t.Fatal(err)
	}
	if launch.Worktree != "/tmp/review-auth" || launch.Harness != "claude-code" || launch.Model != "opus" {
		t.Fatalf("launch = %#v", launch)
	}
}
func TestTaskPickupLaunchesParkedTask(t *testing.T) {
	t.Parallel()
	deps, store := fixture(t)
	record := parked(t, store)
	var launch sessionrun.MoveRequest
	deps.Move = func(_ context.Context, req sessionrun.MoveRequest, _ func([]secretscan.Finding)) (sessionrun.MoveResult, error) {
		launch = req
		return sessionrun.MoveResult{}, nil
	}
	if _, err := New(deps).Pickup(t.Context(), PickupRequest{TaskID: record.TaskID, Harness: "codex", Model: "sol"}, nil, render); err != nil {
		t.Fatal(err)
	}
	if launch.Worktree != "/tmp/review-auth" || launch.Harness != "codex" || launch.Model != "sol" {
		t.Fatalf("launch = %#v", launch)
	}
	loaded, _, err := store.Load(record.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != taskoffload.StatusOffload {
		t.Fatalf("status = %s, want offloaded", loaded.Status)
	}
}
func TestTaskOffloadRejectsBlankContinuationBeforeCreatingWorktree(t *testing.T) {
	t.Parallel()
	for _, park := range []bool{false, true} {
		req := request()
		req.Continuation = []byte(" \n \u2003\u00a0")
		req.ParkOnly = park
		if _, err := New(Dependencies{}).Offload(t.Context(), req, nil, render); err == nil || !strings.Contains(err.Error(), "non-empty continuation") {
			t.Fatalf("blank continuation error = %v", err)
		}
	}
}
func TestTaskOffloadRejectsInvalidHarnessBeforeCreatingWorktree(t *testing.T) {
	t.Parallel()
	req := request()
	req.Harness = "unknown-runtime"
	if _, err := New(Dependencies{}).Offload(t.Context(), req, nil, render); err == nil {
		t.Fatal("invalid harness accepted")
	}
}
func TestTaskOffloadReportsEmptyCreateResultWithoutSavingContinuation(t *testing.T) {
	t.Parallel()
	deps, _ := fixture(t)
	storeCalled := false
	deps.Create = func(context.Context, []string, worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
		return nil, nil
	}
	deps.Store = func(string) (taskoffload.Store, error) { storeCalled = true; return taskoffload.Store{}, nil }
	if _, err := New(deps).Offload(t.Context(), request(), nil, render); err == nil || !strings.Contains(err.Error(), "created no worktree") {
		t.Fatalf("empty create result error = %v", err)
	}
	if storeCalled {
		t.Fatal("continuation store opened without a worktree")
	}
}
func TestTaskOffloadPreservesSavedBriefWhenSuccessorLaunchFails(t *testing.T) {
	t.Parallel()
	deps, store := fixture(t)
	want := errors.New("launcher unavailable")
	deps.Move = func(_ context.Context, req sessionrun.MoveRequest, _ func([]secretscan.Finding)) (sessionrun.MoveResult, error) {
		body, err := os.ReadFile(req.HandoverFile)
		if err != nil || string(body) != "Review auth." {
			t.Fatalf("saved brief = %q, err=%v", body, err)
		}
		return sessionrun.MoveResult{}, want
	}
	if _, err := New(deps).Offload(t.Context(), request(), nil, render); !errors.Is(err, want) {
		t.Fatalf("launch error = %v", err)
	}
	entries, err := os.ReadDir(store.Root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("saved task entries = %v, err=%v", entries, err)
	}
	_, brief, err := store.Load(entries[0].Name())
	if err != nil || brief != "Review auth." {
		t.Fatalf("brief after failed launch = %q, err=%v", brief, err)
	}
}
func TestTaskPickupLaunchFailureLeavesRecordParked(t *testing.T) {
	t.Parallel()
	deps, store := fixture(t)
	record := parked(t, store)
	want := errors.New("launcher unavailable")
	deps.Move = func(_ context.Context, req sessionrun.MoveRequest, _ func([]secretscan.Finding)) (sessionrun.MoveResult, error) {
		if req.Worktree != record.WorktreeDir {
			t.Fatalf("launch request = %+v", req)
		}
		return sessionrun.MoveResult{}, want
	}
	if _, err := New(deps).Pickup(t.Context(), PickupRequest{TaskID: record.TaskID, Target: " remote-vm "}, nil, render); !errors.Is(err, want) {
		t.Fatalf("pickup error = %v", err)
	}
	loaded, _, err := store.Load(record.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != taskoffload.StatusParked {
		t.Fatalf("status after failed launch = %s", loaded.Status)
	}
}
func TestTaskParkAcceptsStdinBriefAndPersistsItsOriginalPrompt(t *testing.T) {
	t.Parallel()
	deps, store := fixture(t)
	raw := []byte("\u2003 Review auth with tests.\n\u00a0")
	deps.Create = func(_ context.Context, repos []string, options worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
		if len(repos) != 1 || repos[0] != "acme/app" || options.WorkLog.OriginalPrompt != "(stdin)" {
			t.Fatalf("create repositories=%v work log=%+v", repos, options.WorkLog)
		}
		expected, err := (worktrees.WorkLogOptions{OriginalPrompt: "-", RequireOriginalPrompt: true, TaskSummary: "offload review-auth", Model: "unknown"}).WithOriginalPromptFromStdin(raw)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(options.WorkLog, expected) {
			t.Fatal("exact raw stdin snapshot/digest was not retained")
		}
		return []worktrees.CreateResult{{Repository: "acme/app", WorktreeDir: "/tmp/review-auth"}}, nil
	}
	req := request()
	req.ParkOnly = true
	req.ContextFile = "-"
	req.Continuation = raw
	result, err := New(deps).Offload(t.Context(), req, nil, render)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "parked" || result.TaskID == "" {
		t.Fatalf("park output = %+v", result)
	}
	_, brief, err := store.Load(result.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if brief != "Review auth with tests." {
		t.Fatalf("stored brief = %q", brief)
	}
}
func TestTaskOffloadAcceptsStdinBriefBeforeLaunchingSuccessor(t *testing.T) {
	t.Parallel()
	deps, _ := fixture(t)
	launched := false
	deps.Create = func(_ context.Context, repos []string, options worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
		if len(repos) != 1 || repos[0] != "acme/app" || options.WorkLog.OriginalPrompt != "(stdin)" {
			t.Fatalf("create repositories=%v work log=%+v", repos, options.WorkLog)
		}
		return []worktrees.CreateResult{{Repository: "acme/app", WorktreeDir: "/tmp/review-auth"}}, nil
	}
	deps.Move = func(_ context.Context, req sessionrun.MoveRequest, _ func([]secretscan.Finding)) (sessionrun.MoveResult, error) {
		launched = true
		body, err := os.ReadFile(req.HandoverFile)
		if err != nil || string(body) != "Review auth with tests." {
			t.Fatalf("successor brief = %q, err=%v", body, err)
		}
		return sessionrun.MoveResult{}, nil
	}
	req := request()
	req.ContextFile = "-"
	req.Continuation = []byte("Review auth with tests.")
	result, err := New(deps).Offload(t.Context(), req, nil, render)
	if err != nil {
		t.Fatal(err)
	}
	if !launched {
		t.Fatal("offload did not launch the successor")
	}
	if result.Status != "offloaded" || result.TaskID == "" {
		t.Fatalf("offload output = %+v", result)
	}
}
func TestTaskPickupAppliesOverridesToLaunchAndSavedRecord(t *testing.T) {
	t.Parallel()
	deps, store := fixture(t)
	record := parked(t, store)
	var launch sessionrun.MoveRequest
	deps.Move = func(_ context.Context, req sessionrun.MoveRequest, _ func([]secretscan.Finding)) (sessionrun.MoveResult, error) {
		launch = req
		return sessionrun.MoveResult{}, nil
	}
	result, err := New(deps).Pickup(t.Context(), PickupRequest{TaskID: record.TaskID, Harness: "codex", Model: "sol", Target: "remote-vm"}, nil, render)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Harness != "codex" || launch.Model != "sol" || launch.Target != "remote-vm" {
		t.Fatalf("launch overrides = %+v", launch)
	}
	loaded, brief, err := store.Load(record.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != taskoffload.StatusOffload || loaded.Target != "remote-vm" || brief != "Review auth." {
		t.Fatalf("saved pickup = %+v brief=%q", loaded, brief)
	}
	if result.Status != "offloaded" {
		t.Fatal(result)
	}
}
func TestDefaultTaskOffloadDependenciesStoreOpensUnderProjectsRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	deps := DefaultDependencies(nil)
	store, err := deps.Store(root)
	if err != nil {
		t.Fatalf("default task-offload store: %v", err)
	}
	if !strings.HasPrefix(store.Root, root) {
		t.Fatalf("task-offload store root = %q, want it under the fixture root %q", store.Root, root)
	}
	if !strings.HasSuffix(store.Root, taskoffload.DirName) {
		t.Fatalf("task-offload store root = %q, want it to end in %q", store.Root, taskoffload.DirName)
	}
	if _, err := deps.Store("/invalid\x00root"); err == nil {
		t.Fatal("unresolvable root accepted")
	}
}
func TestStagedErrorsAndDetachedLaunchPreserveDurableState(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"origin", "invalid repo", "prepare", "create", "store", "id", "save", "render", "pickup store", "pickup load", "pickup harness", "pickup read", "pickup save", "pickup render"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			deps, store := fixture(t)
			req := request()
			want := errors.New(stage)
			var renderer = render
			switch stage {
			case "origin":
				req.Repositories = nil
				deps.OriginSlug = func(context.Context, string) (string, error) { return "", want }
			case "invalid repo":
				req.Repositories = []string{"invalid"}
			case "prepare":
				deps.PrepareWorkLog = func(string, string, worktrees.WorkLogOptions) (worktrees.WorkLogOptions, error) {
					return worktrees.WorkLogOptions{}, want
				}
			case "create":
				deps.Create = func(context.Context, []string, worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
					return nil, want
				}
			case "store", "pickup store":
				deps.Store = func(string) (taskoffload.Store, error) { return taskoffload.Store{}, want }
			case "id":
				deps.NewID = func() (string, error) { return "", want }
			case "save":
				deps.NewID = func() (string, error) { return "invalid", nil }
			case "render", "pickup render":
				renderer = func(sessionrun.MoveResult) error { return want }
			}
			if strings.HasPrefix(stage, "pickup") {
				record := parked(t, store)
				p := PickupRequest{TaskID: record.TaskID}
				if stage == "pickup load" {
					p.TaskID = "task-missing"
				}
				if stage == "pickup harness" {
					p.Harness = "bad"
				}
				if stage == "pickup read" || stage == "pickup save" {
					renderer = func(sessionrun.MoveResult) error {
						if err := os.Remove(filepath.Join(store.Root, record.TaskID, "context.md")); err != nil {
							t.Fatal(err)
						}
						if stage == "pickup save" {
							if err := os.WriteFile(filepath.Join(store.Root, record.TaskID, "context.md"), []byte{}, 0o600); err != nil {
								t.Fatal(err)
							}
						}
						return nil
					}
				}
				if _, err := New(deps).Pickup(t.Context(), p, nil, renderer); err == nil {
					t.Fatal("stage failure lost")
				}
			} else {
				if _, err := New(deps).Offload(t.Context(), req, nil, renderer); err == nil {
					t.Fatal("stage failure lost")
				}
			}
		})
	}
}
func TestCreateKeepsParentContextWhileMoveUsesBackgroundAndRendererStages(t *testing.T) {
	t.Parallel()
	deps, store := fixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var order []string
	deps.OriginSlug = func(got context.Context, path string) (string, error) {
		if got != ctx || path != "." {
			t.Fatal("origin context/path lost")
		}
		return "acme/app", nil
	}
	deps.Create = func(got context.Context, _ []string, options worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
		if got != ctx || got.Err() != context.Canceled || !options.SessionRequired || options.ProjectsRoot != "fixture-root" {
			t.Fatal("Create context/options lost")
		}
		order = append(order, "create")
		return []worktrees.CreateResult{{Repository: "acme/app", WorktreeDir: "/tmp/review-auth"}}, nil
	}
	deps.Move = func(got context.Context, req sessionrun.MoveRequest, warnings func([]secretscan.Finding)) (sessionrun.MoveResult, error) {
		if got.Err() != nil || req.ProjectsRoot != "fixture-root" {
			t.Fatal("Move did not detach")
		}
		_, brief, err := store.Load("task-abc123")
		if err != nil || brief != "Review auth." {
			t.Fatal("brief not saved before Move")
		}
		warnings(nil)
		order = append(order, "move")
		return sessionrun.MoveResult{Phase: "moved"}, nil
	}
	req := request()
	req.Repositories = nil
	req.ProjectsRoot = "fixture-root"
	if _, err := New(deps).Offload(ctx, req, func([]secretscan.Finding) { order = append(order, "warnings") }, func(got sessionrun.MoveResult) error {
		if got.Phase != "moved" {
			t.Fatal(got)
		}
		order = append(order, "render")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "create,warnings,move,render" {
		t.Fatal(order)
	}
}

func TestPickupMoveRenderingPrecedesReadAndCannotSaveOnWriterFailure(t *testing.T) {
	t.Parallel()
	deps, store := fixture(t)
	record := parked(t, store)
	want := errors.New("move output unavailable")
	_, err := New(deps).Pickup(t.Context(), PickupRequest{TaskID: record.TaskID}, nil, func(sessionrun.MoveResult) error {
		loaded, brief, err := store.Load(record.TaskID)
		if err != nil || loaded.Status != taskoffload.StatusParked || brief != "Review auth." {
			t.Fatal("status saved before Move output")
		}
		// An earlier context read would now hide this error ordering regression.
		if err := os.Remove(filepath.Join(store.Root, record.TaskID, "context.md")); err != nil {
			t.Fatal(err)
		}
		return want
	})
	if err != want {
		t.Fatalf("Move output error lost to a later read: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(store.Root, record.TaskID, "task.json"))
	if err != nil {
		t.Fatal(err)
	}
	var loaded taskoffload.Record
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Status != taskoffload.StatusParked {
		t.Fatal("writer failure saved pickup status")
	}
}

func TestActualDefaultIdentityAndClockRemainUsable(t *testing.T) {
	t.Parallel()
	deps := DefaultDependencies(nil)
	id, err := deps.NewID()
	if err != nil || !strings.HasPrefix(id, "task-") {
		t.Fatal(id, err)
	}
	if deps.Now().IsZero() {
		t.Fatal("actual clock lost")
	}
}
