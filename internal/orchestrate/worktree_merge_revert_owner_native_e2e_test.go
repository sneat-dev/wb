//go:build e2e

package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// This is recorded historical eligibility, not a substitute for the existing public Land journey.
type revertHistoryFixture struct {
	engine  engineFixture
	receipt WorktreeMergeReceipt
	patch   string
}

func newRevertHistoryFixture(t *testing.T, id string, malformedPolicy bool) revertHistoryFixture {
	t.Helper()
	f := newExplicitRootEngineFixture(t)
	before := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	writeEngineFile(t, filepath.Join(f.canonical, "landed.txt"), "historical landing "+id+"\n")
	runEngineGit(t, f.canonical, "add", "landed.txt")
	runEngineGit(t, f.canonical, "commit", "-m", "record private historical landing")
	landing := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	if malformedPolicy {
		writeEngineFile(t, filepath.Join(f.canonical, ".wb", "quality.yaml"), "version: [invalid\n")
		runEngineGit(t, f.canonical, "add", ".wb/quality.yaml")
		runEngineGit(t, f.canonical, "commit", "-m", "retain malformed current target policy")
	}
	runEngineGit(t, f.canonical, "push", "origin", "HEAD:refs/heads/main")
	patch := runEngineGit(t, f.canonical, "diff", "--binary", before, landing)
	if !strings.Contains(patch, "+historical landing "+id) {
		t.Fatalf("native historical delta = %q", patch)
	}
	patchPath := filepath.Join(t.TempDir(), "inverse.patch")
	if err := os.WriteFile(patchPath, []byte(patch), 0o600); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, f.canonical, "apply", "--check", "--reverse", patchPath)
	home, err := wbhome.Root(f.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	r := WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, ID: id, Repository: f.repository.Slug, Target: "main", Status: WorktreeMergeLanded, Phase: WorktreeMergePhasePrepare, PreviousTargetSHA: before, LandingSHA: landing, Candidate: WorktreeMergeCandidate{SHA: landing}, ReceiptPath: filepath.Join(home, "reports", "worktree-merge", id+".json")}
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	return revertHistoryFixture{f, r, patch}
}

// Every successful observation uses the native runner; only one exact stage may refuse.
type revertStageRunner struct {
	runner.Runner
	t              *testing.T
	fixture        revertHistoryFixture
	refuse         string
	sentinel       error
	calls          []string
	dir, patchPath string
	consumed       int
	afterHEAD      func()
}

