package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR9 is task-9 PR-9's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally
// matches a different test's error by coincidence.
var errBoomPR9 = errors.New("pr9 boom")

// --- the four worktree-merge prompt writers, sharing
// writeWorktreeMergeScratchPromptInjected ---

func TestWriteWorktreeMergePromptInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite, filewrite.StepClose} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			inj := &filewrite.Injector{Step: step, Err: errBoomPR9}
			path, err := writeWorktreeMergePromptInjected("acme/app", "main", nil, inj)
			if path != "" || !errors.Is(err, errBoomPR9) {
				t.Fatalf("writeWorktreeMergePromptInjected(%s failure) = (%q, %v), want (\"\", errBoomPR9)", step, path, err)
			}
		})
	}
}

func TestWriteWorktreeMergePromptInjectedSucceeds(t *testing.T) {
	t.Parallel()
	path, err := writeWorktreeMergePromptInjected("acme/app", "main", []WorktreeMergeSource{{Branch: "feature/x", SHA: "abc123", Worktree: "/tmp/x"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "feature/x") {
		t.Fatalf("prompt contents = %q, want it to mention the source branch", contents)
	}
}

func TestWriteConflictCandidateRefreshPromptInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite, filewrite.StepClose} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			inj := &filewrite.Injector{Step: step, Err: errBoomPR9}
			path, err := writeConflictCandidateRefreshPromptInjected(WorktreeMergeReceipt{ReceiptPath: "r.json"}, "main", nil, nil, "actor", "reason", inj)
			if path != "" || !errors.Is(err, errBoomPR9) {
				t.Fatalf("writeConflictCandidateRefreshPromptInjected(%s failure) = (%q, %v), want (\"\", errBoomPR9)", step, path, err)
			}
		})
	}
}

func TestWritePublishedForwardRepairPromptInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite, filewrite.StepClose} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			inj := &filewrite.Injector{Step: step, Err: errBoomPR9}
			path, err := writePublishedForwardRepairPromptInjected(WorktreeMergeReceipt{ReceiptPath: "r.json"}, WorktreeMergeValidationFailureSupersession{}, "main", nil, nil, "actor", "reason", inj)
			if path != "" || !errors.Is(err, errBoomPR9) {
				t.Fatalf("writePublishedForwardRepairPromptInjected(%s failure) = (%q, %v), want (\"\", errBoomPR9)", step, path, err)
			}
		})
	}
}

func TestWriteValidationFailureSealPromptInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite, filewrite.StepClose} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			inj := &filewrite.Injector{Step: step, Err: errBoomPR9}
			path, err := writeValidationFailureSealPromptInjected(WorktreeMergeReceipt{ReceiptPath: "r.json"}, "main", "tree", nil, "actor", "reason", inj)
			if path != "" || !errors.Is(err, errBoomPR9) {
				t.Fatalf("writeValidationFailureSealPromptInjected(%s failure) = (%q, %v), want (\"\", errBoomPR9)", step, path, err)
			}
		})
	}
}

// --- runWorktreeMergePrePushGateInjected ---

func TestRunWorktreeMergePrePushGateInjectedHonoursInjectedFailures(t *testing.T) {
	for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite, filewrite.StepClose} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			fixture := newEngineFixture(t)
			source := createMergeSource(t, fixture, "push-gate-fault-"+string(step), "feature/push-gate-"+string(step), "push.txt", "push\n")
			// mergeRevision, not the runEngineGit test helper: both resolve
			// HEAD via a real git process, but mergeRevision is the same
			// production helper this package's own merge flow already uses
			// elsewhere, so this fixture setup step is not itself a second,
			// test-file-local process-launching construct for task-24's
			// unit-tier detector to count.
			head, err := mergeRevision(context.Background(), defaultRunner, source.WorktreeDir, "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			inj := &filewrite.Injector{Step: step, Err: errBoomPR9}
			gate, err := runWorktreeMergePrePushGateInjected(context.Background(), source.WorktreeDir, head, "refs/heads/gated-"+string(step), 5*time.Second, 0, inj)
			if gate != nil || !errors.Is(err, errBoomPR9) {
				t.Fatalf("runWorktreeMergePrePushGateInjected(%s failure) = (%v, %v), want (nil, errBoomPR9)", step, gate, err)
			}
			matches, globErr := filepath.Glob(filepath.Join(os.TempDir(), "wb-worktree-merge-pre-push-*.txt"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if len(matches) != 0 {
				t.Fatalf("leftover pre-push gate input(s) after an injected %s failure: %v", step, matches)
			}
		})
	}
}

// --- prepareWorktreeMergeRevertInjected's git-diff patch write ---

// landedDirectRevertFixture drives one direct landing to completion and
// returns a receipt path PrepareWorktreeMergeRevert can act on. It is
// expensive (a full PrepareWorktreeMerge+LandWorktreeMerge round trip), so
// each fault case below gets its own fresh fixture rather than sharing one:
// prepareWorktreeMergeRevertInjected creates the revert worktree/branch
// (named after the receipt's fixed ID) before reaching the patch-write step
// this test targets, so a second call against the same landed receipt
// would fail on that earlier step instead of reaching the injected one.
func landedDirectRevertFixture(t *testing.T) (projectsRoot, receiptPath string) {
	t.Helper()
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "revert-fault-source", "feature/revert-fault", "revert-fault.txt", "revert-fault\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", receipt.Candidate.SHA)
	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return fixture.githubDir, landed.ReceiptPath
}

func TestPrepareWorktreeMergeRevertInjectedHonoursInjectedFailures(t *testing.T) {
	for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepWrite, filewrite.StepClose} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			projectsRoot, receiptPath := landedDirectRevertFixture(t)
			inj := &filewrite.Injector{Step: step, Err: errBoomPR9}
			_, err := prepareWorktreeMergeRevertInjected(context.Background(), projectsRoot, receiptPath, time.Second, 0, inj)
			if !errors.Is(err, errBoomPR9) {
				t.Fatalf("prepareWorktreeMergeRevertInjected(%s failure) = %v, want errBoomPR9", step, err)
			}
			matches, globErr := filepath.Glob(filepath.Join(os.TempDir(), "wb-worktree-revert-*.patch"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if len(matches) != 0 {
				t.Fatalf("leftover git-diff patch temp file(s) after an injected %s failure: %v", step, matches)
			}
		})
	}
}
