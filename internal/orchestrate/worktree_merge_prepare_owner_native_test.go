package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type prepareOwnerFixture struct {
	engine  engineFixture
	source  worktrees.CreateResult
	sources []WorktreeMergeSource
	options WorktreeMergePrepareOptions
	receipt WorktreeMergeReceipt
}

func newPrepareOwnerFixture(t *testing.T) prepareOwnerFixture {
	t.Helper()
	f := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, f, "owner-source", "feature/owner-source", "source.txt", "source\n")
	head := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	physical, err := filepath.EvalSymlinks(source.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	sources := []WorktreeMergeSource{{Task: "owner-source", Worktree: physical, Branch: source.Branch, SHA: head}}
	lane := worktreeMergeLaneID(f.repository.Slug, "main")
	operation := worktreeMergeOperationID(lane, sources)
	home, err := wbhome.Root(f.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	receipt := WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, ID: operation, Lane: lane, Repository: f.repository.Slug, Target: "main", TargetSHA: source.BaseSHA, Phase: WorktreeMergePhasePrepare, Status: WorktreeMergePreparing, Sources: sources, ReceiptPath: filepath.Join(home, "reports", "worktree-merge", operation+".json")}
	options := WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", Route: WorktreeMergeRoute("unsupported-owner-route")}
	return prepareOwnerFixture{engine: f, source: source, sources: sources, options: options, receipt: receipt}
}

func (f *prepareOwnerFixture) createInterruptedCandidate(t *testing.T) {
	t.Helper()
	prompt, err := writeWorktreeMergePrompt(f.engine.repository.Slug, "main", f.sources)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Remove(prompt); err != nil {
			t.Error(err)
		}
	}()
	created, err := worktrees.Create(t.Context(), []string{f.engine.repository.Slug}, worktrees.CreateOptions{
		ProjectsRoot: f.engine.githubDir, Operation: f.receipt.ID, Branch: "wb/integration/main/" + mergeOperationSuffix(f.receipt.ID), BranchChosen: true, Base: "main",
		WorkLog: worktrees.WorkLogOptions{EffortID: f.receipt.ID, RunID: f.receipt.ID, AgentRuntime: "test", Model: "test-model", OriginalPrompt: prompt, RequireOriginalPrompt: true},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("actual interrupted native creation = %+v, %v", created, err)
	}
	candidate := created[0]
	f.receipt.Candidate = WorktreeMergeCandidate{Task: f.receipt.ID, Worktree: candidate.WorktreeDir, Branch: candidate.Branch, SHA: strings.TrimSpace(runEngineGit(t, candidate.WorktreeDir, "rev-parse", "HEAD"))}
	f.receipt.CreatedAt = time.Now().Add(-time.Hour).UTC()
	if err := persistWorktreeMergeReceipt(f.receipt); err != nil {
		t.Fatal(err)
	}
	if err := validateExactPreparingWorktreeMergeReceipt(t.Context(), f.receipt, f.receipt.Lane, f.receipt.ID, f.sources); err != nil {
		t.Fatalf("native interrupted candidate preconditions: %v", err)
	}
}

// prepareOwnerObservedRunner has no successful-result substitution. A named
// negative observation or private native mutation runs before delegating Git.
type prepareOwnerObservedRunner struct {
	runner.Runner
	before func(context.Context, string, string, []string) error
}

func (r prepareOwnerObservedRunner) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if err := r.before(ctx, dir, name, args); err != nil {
		return runner.Result{}, err
	}
	return r.Runner.RunOpts(ctx, dir, options, name, args...)
}

func TestPrepareOwnerNativeInputRefusalsHaveNoReceiptEffects(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"empty root", "no sources", "canonical discovery", "default branch", "inspection", "invalid target", "source on target", "source read"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newPrepareOwnerFixture(t)
			options := f.options
			want := ""
			sentinel := errors.New("exact source inspection observation refused")
			switch mode {
			case "empty root":
				options.ProjectsRoot = " "
				want = "projects root is required"
			case "no sources":
				options.Sources = nil
				want = "at least one source worktree is required"
			case "canonical discovery":
				options.Target = ""
				options.Sources = []string{filepath.Join(t.TempDir(), "missing")}
				want = "resolve source canonical clone: "
			case "default branch":
				options.Target = ""
				runEngineGit(t, f.engine.canonical, "remote", "remove", "origin")
				want = "resolve remote default branch: "
			case "inspection":
				options.Sources = []string{filepath.Join(t.TempDir(), "missing")}
				want = "guard source "
			case "invalid target":
				options.Target = "["
				want = "guard source " + f.sources[0].Worktree + ": invalid base branch \"[\""
			case "source on target":
				options.Target = f.source.Branch
				want = "on protected base branch"
			case "source read":
				options.run = conflictNegativeRunner{Runner: runner.New(), dir: f.sources[0].Worktree, argv: "rev-parse --verify HEAD^{commit}", err: sentinel}
				want = sentinel.Error()
			}
			got, err := PrepareWorktreeMerge(t.Context(), options)
			if err == nil || !strings.Contains(err.Error(), want) || !reflect.DeepEqual(got, WorktreeMergeReceipt{}) {
				t.Fatalf("native %s refusal=%+v, %v", mode, got, err)
			}
			if mode == "source read" && !errors.Is(err, sentinel) {
				t.Fatalf("native negative source identity lost: %v", err)
			}
			if _, err := os.Stat(f.receipt.ReceiptPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refusal wrote receipt: %v", err)
			}
			if strings.TrimSpace(runEngineGit(t, f.source.WorktreeDir, "rev-parse", "HEAD")) != f.sources[0].SHA {
				t.Fatal("source changed on input refusal")
			}
		})
	}
}