func (r *revertStageRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	r.t.Helper()
	if name != "git" {
		r.t.Fatalf("unexpected revert command %s %v", name, args)
	}
	if r.dir == "" {
		r.dir = dir
		r.t.Cleanup(func() {
			_, _, err := runCommand(context.Background(), defaultRunner, 5*time.Second, 0, r.fixture.engine.canonical, "git", "worktree", "remove", "--force", dir)
			if err != nil {
				r.t.Errorf("owned revert worktree cleanup: %v", err)
			}
		})
		guard, err := worktrees.Guard(ctx, dir, worktrees.GuardOptions{ProjectsRoot: r.fixture.engine.githubDir, Base: "main"})
		if err != nil || guard.Transient || guard.Branch != "wb/revert/"+r.fixture.receipt.ID {
			r.t.Fatalf("native revert custody = %+v,%v", guard, err)
		}
		view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: r.fixture.engine.githubDir, Worktree: dir, IncludePromptBodies: true})
		if err != nil || view.Claim == nil || view.Claim.Task != "revert-"+r.fixture.receipt.ID || view.Claim.Repository != r.fixture.receipt.Repository || view.Claim.Branch != guard.Branch || view.Claim.BaseSHA != view.Git.Head || view.OriginalPrompt == nil || view.OriginalPrompt.SHA256 == "" || !strings.Contains(view.OriginalPrompt.Body, r.fixture.receipt.Repository) {
			r.t.Fatalf("native immutable revert work log = %+v,%v", view, err)
		}
	}
	if dir != r.dir {
		r.t.Fatalf("revert invocation changed directory: %q != %q", dir, r.dir)
	}
	stage := ""
	switch {
	case reflect.DeepEqual(args, []string{"diff", "--binary", r.fixture.receipt.PreviousTargetSHA, r.fixture.receipt.LandingSHA}):
		stage = "diff"
	case len(args) == 5 && reflect.DeepEqual(args[:4], []string{"apply", "--check", "--3way", "--reverse"}):
		stage = "apply-check"
		r.patchPath = args[4]
	case len(args) == 5 && reflect.DeepEqual(args[:4], []string{"apply", "--3way", "--reverse", "--index"}):
		stage = "apply"
		if args[4] != r.patchPath {
			r.t.Fatal("apply changed exact patch")
		}
	case reflect.DeepEqual(args, []string{"commit", "-m", "revert: reverse worktree merge " + r.fixture.receipt.ID}):
		stage = "commit"
	case reflect.DeepEqual(args, []string{"rev-parse", "--verify", "HEAD^{commit}"}):
		stage = "HEAD"
	default:
		r.t.Fatalf("unexpected revert argv %v", args)
	}
	expected := []string{"diff", "apply-check", "apply", "commit", "HEAD"}
	if len(r.calls) >= len(expected) || stage != expected[len(r.calls)] {
		r.t.Fatalf("revert stage order %v then %s", r.calls, stage)
	}
	r.calls = append(r.calls, stage)
	if stage == r.refuse {
		r.consumed++
		return runner.Result{}, r.sentinel
	}
	result, err := r.Runner.RunOpts(ctx, dir, opts, name, args...)
	if stage == "diff" && err == nil && result.CombinedOutput != r.fixture.patch {
		r.t.Fatal("created candidate binary diff changed historical delta")
	}
	if stage == "HEAD" && err == nil && r.afterHEAD != nil {
		r.afterHEAD()
	}
	return result, err
}

func TestE2EForwardRevertRecordedHistoryHasExactStageRefusals(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"diff", "apply-check", "apply", "commit", "HEAD"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			f := newRevertHistoryFixture(t, "stage-"+strings.ToLower(stage), false)
			sentinel := errors.New("selected native revert " + stage + " refused")
			run := &revertStageRunner{Runner: defaultRunner, t: t, fixture: f, refuse: stage, sentinel: sentinel}
			got, err := prepareWorktreeMergeRevertWithRunner(t.Context(), f.engine.githubDir, f.receipt.ReceiptPath, 5*time.Second, 0, nil, run)
			if !errors.Is(err, sentinel) || run.consumed != 1 || run.calls[len(run.calls)-1] != stage {
				t.Fatalf("selected stage %s = %+v,%v;calls=%v consumed=%d", stage, got, err, run.calls, run.consumed)
			}
			durable, readErr := readWorktreeMergeReceipt(f.receipt.ReceiptPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			switch stage {
			case "diff":
				if !reflect.DeepEqual(got, f.receipt) || !reflect.DeepEqual(durable, f.receipt) {
					t.Fatalf("diff refusal mutated receipt: %+v / %+v", got, durable)
				}
			case "HEAD":
				if got.Phase != WorktreeMergePhaseRevert || got.Status != WorktreeMergePrepared || got.Candidate.Worktree != run.dir || got.Candidate.SHA != "" || got.RevertOf == nil || got.RevertOf.LandingSHA != f.receipt.LandingSHA || got.PreviousTargetSHA != "" || got.LandingSHA != "" || !reflect.DeepEqual(durable, f.receipt) {
					t.Fatalf("HEAD refusal partial/durable receipt = %+v / %+v", got, durable)
				}
				if _, statErr := os.Stat(filepath.Join(run.dir, "landed.txt")); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("genuine committed inverse retained landed file: %v", statErr)
				}
			default:
				if got.Status != WorktreeMergeConflict || got.Phase != f.receipt.Phase || !reflect.DeepEqual(got, durable) || !strings.Contains(got.Failure, sentinel.Error()) {
					t.Fatalf("conflict checkpoint = %+v / %+v", got, durable)
				}
				if stage == "apply-check" && !strings.HasPrefix(err.Error(), "forward revert conflicts with current target: ") {
					t.Fatalf("apply-check context lost: %v", err)
				}
			}
			if run.patchPath != "" {
				if _, e := os.Stat(run.patchPath); !errors.Is(e, os.ErrNotExist) {
					t.Fatalf("patch scratch survived selected %s: %v", stage, e)
				}
			}
		})
	}
}