func TestPrepareOwnerNativeReceiptSelectionRefusalsPreserveOriginalEvidence(t *testing.T) {
	t.Parallel()
	sidecars := []struct {
		name string
		path func(string) string
	}{
		{"collision", receiptCollisionAcknowledgementPath}, {"landed failure", landedFailureAcknowledgementPath},
		{"supersession", validationFailureSupersessionPath}, {"rebatch", rebatchPath},
		{"stranded", strandedLandingAcknowledgementPath}, {"absorbed", absorbedConflictAcknowledgementPath},
		{"retired publication", retiredPublicationAcknowledgementPath}, {"unpublished validation failure", unpublishedValidationFailureAcknowledgementPath},
		{"published adoption", publishedCandidateAdoptionPath},
	}
	for _, tc := range sidecars {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newPrepareOwnerFixture(t)
			receipt := f.receipt
			receipt.Status = WorktreeMergePrepared
			if err := persistWorktreeMergeReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			sidecar := tc.path(receipt.ReceiptPath)
			if err := os.WriteFile(sidecar, []byte("not valid JSON\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := PrepareWorktreeMerge(t.Context(), f.options)
			if err == nil || got.ID != receipt.ID {
				t.Fatalf("native malformed %s did not refuse same receipt: %+v, %v", tc.name, got, err)
			}
			after, readErr := os.ReadFile(receipt.ReceiptPath)
			if readErr != nil || string(after) != string(before) {
				t.Fatalf("malformed sidecar changed original bytes: %v", readErr)
			}
		})
	}
	for _, mode := range []string{"malformed receipt", "different sources", "different repository", "different target", "already prepared", "validation failed", "invalid preparing"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newPrepareOwnerFixture(t)
			receipt := f.receipt
			switch mode {
			case "different sources":
				receipt.Sources = nil
			case "different repository":
				receipt.Repository = "acme/other"
			case "different target":
				receipt.Target = "other"
			case "already prepared":
				receipt.Status = WorktreeMergePrepared
			case "validation failed":
				receipt.Status = WorktreeMergeValidationFailed
			case "invalid preparing":
				receipt.Candidate.Task = "wrong"
			}
			if err := persistWorktreeMergeReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			if mode == "malformed receipt" {
				if err := os.WriteFile(receipt.ReceiptPath, []byte("not JSON\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			got, err := PrepareWorktreeMerge(t.Context(), f.options)
			if mode == "already prepared" {
				if err != nil || !reflect.DeepEqual(got, receipt) {
					t.Fatalf("read-only replay=%+v, %v", got, err)
				}
			} else if err == nil {
				t.Fatalf("native %s receipt accepted: %+v", mode, got)
			}
			after, readErr := os.ReadFile(receipt.ReceiptPath)
			if readErr != nil || string(after) != string(before) {
				t.Fatalf("receipt selection changed prior bytes: %v", readErr)
			}
		})
	}
}

func TestPrepareOwnerNativeDurableIORefusalsHaveExactStageAndIdentity(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("named per-invocation durable IO refusal")
	for _, mode := range []string{"initial read", "initial save", "integrated save", "prepared save", "postlock read"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newPrepareOwnerFixture(t)
			if mode == "postlock read" {
				f.createInterruptedCandidate(t)
			}
			options := f.options
			options.PrepareTimeout = time.Minute
			options.Sleep = func(time.Duration) {}
			if mode == "prepared save" {
				options.Route = WorktreeMergeRoutePullRequest
				options.ValidateLocally = true
			}
			var locked atomic.Bool
			options.Progress = func(event progress.Event) {
				if event.Phase == "acquire_lane" && event.State == progress.Completed {
					locked.Store(true)
				}
			}
			var refused atomic.Bool
			read := func(path string) (WorktreeMergeReceipt, error) {
				if path == f.receipt.ReceiptPath && (mode == "initial read" || mode == "postlock read" && locked.Load()) && refused.CompareAndSwap(false, true) {
					return WorktreeMergeReceipt{}, sentinel
				}
				return readWorktreeMergeReceipt(path)
			}
			save := func(receipt WorktreeMergeReceipt) error {
				reject := mode == "initial save" && receipt.Status == WorktreeMergePreparing && receipt.Candidate.SHA == "" || mode == "integrated save" && receipt.Status == WorktreeMergePreparing && len(receipt.Sources) > 0 && receipt.Sources[0].Merged || mode == "prepared save" && receipt.Status == WorktreeMergePrepared
				if reject && refused.CompareAndSwap(false, true) {
					return sentinel
				}
				return persistWorktreeMergeReceipt(receipt)
			}
			got, err := prepareWorktreeMerge(t.Context(), options, read, save)
			if !refused.Load() || !errors.Is(err, sentinel) {
				t.Fatalf("%s failed before intended native stage: %+v, %v refused=%v", mode, got, err, refused.Load())
			}
			if mode == "postlock read" && (!locked.Load() || !reflect.DeepEqual(got, WorktreeMergeReceipt{})) {
				t.Fatalf("postlock read must hold lock and return zero receipt: %+v", got)
			}
			if mode == "initial read" || mode == "initial save" {
				if _, err := os.Stat(f.receipt.ReceiptPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed first durable boundary wrote receipt: %v", err)
				}
			} else {
				durable, readErr := readWorktreeMergeReceipt(f.receipt.ReceiptPath)
				if readErr != nil || durable.Status != WorktreeMergePreparing {
					t.Fatalf("failed later boundary rewrote old durable status: %+v, %v", durable, readErr)
				}
			}
			if got.Candidate.Worktree != "" {
				if _, err := worktrees.Guard(t.Context(), got.Candidate.Worktree, worktrees.GuardOptions{ProjectsRoot: f.engine.githubDir, Base: "main"}); err != nil {
					t.Fatalf("durable stage candidate lost native custody: %v", err)
				}
			}
		})
	}
}

func TestPrepareOwnerNativeIntegrationRefusalsPersistFailureAndPreserveSources(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"candidate clean", "source ancestry", "merge", "integrated head", "source recheck", "final clean", "route", "final head"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newPrepareOwnerFixture(t)
			sentinel := errors.New("exact negative integration observation")
			var integrated atomic.Bool
			var validating atomic.Bool
			var candidatePath atomic.Value
			var consumed atomic.Bool
			options := f.options
			if mode == "final head" {
				options.Route = WorktreeMergeRoutePullRequest
				options.ValidateLocally = true
			}
			options.Progress = func(event progress.Event) {
				if event.Phase == "integrate_sources" && event.State == progress.Running {
					integrated.Store(true)
				}
				if event.Phase == "create_candidate" && event.State == progress.Completed {
					candidatePath.Store(event.Detail)
				}
				if event.Phase == "validate_candidate" && event.State == progress.Completed && (event.Detail == "passed" || event.Detail == "skipped") {
					validating.Store(true)
				}
			}
			options.run = prepareOwnerObservedRunner{Runner: runner.New(), before: func(_ context.Context, dir, name string, args []string) error {
				if name != "git" {
					return nil
				}
				argv := strings.Join(args, " ")
				candidate := false
				if actual := candidatePath.Load(); actual != nil {
					candidate = dir == actual.(string)
				}
				reject := candidate && mode == "candidate clean" && argv == "status --porcelain=v1" && !integrated.Load() ||
					candidate && mode == "source ancestry" && argv == "merge-base "+f.sources[0].SHA+" HEAD" ||
					candidate && mode == "merge" && argv == "merge --no-edit "+f.sources[0].SHA ||
					candidate && mode == "integrated head" && argv == "rev-parse --verify HEAD^{commit}" && integrated.Load() ||
					dir == f.sources[0].Worktree && mode == "source recheck" && argv == "status --porcelain=v1" && integrated.Load() ||
					candidate && mode == "final clean" && argv == "status --porcelain=v1" && integrated.Load() ||
					candidate && mode == "final head" && argv == "rev-parse --verify HEAD^{commit}" && validating.Load()
				if reject && consumed.CompareAndSwap(false, true) {
					return sentinel
				}
				return nil
			}}
			got, err := PrepareWorktreeMerge(t.Context(), options)
			if mode == "route" {
				if err == nil || !strings.Contains(err.Error(), "unsupported merge route") {
					t.Fatalf("route refusal=%+v, %v", got, err)
				}
			} else if !consumed.Load() || !errors.Is(err, sentinel) {
				t.Fatalf("%s failed before named observation: %+v, %v consumed=%v", mode, got, err, consumed.Load())
			}
			if got.Status != WorktreeMergeConflict || got.Failure == "" {
				t.Fatalf("failure not returned as durable conflict: %+v", got)
			}
			durable, readErr := readWorktreeMergeReceipt(got.ReceiptPath)
			if readErr != nil || durable.Status != got.Status || durable.Failure != got.Failure {
				t.Fatalf("durable failure=%+v, %v", durable, readErr)
			}
			if strings.TrimSpace(runEngineGit(t, f.source.WorktreeDir, "rev-parse", "HEAD")) != f.sources[0].SHA {
				t.Fatal("negative integration mutated authoritative source")
			}
		})
	}
}

func TestPrepareOwnerNativePostLockEvidenceIsReadAgain(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"collision sidecar", "adoption sidecar", "changed status", "competing lane"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newPrepareOwnerFixture(t)
			f.createInterruptedCandidate(t)
			if mode == "competing lane" {
				writeEngineFile(t, filepath.Join(f.source.WorktreeDir, "advanced.txt"), "advanced\n")
				runEngineGit(t, f.source.WorktreeDir, "add", "advanced.txt")
				runEngineGit(t, f.source.WorktreeDir, "commit", "-m", "native additive advance before lock")
			}
			before, err := os.ReadFile(f.receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			var changed atomic.Bool
			options := f.options
			options.Progress = func(event progress.Event) {
				if event.Phase != "acquire_lane" || event.State != progress.Completed || !changed.CompareAndSwap(false, true) {
					return
				}
				switch mode {
				case "collision sidecar":
					if err := os.WriteFile(receiptCollisionAcknowledgementPath(f.receipt.ReceiptPath), []byte("not JSON\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "adoption sidecar":
					if err := os.WriteFile(publishedCandidateAdoptionPath(f.receipt.ReceiptPath), []byte("not JSON\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "changed status":
					receipt := f.receipt
					receipt.Status = WorktreeMergeValidationFailed
					if err := persistWorktreeMergeReceipt(receipt); err != nil {
						t.Fatal(err)
					}
				case "competing lane":
					// A durable competing receipt is a native store observation. Neither candidate custody nor a positive admission is substituted.
					receipt := f.receipt
					receipt.ID = "other-operation"
					receipt.Status = WorktreeMergePreparing
					receipt.ReceiptPath = filepath.Join(filepath.Dir(receipt.ReceiptPath), f.receipt.Lane+"-other-operation.json")
					if err := persistWorktreeMergeReceipt(receipt); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := PrepareWorktreeMerge(t.Context(), options)
			if !changed.Load() || err == nil {
				t.Fatalf("post-lock %s was not observed: %+v, %v", mode, got, err)
			}
			switch mode {
			case "collision sidecar":
				if !strings.Contains(err.Error(), "re-read receipt-collision acknowledgement") {
					t.Fatalf("collision wrong stage: %v", err)
				}
			case "adoption sidecar":
				if !strings.Contains(err.Error(), "re-read published-candidate adoption") {
					t.Fatalf("adoption wrong stage: %v", err)
				}
			case "changed status":
				if !strings.Contains(err.Error(), "is validation_failed; only an exact preparing receipt may resume") {
					t.Fatalf("status wrong stage: %v", err)
				}
			case "competing lane":
				if !strings.Contains(err.Error(), "is still owned by non-terminal receipt") {
					t.Fatalf("competing native post-lock receipt was not refused: %v", err)
				}
			}
			after, readErr := os.ReadFile(f.receipt.ReceiptPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if mode != "changed status" && string(after) != string(before) {
				t.Fatal("post-lock refusal rewrote original evidence")
			}
		})
	}
}

func TestPrepareOwnerNativeLockAndRebatchRefusalsDoNotCreateCandidate(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"held operation", "missing rebatch", "malformed active scan"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newPrepareOwnerFixture(t)
			options := f.options
			switch mode {
			case "held operation":
				lock, err := AcquireOperationLock(f.engine.githubDir, f.receipt.Lane, false)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := lock.Release(); err != nil {
						t.Error(err)
					}
				})
			case "missing rebatch":
				options.RebatchReceipt = filepath.Join(t.TempDir(), "missing.json")
			case "malformed active scan":
				if err := os.MkdirAll(filepath.Dir(f.receipt.ReceiptPath), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(filepath.Dir(f.receipt.ReceiptPath), f.receipt.Lane+"-malformed.json"), []byte("not JSON\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := PrepareWorktreeMerge(t.Context(), options)
			if err == nil || !reflect.DeepEqual(got, WorktreeMergeReceipt{}) {
				t.Fatalf("native %s refusal=%+v, %v", mode, got, err)
			}
			if _, err := os.Stat(f.receipt.ReceiptPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("early refusal wrote receipt: %v", err)
			}
			listed, listErr := worktrees.List(t.Context(), worktrees.ListOptions{ProjectsRoot: f.engine.githubDir, Task: f.receipt.ID, Base: "main", Workers: 1})
			if listErr != nil || len(listed) != 0 {
				t.Fatalf("early refusal created candidate: %+v, %v", listed, listErr)
			}
		})
	}
}

func TestPrepareOwnerNativeFailureSavePreservesPrimaryErrorIdentity(t *testing.T) {
	t.Parallel()
	f := newPrepareOwnerFixture(t)
	primary := errors.New("named candidate observation refused")
	persistence := errors.New("failure receipt persistence refused")
	var candidatePath atomic.Value
	options := f.options
	options.Progress = func(event progress.Event) {
		if event.Phase == "create_candidate" && event.State == progress.Completed {
			candidatePath.Store(event.Detail)
		}
	}
	options.run = prepareOwnerObservedRunner{Runner: runner.New(), before: func(_ context.Context, dir, name string, args []string) error {
		if actual := candidatePath.Load(); actual != nil && name == "git" && dir == actual.(string) && strings.Join(args, " ") == "status --porcelain=v1" {
			return primary
		}
		return nil
	}}
	var failureSave atomic.Bool
	save := func(receipt WorktreeMergeReceipt) error {
		if receipt.Status == WorktreeMergeConflict {
			failureSave.Store(true)
			return persistence
		}
		return persistWorktreeMergeReceipt(receipt)
	}
	got, err := prepareWorktreeMerge(t.Context(), options, readWorktreeMergeReceipt, save)
	if !failureSave.Load() || !errors.Is(err, primary) || errors.Is(err, persistence) || !strings.Contains(err.Error(), "persist failure receipt: "+persistence.Error()) || got.Status != WorktreeMergeConflict {
		t.Fatalf("failure/save precedence=%+v, %v", got, err)
	}
	durable, readErr := readWorktreeMergeReceipt(got.ReceiptPath)
	if readErr != nil || durable.Status != WorktreeMergePreparing || durable.Failure != "" {
		t.Fatalf("failed save must retain previous durable receipt=%+v, %v", durable, readErr)
	}
}

func TestPrepareOwnerNativeLaneFailureReleasesAndSuccessRetainsCustody(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"refused live owner", "early failure releases", "failure releases", "success retains"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newPrepareOwnerFixture(t)
			home, err := wbhome.EnsureRoot(f.engine.githubDir)
			if err != nil {
				t.Fatal(err)
			}
			owner := landinglane.Owner{WBSessionID: "owner-session", PID: os.Getpid(), Runtime: "test", Command: "prepare owner native witness"}
			if _, err := session.Register(filepath.Join(home, session.DirName), session.Record{PID: os.Getpid(), WBSessionID: owner.WBSessionID, Runtime: "test", Model: "test", StartedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			options := f.options
			options.Lane = LaneGuardRequest{Owner: owner}
			if mode == "refused live owner" {
				if _, err := landinglane.Acquire(home, landinglane.AcquireRequest{Repository: f.engine.repository.Slug, Target: "main", Self: owner, SessionDir: filepath.Join(home, session.DirName)}); err != nil {
					t.Fatal(err)
				}
				options.Lane.Owner.WBSessionID = "requesting-other-owner"
			}
			if mode == "early failure releases" {
				options.RebatchReceipt = filepath.Join(t.TempDir(), "missing-rebatch.json")
			}
			if mode == "success retains" {
				options.Route = WorktreeMergeRoutePullRequest
				options.ValidateLocally = true
			}
			got, err := PrepareWorktreeMerge(t.Context(), options)
			record, found, readErr := landinglane.Read(home, f.engine.repository.Slug, "main")
			if readErr != nil {
				t.Fatal(readErr)
			}
			switch mode {
			case "refused live owner":
				var conflict *landinglane.ConflictError
				if !errors.As(err, &conflict) || !found || record.Owner.WBSessionID != owner.WBSessionID || !reflect.DeepEqual(got, WorktreeMergeReceipt{}) {
					t.Fatalf("actual live-owner refusal=%+v, %v lane=%+v found=%v", got, err, record, found)
				}
			case "early failure releases":
				if err == nil || !reflect.DeepEqual(got, WorktreeMergeReceipt{}) || found {
					t.Fatalf("native early refusal retained lane=%+v, %v found=%v", got, err, found)
				}
			case "failure releases":
				if err == nil || got.Status != WorktreeMergeConflict || found {
					t.Fatalf("native failed prepare retained lane=%+v, %v found=%v", got, err, found)
				}
			case "success retains":
				if err != nil || got.Status != WorktreeMergePrepared || !found || record.Owner.WBSessionID != owner.WBSessionID || got.LaneOwner == nil || got.LaneOwner.Owner.WBSessionID != owner.WBSessionID {
					t.Fatalf("native success custody=%+v, %v lane=%+v found=%v", got, err, record, found)
				}
				if err := releaseLandingLane(f.engine.githubDir, f.engine.repository.Slug, "main", owner.WBSessionID); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPrepareOwnerNativeLazyAdmissionRefusalStopsValidation(t *testing.T) {
	t.Parallel()
	f := newPrepareOwnerFixture(t)
	options := f.options
	options.Route = WorktreeMergeRoutePullRequest
	options.ValidateLocally = true
	sentinel := errors.New("per-instance host admission refused")
	var called atomic.Bool
	options.RequireHostLoadAdmission = func() (*WorktreeMergeHostLoadAdmission, error) { called.Store(true); return nil, sentinel }
	got, err := PrepareWorktreeMerge(t.Context(), options)
	if !called.Load() || !errors.Is(err, sentinel) || got.Status != WorktreeMergeConflict || got.Validation.Status != "" {
		t.Fatalf("native admission refusal=%+v, %v called=%v", got, err, called.Load())
	}
	durable, readErr := readWorktreeMergeReceipt(got.ReceiptPath)
	if readErr != nil || durable.Status != WorktreeMergeConflict || durable.Failure != sentinel.Error() {
		t.Fatalf("durable admission failure=%+v, %v", durable, readErr)
	}
}

//nolint:paralleltest // This genuine native prompt refusal changes TMPDIR only for the serial test; all paths are private and restored by testing.
func TestPrepareOwnerNativePromptFailureUsesPrivateTemporaryDirectory(t *testing.T) {
	f := newPrepareOwnerFixture(t)
	bad := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(bad, []byte("private regular file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Setenv("TMP", bad)
		t.Setenv("TEMP", bad)
	} else {
		t.Setenv("TMPDIR", bad)
	}
	got, err := PrepareWorktreeMerge(t.Context(), f.options)
	var pathError *os.PathError
	if !errors.As(err, &pathError) || !reflect.DeepEqual(got, WorktreeMergeReceipt{}) || !strings.HasPrefix(pathError.Path, filepath.Join(bad, "wb-worktree-merge-prompt-")) {
		t.Fatalf("native prompt directory refusal=%+v, %v", got, err)
	}
	if _, err := os.Stat(f.receipt.ReceiptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed prompt wrote receipt: %v", err)
	}
}

//nolint:paralleltest // This native legacy-home resolution refusal uses a private HOME and restores process environment; it cannot run alongside Parallel fixtures.
func TestPrepareOwnerNativeHomeErrorAfterActualSourceInspection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("private Unix HOME symlink-loop witness; Windows USERPROFILE/symlink privileges have a different native contract")
	}
	f := newPrepareOwnerFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	var changed atomic.Bool
	options := f.options
	options.Progress = func(event progress.Event) {
		if event.Phase == "inspect_sources" && event.State == progress.Completed && changed.CompareAndSwap(false, true) {
			path := filepath.Join(home, ".wb")
			if err := os.Symlink(path, path); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := PrepareWorktreeMerge(t.Context(), options)
	if !changed.Load() || err == nil || !reflect.DeepEqual(got, WorktreeMergeReceipt{}) {
		t.Fatalf("actual post-inspection home error=%+v, %v observed=%v", got, err, changed.Load())
	}
	if _, err := os.Stat(f.receipt.ReceiptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("home resolution wrote receipt: %v", err)
	}
}

func TestPrepareOwnerNativeContinuationRechecksUnderHeldLock(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"before lock source ancestry", "after lock source ancestry", "after lock refusal"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newPrepareOwnerFixture(t)
			f.createInterruptedCandidate(t)
			oldSHA := f.sources[0].SHA
			writeEngineFile(t, filepath.Join(f.source.WorktreeDir, "additive.txt"), "additive\n")
			runEngineGit(t, f.source.WorktreeDir, "add", "additive.txt")
			runEngineGit(t, f.source.WorktreeDir, "commit", "-m", "native additive source for continuation")
			freshSHA := strings.TrimSpace(runEngineGit(t, f.source.WorktreeDir, "rev-parse", "HEAD"))
			before, err := os.ReadFile(f.receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			sentinel := errors.New("exact temporal source ancestry observation refused")
			var locked atomic.Bool
			var consumed atomic.Bool
			options := f.options
			options.Progress = func(event progress.Event) {
				if event.Phase == "acquire_lane" && event.State == progress.Completed {
					locked.Store(true)
					if mode == "after lock refusal" {
						current := f.receipt
						current.Status = WorktreeMergeComplete
						if err := persistWorktreeMergeReceipt(current); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			options.run = prepareOwnerObservedRunner{Runner: runner.New(), before: func(_ context.Context, dir, name string, args []string) error {
				if mode == "after lock refusal" {
					return nil
				}
				reject := mode == "before lock source ancestry" && !locked.Load() || mode == "after lock source ancestry" && locked.Load()
				if reject && dir == f.sources[0].Worktree && name == "git" && strings.Join(args, " ") == "merge-base "+oldSHA+" "+freshSHA && consumed.CompareAndSwap(false, true) {
					return sentinel
				}
				return nil
			}}
			got, err := PrepareWorktreeMerge(t.Context(), options)
			if mode == "after lock refusal" {
				if err == nil || !locked.Load() || !strings.Contains(err.Error(), "can no longer refresh") {
					t.Fatalf("native changed postlock policy=%+v, %v", got, err)
				}
			} else {
				if !consumed.Load() || !errors.Is(err, sentinel) || got.ID != f.receipt.ID {
					t.Fatalf("native temporal %s=%+v, %v consumed=%v", mode, got, err, consumed.Load())
				}
				after, readErr := os.ReadFile(f.receipt.ReceiptPath)
				if readErr != nil || string(after) != string(before) {
					t.Fatalf("negative continuation changed durable prior: %v", readErr)
				}
			}
		})
	}
}

func TestPrepareOwnerNativeExactPublishedFailureReplayIsReadOnly(t *testing.T) {
	t.Parallel()
	f := newPrepareOwnerFixture(t)
	f.createInterruptedCandidate(t)
	// This branch is a recorded receipt replay predicate, not a claim of server CI or publication observation.
	receipt := f.receipt
	receipt.Status = WorktreeMergeValidationFailed
	receipt.PullRequest = "https://example.test/pull/1"
	receipt.PublishedCandidateSHA = receipt.Candidate.SHA
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := PrepareWorktreeMerge(t.Context(), f.options)
	if err != nil || !reflect.DeepEqual(got, receipt) {
		t.Fatalf("recorded exact failure replay=%+v, %v", got, err)
	}
	after, readErr := os.ReadFile(receipt.ReceiptPath)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("read-only exact replay changed evidence: %v", readErr)
	}
}

func TestPrepareOwnerNativeTargetRepairFaultsKeepOriginalLandingEvidence(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"target fetch", "target revision", "landing ancestry", "target rewind", "repair merge", "squash repair merge", "candidate not absorbed", "refresh after lock"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newPrepareOwnerFixture(t)
			f.createInterruptedCandidate(t)
			runEngineGit(t, f.receipt.Candidate.Worktree, "merge", "--no-edit", f.sources[0].SHA)
			receipt := f.receipt
			receipt.Candidate.SHA = strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
			if mode == "squash repair merge" {
				runEngineGit(t, f.engine.canonical, "merge", "--squash", receipt.Candidate.SHA)
				runEngineGit(t, f.engine.canonical, "commit", "-m", "native squash landing for repair")
			} else {
				runEngineGit(t, f.engine.canonical, "merge", "--ff-only", receipt.Candidate.SHA)
			}
			runEngineGit(t, f.engine.canonical, "push", "origin", "main")
			receipt.Phase = WorktreeMergePhaseLand
			receipt.Status = WorktreeMergePostTargetCIFailed
			receipt.LandingSHA = strings.TrimSpace(runEngineGit(t, f.engine.canonical, "rev-parse", "HEAD"))
			if mode == "squash repair merge" {
				runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", receipt.Candidate.SHA+":refs/heads/"+receipt.Candidate.Branch)
				receipt.PullRequest = "https://example.test/pull/private-squash"
				receipt.PublishedCandidateSHA = receipt.Candidate.SHA
				candidateTree := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", receipt.Candidate.SHA+"^{tree}"))
				landingTree := strings.TrimSpace(runEngineGit(t, f.engine.canonical, "rev-parse", receipt.LandingSHA+"^{tree}"))
				if candidateTree != landingTree {
					t.Fatalf("actual squash landing did not preserve candidate tree: %s != %s", candidateTree, landingTree)
				}
			}
			if mode == "candidate not absorbed" {
				writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "unlanded.txt"), "native unlanded future candidate\n")
				runEngineGit(t, receipt.Candidate.Worktree, "add", "unlanded.txt")
				runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "native future candidate not contained by target")
				receipt.Candidate.SHA = strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
			}
			receipt.Failure = "recorded target CI failure"
			if err := persistWorktreeMergeReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			writeEngineFile(t, filepath.Join(f.source.WorktreeDir, "repair.txt"), "repair\n")
			runEngineGit(t, f.source.WorktreeDir, "add", "repair.txt")
			runEngineGit(t, f.source.WorktreeDir, "commit", "-m", "repair actual private landed target")
			fresh := f.sources[0]
			fresh.SHA = strings.TrimSpace(runEngineGit(t, f.source.WorktreeDir, "rev-parse", "HEAD"))
			choice, choiceErr := choosePrepareContinuation(t.Context(), runner.New(), receipt, []WorktreeMergeSource{fresh})
			if choiceErr != nil || choice != prepareForwardRepair {
				t.Fatalf("actual native repair preconditions=%v, %v", choice, choiceErr)
			}
			before, err := os.ReadFile(receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			sentinel := errors.New("exact postcustody repair observation refused")
			var consumed atomic.Bool
			options := f.options
			var changedAtLock atomic.Bool
			options.Progress = func(event progress.Event) {
				if mode == "refresh after lock" && event.Phase == "acquire_lane" && event.State == progress.Completed && changedAtLock.CompareAndSwap(false, true) {
					current := receipt
					current.Status = WorktreeMergeChecksFailed
					current.LandingSHA = ""
					if err := persistWorktreeMergeReceipt(current); err != nil {
						t.Fatal(err)
					}
					choice, choiceErr := choosePrepareContinuation(t.Context(), runner.New(), current, []WorktreeMergeSource{fresh})
					if choiceErr != nil || choice != prepareRefresh {
						t.Fatalf("native temporal reread must select refresh=%v, %v", choice, choiceErr)
					}
				}
			}
			options.run = prepareOwnerObservedRunner{Runner: runner.New(), before: func(_ context.Context, dir, name string, args []string) error {
				if dir != receipt.Candidate.Worktree || name != "git" {
					return nil
				}
				argv := strings.Join(args, " ")
				reject := (mode == "target fetch" || mode == "refresh after lock") && argv == "fetch --no-tags origin +refs/heads/main:refs/remotes/origin/main" ||
					mode == "target revision" && argv == "rev-parse --verify refs/remotes/origin/main^{commit}" ||
					mode == "landing ancestry" && argv == "merge-base "+receipt.LandingSHA+" "+receipt.LandingSHA ||
					mode == "repair merge" && argv == "merge --ff-only "+receipt.LandingSHA ||
					mode == "squash repair merge" && argv == "merge --no-ff --no-edit "+receipt.LandingSHA
				if mode == "target rewind" && argv == "fetch --no-tags origin +refs/heads/main:refs/remotes/origin/main" && consumed.CompareAndSwap(false, true) {
					// Real private remote rewind after both continuation proofs; every subsequent observation is actual Git.
					runEngineGit(t, f.engine.canonical, "push", "--force", "origin", receipt.TargetSHA+":refs/heads/main")
					return nil
				}
				if reject && consumed.CompareAndSwap(false, true) {
					return sentinel
				}
				return nil
			}}
			got, err := PrepareWorktreeMerge(t.Context(), options)
			if mode != "candidate not absorbed" && !consumed.Load() || err == nil || got.ID != receipt.ID || got.Candidate != receipt.Candidate || mode != "refresh after lock" && got.LandingSHA != receipt.LandingSHA {
				t.Fatalf("native %s repair boundary=%+v, %v consumed=%v", mode, got, err, consumed.Load())
			}
			if mode == "candidate not absorbed" {
				if !strings.Contains(err.Error(), "neither contains prior candidate") {
					t.Fatalf("native nonabsorbed candidate diagnostic=%v", err)
				}
			} else if mode == "target rewind" {
				if !strings.Contains(err.Error(), "no longer contains prior landing") {
					t.Fatalf("native target rewind diagnostic=%v", err)
				}
			} else if !errors.Is(err, sentinel) {
				t.Fatalf("repair error identity lost=%v", err)
			}
			after, readErr := os.ReadFile(receipt.ReceiptPath)
			if mode == "refresh after lock" {
				current, currentErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
				if !changedAtLock.Load() || currentErr != nil || current.Status != WorktreeMergeChecksFailed || current.LandingSHA != "" || current.Candidate != receipt.Candidate || got.Status != current.Status {
					t.Fatalf("monotonic repair replaced actual reread evidence=%+v, %v", current, currentErr)
				}
			} else if readErr != nil || string(after) != string(before) {
				t.Fatalf("repair refusal rewrote landed evidence: %v", readErr)
			}
		})
	}
}

// The recorded failure/publication fields below select existing replay policy;
// custody, Work Log and every positive ancestry observation remain actual.
func TestPrepareOwnerNativeTemporalProtocolRefusalsKeepEvidence(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"target check canceled", "initial replay ancestry", "active replay ancestry", "postlock replay ancestry", "postlock exact replay", "active collision sidecar", "active adoption sidecar", "active validation failed", "active checks failed", "postlock active decode", "create filesystem refusal", "unknown model"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newPrepareOwnerFixture(t)
			options := f.options
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var observed atomic.Bool
			var baseline []byte
			want := ""
			if strings.Contains(mode, "replay") || strings.HasPrefix(mode, "active ") || mode == "postlock active decode" {
				f.createInterruptedCandidate(t)
			}
			if mode == "postlock active decode" {
				writeEngineFile(t, filepath.Join(f.source.WorktreeDir, "postlock-refresh.txt"), "native additive refresh\n")
				runEngineGit(t, f.source.WorktreeDir, "add", "postlock-refresh.txt")
				runEngineGit(t, f.source.WorktreeDir, "commit", "-m", "advance source before postlock inventory fault")
				fresh := append([]WorktreeMergeSource(nil), f.sources...)
				fresh[0].SHA = strings.TrimSpace(runEngineGit(t, f.source.WorktreeDir, "rev-parse", "HEAD"))
				choice, err := choosePrepareContinuation(t.Context(), runner.New(), f.receipt, fresh)
				if err != nil || choice != prepareRefresh {
					t.Fatalf("native additive postlock refresh precondition=%v, %v", choice, err)
				}
			}
			setReplay := func(exact bool) {
				receipt := f.receipt
				receipt.Status = WorktreeMergeValidationFailed
				receipt.PullRequest = "https://example.test/pull/1"
				receipt.PublishedCandidateSHA = f.sources[0].SHA
				receipt.SourceRefreshes = []WorktreeMergeSourceRefresh{{Sources: append([]WorktreeMergeSource(nil), f.sources...)}}
				if exact {
					receipt.PublishedCandidateSHA = receipt.Candidate.SHA
				}
				if err := persistWorktreeMergeReceipt(receipt); err != nil {
					t.Fatal(err)
				}
				f.receipt = receipt
			}
			if mode == "initial replay ancestry" || mode == "active replay ancestry" {
				setReplay(false)
			}
			if strings.HasPrefix(mode, "active ") {
				oldPath := f.receipt.ReceiptPath
				if mode == "active validation failed" {
					f.receipt.Status = WorktreeMergeValidationFailed
				}
				if mode == "active checks failed" {
					f.receipt.Status = WorktreeMergeChecksFailed
				}
				f.receipt.ReceiptPath = filepath.Join(filepath.Dir(oldPath), f.receipt.Lane+"-private-active.json")
				if err := persistWorktreeMergeReceipt(f.receipt); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(oldPath); err != nil {
					t.Fatal(err)
				}
				if mode == "active collision sidecar" || mode == "active adoption sidecar" {
					path := receiptCollisionAcknowledgementPath(f.receipt.ReceiptPath)
					if mode == "active adoption sidecar" {
						path = publishedCandidateAdoptionPath(f.receipt.ReceiptPath)
					}
					if err := os.WriteFile(path, []byte("malformed private sidecar\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if f.receipt.Candidate.Worktree != "" {
				var err error
				baseline, err = os.ReadFile(f.receipt.ReceiptPath)
				if err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "target check canceled":
				want = "invalid target branch"
			case "initial replay ancestry", "active replay ancestry", "postlock replay ancestry":
				want = "verify published validation failure replay"
			case "active collision sidecar":
				want = "validate receipt-collision acknowledgement"
			case "active adoption sidecar":
				want = "validate published-candidate adoption"
			case "active validation failed":
				want = "is validation_failed; only an exact preparing receipt may resume"
			case "active checks failed":
				want = "is still owned by non-terminal receipt"
			case "postlock active decode":
				want = "decode merge receipt"
			case "create filesystem refusal":
				want = "create candidate worktree:"
			case "unknown model":
				options.Model = ""
				want = "unsupported merge route"
			}
			options.Progress = func(event progress.Event) {
				if mode == "target check canceled" && event.Phase == "inspect_sources" && event.State == progress.Completed && observed.CompareAndSwap(false, true) {
					cancel()
				}
				if strings.HasPrefix(mode, "postlock ") && event.Phase == "acquire_lane" && event.State == progress.Completed && observed.CompareAndSwap(false, true) {
					if mode == "postlock active decode" {
						path := filepath.Join(filepath.Dir(f.receipt.ReceiptPath), f.receipt.Lane+"-malformed-after-lock.json")
						if err := os.WriteFile(path, []byte("not JSON\n"), 0o600); err != nil {
							t.Fatal(err)
						}
					} else {
						setReplay(mode == "postlock exact replay")
						var err error
						baseline, err = os.ReadFile(f.receipt.ReceiptPath)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				if mode == "create filesystem refusal" && event.Phase == "create_candidate" && event.State == progress.Started && observed.CompareAndSwap(false, true) {
					path := filepath.Join(f.engine.githubDir, ".worktrees", f.receipt.ID)
					if err := os.WriteFile(path, []byte("private path blocker\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := PrepareWorktreeMerge(ctx, options)
			if strings.HasPrefix(mode, "postlock ") || mode == "create filesystem refusal" || mode == "target check canceled" {
				if !observed.Load() {
					t.Fatalf("did not reach exact temporal %s boundary: %+v, %v", mode, got, err)
				}
			}
			if mode == "postlock exact replay" {
				if err != nil || !reflect.DeepEqual(got, f.receipt) {
					t.Fatalf("actual postlock exact recorded replay=%+v, %v", got, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s native boundary=%+v, %v want %q", mode, got, err, want)
			}
			if baseline != nil {
				after, readErr := os.ReadFile(f.receipt.ReceiptPath)
				if readErr != nil || string(after) != string(baseline) {
					t.Fatalf("temporal refusal rewrote evidence: %v", readErr)
				}
			}
			if mode == "unknown model" {
				view, viewErr := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{ProjectsRoot: f.engine.githubDir, Worktree: got.Candidate.Worktree})
				if viewErr != nil || view.Claim == nil || view.Claim.Model != "unknown" {
					t.Fatalf("actual model fallback was not recorded: %+v, %v", view, viewErr)
				}
			}
		})
	}
}

func TestPrepareOwnerNativeRebatchRecoveryReprovesUnderRealLock(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"held lock", "locked read", "original changed", "replacement changed", "candidate cleanliness", "ancestry error", "noncontaining replacement", "ack filesystem refusal", "success"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newPrepareOwnerFixture(t)
			options := f.options
			options.Route = WorktreeMergeRoutePullRequest
			options.ValidateLocally = true
			first, err := PrepareWorktreeMerge(t.Context(), options)
			if err != nil {
				t.Fatalf("native first prepared fixture: %v", err)
			}
			extra := createMergeSource(t, f.engine, "owner-extra", "feature/owner-extra", "extra.txt", "extra\n")
			options.Sources = append(options.Sources, extra.WorktreeDir)
			options.RebatchReceipt = first.ReceiptPath
			replacement, err := PrepareWorktreeMerge(t.Context(), options)
			if err != nil || replacement.RebatchOf != first.ReceiptPath {
				t.Fatalf("native completed additive rebatch fixture: %+v, %v", replacement, err)
			}
			// Actual removal models the durable replacement-before-ack crash window.
			if err := os.Remove(rebatchPath(first.ReceiptPath)); err != nil {
				t.Fatal(err)
			}
			if mode == "noncontaining replacement" {
				runEngineGit(t, replacement.Candidate.Worktree, "reset", "--hard", replacement.TargetSHA)
				writeEngineFile(t, filepath.Join(replacement.Candidate.Worktree, "different.txt"), "native noncontaining branch\n")
				runEngineGit(t, replacement.Candidate.Worktree, "add", "different.txt")
				runEngineGit(t, replacement.Candidate.Worktree, "commit", "-m", "native negative original-candidate ancestry")
				replacement.Candidate.SHA = strings.TrimSpace(runEngineGit(t, replacement.Candidate.Worktree, "rev-parse", "HEAD"))
				if err := persistWorktreeMergeReceipt(replacement); err != nil {
					t.Fatal(err)
				}
				if _, err := validateMergeAcknowledgementCandidate(t.Context(), f.engine.githubDir, replacement, replacement.Candidate); err != nil {
					t.Fatalf("negative native DAG retained actual custody: %v", err)
				}
			}
			before, err := os.ReadFile(replacement.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "held lock" {
				lock, err := AcquireOperationLock(f.engine.githubDir, replacement.Lane, true)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = lock.Release() }()
			}
			sentinel := errors.New("exact locked rebatch observation refused")
			var locked, fault atomic.Bool
			read := func(path string) (WorktreeMergeReceipt, error) {
				if path == replacement.ReceiptPath && mode != "held lock" {
					probe, lockErr := AcquireOperationLock(f.engine.githubDir, replacement.Lane, true)
					if lockErr == nil {
						if err := probe.Release(); err != nil {
							t.Fatal(err)
						}
					} else {
						// Native exclusivity, not a read-call count, identifies this slot.
						locked.Store(true)
						if fault.CompareAndSwap(false, true) {
							switch mode {
							case "locked read":
								return WorktreeMergeReceipt{}, sentinel
							case "original changed":
								if err := os.WriteFile(first.ReceiptPath, []byte("private malformed original after lock\n"), 0o600); err != nil {
									t.Fatal(err)
								}
							case "replacement changed":
								changed := replacement
								changed.Candidate.SHA = ""
								if err := persistWorktreeMergeReceipt(changed); err != nil {
									t.Fatal(err)
								}
							}
						}
					}
				}
				return readWorktreeMergeReceipt(path)
			}
			var negative atomic.Bool
			options.run = prepareOwnerObservedRunner{Runner: runner.New(), before: func(_ context.Context, dir, name string, args []string) error {
				if !locked.Load() || dir != replacement.Candidate.Worktree || name != "git" {
					return nil
				}
				argv := strings.Join(args, " ")
				if mode == "ack filesystem refusal" && argv == "merge-base "+first.Candidate.SHA+" "+replacement.Candidate.SHA && negative.CompareAndSwap(false, true) {
					if err := os.Mkdir(rebatchPath(first.ReceiptPath), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				reject := mode == "candidate cleanliness" && strings.HasPrefix(argv, "status ") || mode == "ancestry error" && argv == "merge-base "+first.Candidate.SHA+" "+replacement.Candidate.SHA
				if reject && negative.CompareAndSwap(false, true) {
					return sentinel
				}
				return nil
			}}
			got, err := prepareWorktreeMerge(t.Context(), options, read, persistWorktreeMergeReceipt)
			if mode == "success" {
				if err != nil || got.Candidate != replacement.Candidate {
					t.Fatalf("actual locked recovery=%+v, %v", got, err)
				}
				if _, err := readPreparedWorktreeMergeRebatch(rebatchPath(first.ReceiptPath), first); err != nil {
					t.Fatalf("actual recovery did not append acknowledgement: %v", err)
				}
			} else {
				if err == nil || mode != "held lock" && !locked.Load() {
					t.Fatalf("%s did not reach native locked refusal: %+v, %v", mode, got, err)
				}
				if mode == "locked read" || mode == "candidate cleanliness" || mode == "ancestry error" {
					if !errors.Is(err, sentinel) {
						t.Fatalf("exact negative identity lost: %v", err)
					}
				}
				if mode == "replacement changed" && !strings.Contains(err.Error(), "no longer matches the requested immutable rebatch") || mode == "noncontaining replacement" && !strings.Contains(err.Error(), "does not retain original rebatch candidate") {
					t.Fatalf("native reproof diagnostic=%v", err)
				}
				if mode == "ack filesystem refusal" {
					if !negative.Load() {
						t.Fatal("ack fault did not occur after native locked ancestry proof")
					}
					if _, readErr := readPreparedWorktreeMergeRebatch(rebatchPath(first.ReceiptPath), first); readErr == nil {
						t.Fatal("filesystem obstruction became valid acknowledgement evidence")
					}
				} else if _, statErr := os.Stat(rebatchPath(first.ReceiptPath)); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("refusal appended acknowledgement: %v", statErr)
				}
			}
			if mode != "replacement changed" {
				after, readErr := os.ReadFile(replacement.ReceiptPath)
				if readErr != nil || string(after) != string(before) {
					t.Fatalf("recovery changed immutable replacement receipt: %v", readErr)
				}
			}
		})
	}
}

func TestPrepareOwnerNativeOriginalRebatchMergeFaultsPersistPrimaryFailure(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"ancestry", "merge", "merged head", "merged save", "postlock original read", "final ancestry error", "final native DAG drift"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newPrepareOwnerFixture(t)
			options := f.options
			options.Route = WorktreeMergeRoutePullRequest
			options.ValidateLocally = true
			originalSecond := createMergeSource(t, f.engine, "original-second", "feature/original-second", "original-second.txt", "independent original source\n")
			options.Sources = append(options.Sources, originalSecond.WorktreeDir)
			first, err := PrepareWorktreeMerge(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			writeEngineFile(t, filepath.Join(f.source.WorktreeDir, "advanced.txt"), "native advanced source\n")
			runEngineGit(t, f.source.WorktreeDir, "add", "advanced.txt")
			runEngineGit(t, f.source.WorktreeDir, "commit", "-m", "advance source before native rebatch")
			fresh := strings.TrimSpace(runEngineGit(t, f.source.WorktreeDir, "rev-parse", "HEAD"))
			if contains, err := isMergeAncestor(t.Context(), f.source.WorktreeDir, first.Candidate.SHA, fresh); err != nil || contains {
				t.Fatalf("native fixture requires separately retained original candidate: %v, %v", contains, err)
			}
			extra := createMergeSource(t, f.engine, "rebatch-extra", "feature/rebatch-extra", "extra.txt", "extra\n")
			options.Sources = append(options.Sources, extra.WorktreeDir)
			options.RebatchReceipt = first.ReceiptPath
			options.Route = WorktreeMergeRoute("unsupported-owner-route")
			var candidate, integratedHead atomic.Value
			var merging, rejected, integrated atomic.Bool
			sentinel := errors.New("exact original rebatch integration fault")
			options.Progress = func(event progress.Event) {
				if event.Phase == "create_candidate" && event.State == progress.Completed {
					candidate.Store(event.Detail)
				}
				if event.Phase == "integrate_sources" && event.Completed == event.Total && event.Total > 0 {
					integrated.Store(true)
					path, _ := candidate.Load().(string)
					integratedHead.Store(strings.TrimSpace(runEngineGit(t, path, "rev-parse", "HEAD")))
				}
				if mode == "postlock original read" && event.Phase == "acquire_lane" && event.State == progress.Completed && rejected.CompareAndSwap(false, true) {
					if err := os.WriteFile(first.ReceiptPath, []byte("native original corrupted after lock\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			options.run = prepareOwnerObservedRunner{Runner: runner.New(), before: func(_ context.Context, dir, name string, args []string) error {
				path, _ := candidate.Load().(string)
				if path == "" || dir != path || name != "git" {
					return nil
				}
				argv := strings.Join(args, " ")
				isMerge := argv == "merge --no-edit "+first.Candidate.SHA
				if isMerge {
					merging.Store(true)
				}
				nativeIntegratedHead, _ := integratedHead.Load().(string)
				finalProof := integrated.Load() && nativeIntegratedHead != "" && len(args) == 3 && args[0] == "merge-base" && args[1] == first.Candidate.SHA && args[2] == nativeIntegratedHead
				if mode == "final native DAG drift" && finalProof && rejected.CompareAndSwap(false, true) {
					// Actual object replacement occurs only after source rechecks and
					// at the exact final proof. No positive observation is substituted.
					candidateSHA := args[2]
					if strings.TrimSpace(runEngineGit(t, path, "merge-base", first.Candidate.SHA, candidateSHA)) != first.Candidate.SHA {
						t.Fatal("actual final original-candidate ancestry must hold immediately before drift")
					}
					// A replacement rooted at a new commit with the real target as
					// parent retains a connected common base. Replacing directly
					// with the target's payload would instead produce a new root.
					targetTree := strings.TrimSpace(runEngineGit(t, path, "rev-parse", first.TargetSHA+"^{tree}"))
					driftSHA := strings.TrimSpace(runEngineGit(t, path, "commit-tree", targetTree, "-p", first.TargetSHA, "-m", "private post-custody DAG drift"))
					if base := strings.TrimSpace(runEngineGit(t, path, "merge-base", first.Candidate.SHA, driftSHA)); base != first.TargetSHA {
						t.Fatalf("actual alternative commit must share only the receipted target base: %s", base)
					}
					runEngineGit(t, path, "replace", candidateSHA, driftSHA)
					t.Cleanup(func() { runEngineGit(t, path, "replace", "-d", candidateSHA) })
					if base := strings.TrimSpace(runEngineGit(t, path, "merge-base", first.Candidate.SHA, candidateSHA)); base != first.TargetSHA || base == first.Candidate.SHA {
						t.Fatalf("actual private DAG replacement must remove original ancestry while retaining a common base: %s", base)
					}
					return nil
				}
				reject := mode == "ancestry" && strings.HasPrefix(argv, "merge-base "+first.Candidate.SHA+" ") || mode == "merge" && isMerge || mode == "merged head" && merging.Load() && argv == "rev-parse --verify HEAD^{commit}" || mode == "final ancestry error" && finalProof
				if reject && rejected.CompareAndSwap(false, true) {
					return sentinel
				}
				return nil
			}}
			save := func(receipt WorktreeMergeReceipt) error {
				if mode == "merged save" && receipt.Status == WorktreeMergePreparing && receipt.Candidate.SHA != "" && !receipt.Sources[0].Merged && rejected.CompareAndSwap(false, true) {
					return sentinel
				}
				return persistWorktreeMergeReceipt(receipt)
			}
			got, err := prepareWorktreeMerge(t.Context(), options, readWorktreeMergeReceipt, save)
			if !rejected.Load() || err == nil || mode != "postlock original read" && mode != "final native DAG drift" && !errors.Is(err, sentinel) {
				t.Fatalf("native %s did not reach exact rebatch boundary=%+v, %v", mode, got, err)
			}
			if mode == "final native DAG drift" && !strings.Contains(err.Error(), "does not retain rebatched candidate") {
				t.Fatalf("actual temporal DAG refusal=%v", err)
			}
			if mode == "postlock original read" {
				if !reflect.DeepEqual(got, WorktreeMergeReceipt{}) || !strings.Contains(err.Error(), "decode merge receipt") {
					t.Fatalf("actual postlock rebatch original reread=%+v, %v", got, err)
				}
				return
			}
			durable, readErr := readWorktreeMergeReceipt(got.ReceiptPath)
			if readErr != nil || mode != "merged save" && (got.Status != WorktreeMergeConflict || durable.Status != got.Status) || mode == "merged save" && durable.Status != WorktreeMergePreparing {
				t.Fatalf("rebatch refusal durable status=%+v, %v", durable, readErr)
			}
			if _, err := os.Stat(rebatchPath(first.ReceiptPath)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed integration published acknowledgement: %v", err)
			}
		})
	}
}

//nolint:paralleltest // The actual inventory consumes process-wide XDG_CONFIG_HOME; this private native config refusal is serial and restored by testing.
func TestPrepareOwnerNativeCandidateInventoryRefusesPrivateConfig(t *testing.T) {
	f := newPrepareOwnerFixture(t)
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path := filepath.Join(configHome, "wb", "worktrees.yaml")
	var written atomic.Bool
	options := f.options
	options.Progress = func(event progress.Event) {
		if event.Phase == "inspect_sources" && event.State == progress.Completed && written.CompareAndSwap(false, true) {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("worktrees: [invalid YAML\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := PrepareWorktreeMerge(t.Context(), options)
	if !written.Load() || err == nil || !strings.Contains(err.Error(), "inspect candidate lane:") || !reflect.DeepEqual(got, WorktreeMergeReceipt{}) {
		t.Fatalf("actual inventory config refusal=%+v, %v", got, err)
	}
	if _, err := os.Stat(f.receipt.ReceiptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inventory refusal wrote receipt: %v", err)
	}
}

func TestPrepareOwnerNativeAcknowledgedSameSourceCreatesFreshSuccessor(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"supersession", "unpublished failure"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newPrepareOwnerFixture(t)
			options := f.options
			options.Route = WorktreeMergeRoutePullRequest
			options.ValidateLocally = true
			first, err := PrepareWorktreeMerge(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			// Recorded failure selects the receipt protocol; candidate/source
			// custody and every acknowledgement proof below are actual native.
			first.Status = WorktreeMergeValidationFailed
			first.Failure = "recorded owner-protocol failure"
			if err := persistWorktreeMergeReceipt(first); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(first.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "unpublished failure" {
				if _, err := AcknowledgeUnpublishedValidationFailure(t.Context(), WorktreeMergeUnpublishedValidationFailureAcknowledgementOptions{ProjectsRoot: f.engine.githubDir, Receipt: first.ReceiptPath, Apply: true, Actor: "private-fixture", Reason: "native unpublished owner protocol"}); err != nil {
					t.Fatal(err)
				}
			} else {
				runEngineGit(t, f.source.WorktreeDir, "push", "origin", f.source.Branch)
				writeEngineFile(t, filepath.Join(f.engine.canonical, "target.txt"), "native target advance\n")
				runEngineGit(t, f.engine.canonical, "add", "target.txt")
				runEngineGit(t, f.engine.canonical, "commit", "-m", "advance native replacement target")
				runEngineGit(t, f.engine.canonical, "push", "origin", "main")
				replacement := createMergeSource(t, f.engine, "native-replacement", "feature/native-replacement", "replacement.txt", "replacement\n")
				runEngineGit(t, replacement.WorktreeDir, "fetch", "origin")
				runEngineGit(t, replacement.WorktreeDir, "merge", "--no-edit", "origin/"+f.source.Branch)
				if _, err := SupersedeValidationFailedWorktreeMerge(t.Context(), WorktreeMergeValidationFailureSupersessionOptions{ProjectsRoot: f.engine.githubDir, Receipt: first.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir, Apply: true, Actor: "private-fixture", Reason: "native audited successor"}); err != nil {
					t.Fatal(err)
				}
			}
			options.Route = WorktreeMergeRoute("unsupported-owner-route")
			got, err := PrepareWorktreeMerge(t.Context(), options)
			if err == nil || !strings.Contains(err.Error(), "unsupported merge route") || got.ID == first.ID || got.ReceiptPath == first.ReceiptPath || got.Candidate.Worktree == first.Candidate.Worktree || got.Status != WorktreeMergeConflict {
				t.Fatalf("native acknowledged same-source successor=%+v, %v", got, err)
			}
			after, readErr := os.ReadFile(first.ReceiptPath)
			if readErr != nil || string(after) != string(before) {
				t.Fatalf("successor rewrote historical failure evidence: %v", readErr)
			}
		})
	}
}

func TestPrepareOwnerNativeSuccessfulAdmissionIsRecorded(t *testing.T) {
	t.Parallel()
	f := newPrepareOwnerFixture(t)
	options := f.options
	options.Route = WorktreeMergeRoutePullRequest
	options.ValidateLocally = true
	admission := &WorktreeMergeHostLoadAdmission{}
	var called atomic.Bool
	options.RequireHostLoadAdmission = func() (*WorktreeMergeHostLoadAdmission, error) {
		called.Store(true)
		return admission, nil
	}
	got, err := PrepareWorktreeMerge(t.Context(), options)
	if err != nil || !called.Load() || got.HostLoadAdmission != admission || got.Status != WorktreeMergePrepared {
		t.Fatalf("native workflow did not record caller admission: %+v, %v", got, err)
	}
}