func TestE2EForwardRevertAdmissionPreservesRecordedState(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"empty input", "malformed receipt", "prepared revert", "failed revert", "missing before", "missing landing", "invalid repository"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newRevertHistoryFixture(t, "admission-"+strings.ReplaceAll(mode, " ", "-"), false)
			r := f.receipt
			input := r.ReceiptPath
			want := ""
			switch mode {
			case "empty input":
				input = ""
				want = "candidate worktree or receipt is required"
			case "malformed receipt":
				if err := os.WriteFile(input, []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = "decode merge receipt"
			case "prepared revert":
				r.Phase = WorktreeMergePhaseRevert
				r.Status = WorktreeMergePrepared
			case "failed revert":
				r.Phase = WorktreeMergePhaseRevert
				r.Status = WorktreeMergeConflict
				want = "receipt already represents a forward revert with status conflict"
			case "missing before":
				r.PreviousTargetSHA = ""
				want = "receipt has no landed before/after target identity to revert"
			case "missing landing":
				r.LandingSHA = ""
				want = "receipt has no landed before/after target identity to revert"
			case "invalid repository":
				r.Repository = "../unsafe"
				want = "repository"
			}
			if mode != "malformed receipt" {
				if err := persistWorktreeMergeReceipt(r); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(r.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			got, err := PrepareWorktreeMergeRevert(t.Context(), f.engine.githubDir, input, 5*time.Second, 0)
			if mode == "prepared revert" {
				if err != nil || !reflect.DeepEqual(got, r) {
					t.Fatalf("prepared revert idempotence = %+v,%v", got, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("admission %s = %+v,%v want %q", mode, got, err, want)
			}
			after, e := os.ReadFile(r.ReceiptPath)
			if e != nil || string(after) != string(before) {
				t.Fatalf("admission durable record changed: %v", e)
			}
		})
	}
}

//nolint:paralleltest // Owned TMPDIR scopes native patch scratch cleanup; process environment requires serial rows.
func TestE2EForwardRevertPatchScratchRefusalsFollowNativeCreation(t *testing.T) {
	for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepWrite, filewrite.StepClose} {
		//nolint:paralleltest // Each row sets owned TMPDIR to inspect native scratch cleanup; process environment requires serial execution.
		t.Run(string(step), func(t *testing.T) {
			f := newRevertHistoryFixture(t, "scratch-"+string(step), false)
			scratchRoot := t.TempDir()
			t.Setenv("TMPDIR", scratchRoot)
			sentinel := errors.New("exact later patch scratch refused")
			run := &revertStageRunner{Runner: defaultRunner, t: t, fixture: f}
			observed := false
			inj := &filewrite.Injector{Step: step, Err: sentinel, Skip: 1, Hook: func() {
				if !reflect.DeepEqual(run.calls, []string{"diff"}) || run.dir == "" {
					t.Fatalf("patch scratch fired before genuine Create/diff: %v", run.calls)
				}
				observed = true
			}}
			if step == filewrite.StepOpenOrCreate {
				inj.Name = "wb-worktree-revert-*.patch"
				inj.Skip = 0
			}
			got, err := prepareWorktreeMergeRevertWithRunner(t.Context(), f.engine.githubDir, f.receipt.ReceiptPath, 5*time.Second, 0, inj, run)
			if !observed || !errors.Is(err, sentinel) || !reflect.DeepEqual(got, f.receipt) {
				t.Fatalf("late scratch %s = %+v,%v observed=%v", step, got, err, observed)
			}
			files, e := os.ReadDir(scratchRoot)
			if e != nil {
				t.Fatal(e)
			}
			for _, entry := range files {
				if strings.HasPrefix(entry.Name(), "wb-worktree-revert-") || strings.HasPrefix(entry.Name(), "wb-worktree-merge-prompt-") {
					t.Fatalf("owned patch/prompt scratch survived refusal: %s", entry.Name())
				}
			}
			durable, e := readWorktreeMergeReceipt(f.receipt.ReceiptPath)
			if e != nil || !reflect.DeepEqual(durable, f.receipt) {
				t.Fatalf("scratch refusal durable receipt = %+v,%v", durable, e)
			}
		})
	}
}

func TestE2EForwardRevertNativeValidationAndSaveRefusals(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"validation", "save"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newRevertHistoryFixture(t, "late-"+mode, mode == "validation")
			run := &revertStageRunner{Runner: defaultRunner, t: t, fixture: f}
			var original []byte
			if mode == "save" {
				var err error
				original, err = os.ReadFile(f.receipt.ReceiptPath)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if e := os.Remove(f.receipt.ReceiptPath); e != nil && !errors.Is(e, os.ErrNotExist) {
						t.Error(e)
					}
					if e := os.WriteFile(f.receipt.ReceiptPath, original, 0o600); e != nil {
						t.Error(e)
					}
				})
				run.afterHEAD = func() {
					if err := os.Remove(f.receipt.ReceiptPath); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(f.receipt.ReceiptPath, 0o700); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := prepareWorktreeMergeRevertWithRunner(t.Context(), f.engine.githubDir, f.receipt.ReceiptPath, 5*time.Second, 0, nil, run)
			if err == nil || !reflect.DeepEqual(run.calls, []string{"diff", "apply-check", "apply", "commit", "HEAD"}) || got.Candidate.SHA == "" || got.Phase != WorktreeMergePhaseRevert || got.RevertOf == nil {
				t.Fatalf("late native %s = %+v,%v calls=%v", mode, got, err, run.calls)
			}
			if _, e := os.Stat(filepath.Join(run.dir, "landed.txt")); !errors.Is(e, os.ErrNotExist) {
				t.Fatalf("late native inverse retained historical file: %v", e)
			}
			if mode == "validation" {
				durable, e := readWorktreeMergeReceipt(f.receipt.ReceiptPath)
				if !strings.Contains(err.Error(), "forward revert candidate validation failed: load candidate quality policy:") || got.Status != WorktreeMergeValidationFailed || e != nil || !reflect.DeepEqual(durable, got) {
					t.Fatalf("native validation refusal checkpoint = %+v / %+v,%v / %v", got, durable, err, e)
				}
				if body, e := os.ReadFile(filepath.Join(run.dir, ".wb", "quality.yaml")); e != nil || string(body) != "version: [invalid\n" {
					t.Fatalf("native inverse changed malformed current policy: %q,%v", body, e)
				}
			} else {
				if got.Status != WorktreeMergePrepared || got.Validation.Status != quality.StatusPassed {
					t.Fatalf("save refusal lost genuinely validated return: %+v", got)
				}
				info, e := os.Stat(f.receipt.ReceiptPath)
				if e != nil || !info.IsDir() {
					t.Fatalf("actual save refusal destination = %v,%v", info, e)
				}
				if !strings.Contains(err.Error(), f.receipt.ReceiptPath) {
					t.Fatalf("native save error lost destination: %v", err)
				}
			}
			if _, e := os.Stat(run.patchPath); !errors.Is(e, os.ErrNotExist) {
				t.Fatalf("late native patch survived: %v", e)
			}
		})
	}
}
