//go:build e2e

package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
)

//nolint:paralleltest // The native fixture sets process-wide Git and WB environment.
func TestE2ELogInitOwnerFailureRetainsDurableInit(t *testing.T) {
	fixture, worktree, _, claimPath := logVerbPhaseCreated(t, "log-tail-owner")
	claimBefore := logVerbFileBytes(t, claimPath)
	head := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	path := logVerbEventsPath(worktree)
	retained := path + ".before-owner"
	corrupt := []byte("{invalid-owner-journal\n")
	var nativeErr error
	var durable []byte
	called := false
	got, err := logInitWithOwner(context.Background(), LogInitOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree}, func(root, effort, agent, model string, pid int) (OwnerRegistration, error) {
		called = true
		events, readErr := readLocalEvents(root)
		if readErr != nil || len(events) == 0 || events[len(events)-1].Type != LocalEventInit {
			t.Fatalf("owner boundary did not follow durable init: %+v, %v", events, readErr)
		}
		durable = logVerbFileBytes(t, path)
		if renameErr := os.Rename(path, retained); renameErr != nil {
			t.Fatal(renameErr)
		}
		if writeErr := os.WriteFile(path, corrupt, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		owner, ownerErr := recordOwner(root, effort, agent, model, pid)
		nativeErr = ownerErr
		return owner, ownerErr
	})
	if !called || nativeErr == nil || !errors.Is(err, nativeErr) || !reflect.DeepEqual(got, LogVerbResult{}) {
		t.Fatalf("native owner refusal = %+v, %v; called=%v native=%v", got, err, called, nativeErr)
	}
	logVerbAssertFileBytes(t, "retained durable init", retained, durable)
	logVerbAssertFileBytes(t, "invalid replacement journal", path, corrupt)
	logVerbAssertFileBytes(t, "private claim", claimPath, claimBefore)
	if after := gitTestOutput(t, worktree, "rev-parse", "HEAD"); after != head {
		t.Fatalf("owner refusal changed HEAD: %s -> %s", head, after)
	}
}

//nolint:paralleltest // The native fixture sets process-wide Git and WB environment.
func TestE2ELogRefreshRecordsNativeDivergenceFailure(t *testing.T) {
	fixture, worktree, _, _ := logVerbPhaseCreated(t, "log-tail-refresh")
	head := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	gitDir := gitTestOutput(t, worktree, "rev-parse", "--absolute-git-dir")
	headPath := filepath.Join(gitDir, "HEAD")
	headBytes := logVerbFileBytes(t, headPath)
	t.Cleanup(func() {
		if err := os.WriteFile(headPath, headBytes, 0o600); err != nil {
			t.Error(err)
		}
	})
	observed := &logVerbTailBreakHeadAfterFetch{Runner: runner.New(), t: t, root: worktree, headPath: headPath, headBytes: headBytes}
	got, err := LogRefresh(withGitRunner(context.Background(), observed), LogRefreshOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Base: "main"})
	if err != nil || !got.Applied || !observed.changed || !observed.restored || observed.countError == nil || got.Event == nil || got.Event.Target == nil || got.Event.Target.SHA != head || got.Event.Type != LocalEventRefresh || got.Event.Conflict != "" {
		t.Fatalf("native divergence failure = %+v, %v; changed=%v count=%v", got, err, observed.changed, observed.countError)
	}
	detail := strings.TrimSpace(observed.countOutput)
	if detail == "" || !strings.Contains(strings.Join(got.Notes, "\n"), detail) {
		t.Fatalf("native divergence diagnostic missing: %q, %v", detail, got.Notes)
	}
	if got.Event.Target.Ahead != 0 || got.Event.Target.Behind != 0 {
		t.Fatalf("failed divergence query published counts: %+v", got.Event.Target)
	}
	logVerbAssertFileBytes(t, "HEAD restored at native count boundary", headPath, headBytes)
	if after := gitTestOutput(t, worktree, "rev-parse", "HEAD"); after != head {
		t.Fatalf("refresh changed HEAD: %s -> %s", head, after)
	}
	if refs := gitTestOutput(t, worktree, "for-each-ref", "--format=%(refname)", "refs/wb/fetch-base/"); refs != "" {
		t.Fatalf("refresh retained private fetch refs: %q", refs)
	}
	events, readErr := readLocalEvents(worktree)
	if readErr != nil || len(events) == 0 || !reflect.DeepEqual(events[len(events)-1].Target, got.Event.Target) {
		t.Fatalf("refresh evidence was not durable: %+v, %v", events, readErr)
	}
}

// All commands run native Git. The actual HEAD is made invalid only after the
// successful native fetch-ref cleanup, isolating the subsequent count query.
type logVerbTailBreakHeadAfterFetch struct {
	runner.Runner
	t              *testing.T
	root, headPath string
	headBytes      []byte
	restored       bool
	changed        bool
	countError     error
	countOutput    string
}

func (r *logVerbTailBreakHeadAfterFetch) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	result, err := r.Runner.RunOpts(ctx, dir, options, name, args...)
	if name == "git" && dir == r.root && len(args) >= 3 && args[0] == "-C" && args[1] == dir {
		if err == nil && len(args) == 5 && args[2] == "update-ref" && args[3] == "-d" && strings.HasPrefix(args[4], "refs/wb/fetch-base/") {
			if writeErr := os.WriteFile(r.headPath, []byte("invalid-native-HEAD\n"), 0o600); writeErr != nil {
				r.t.Fatal(writeErr)
			}
			r.changed = true
		}
		if len(args) == 6 && reflect.DeepEqual(args[2:5], []string{"rev-list", "--left-right", "--count"}) {
			r.countError, r.countOutput = err, result.CombinedOutput
			if restoreErr := os.WriteFile(r.headPath, r.headBytes, 0o600); restoreErr != nil {
				r.t.Fatal(restoreErr)
			}
			r.restored = true
		}
	}
	return result, err
}

//nolint:paralleltest // The native fixture sets process-wide Git and WB environment.
func TestE2ELogFinalizeUsesValidatedClaimEffortForBlankTask(t *testing.T) {
	fixture, worktree, projection, claimPath := logVerbPhaseCreated(t, "log-tail-blank-task")
	var claim workLogClaim
	if err := json.Unmarshal(logVerbFileBytes(t, claimPath), &claim); err != nil {
		t.Fatal(err)
	}
	claim.Task = ""
	claimBytes, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claimPath, claimBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	control, _, _, err := activeWorkLogClaim(fixture.home, worktree)
	if err != nil || control.Task != "" || control.ClaimID != projection.ClaimID {
		t.Fatalf("blank task claim failed native admission: %+v, %v", control, err)
	}
	head := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	report := []byte("native blank-task finalize evidence\n")
	got, err := LogFinalize(context.Background(), LogFinalizeOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Result: "failure", Apply: true, Report: report})
	if err != nil || !got.Applied {
		t.Fatalf("blank task finalize = %+v, %v", got, err)
	}
	var terminal workLogTerminalRecord
	terminalPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID, "terminals", projection.ClaimID+".json")
	if err := json.Unmarshal(logVerbFileBytes(t, terminalPath), &terminal); err != nil {
		t.Fatal(err)
	}
	name, err := finalizeReportFileName(claim.EffortID, claim.Repository)
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(filepath.Dir(filepath.Dir(terminalPath)), "reports", name)
	if terminal.FinalizeReport == nil || terminal.FinalizeReport.ReportPath != expected || terminal.FinalCommit != head || terminal.Disposition != string(AbortNotLanded) {
		t.Fatalf("blank task terminal/report = %+v; want path %s", terminal, expected)
	}
	logVerbAssertFileBytes(t, "effort-named report", expected, report)
	logVerbAssertFileBytes(t, "private blank-task claim", claimPath, claimBytes)
	if after := gitTestOutput(t, worktree, "rev-parse", "HEAD"); after != head {
		t.Fatalf("finalize changed HEAD: %s -> %s", head, after)
	}
}

//nolint:paralleltest // The native fixture sets process-wide Git and WB environment.
func TestE2ELogFinalizeRepairFailureRetainsSealedTerminal(t *testing.T) {
	fixture, worktree, projection, claimPath := logVerbPhaseCreated(t, "log-tail-sealed-repair")
	claimBefore := logVerbFileBytes(t, claimPath)
	head := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	terminalPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID, "terminals", projection.ClaimID+".json")
	path := logVerbEventsPath(worktree)
	retained := path + ".before-final-repair"
	corrupt := []byte("{invalid-finalize-journal\n")
	var nativeErr error
	var terminalBytes, durable []byte
	called := false
	got, err := logFinalizeWithRepair(context.Background(), LogFinalizeOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Result: "failure", Apply: true}, func(root string) (LocalWorkLogProjection, error) {
		called = true
		terminalBytes = logVerbFileBytes(t, terminalPath)
		var terminal workLogTerminalRecord
		if decodeErr := json.Unmarshal(terminalBytes, &terminal); decodeErr != nil || terminal.ClaimID != projection.ClaimID || terminal.FinalCommit != head || terminal.Disposition != string(AbortNotLanded) {
			t.Fatalf("repair boundary preceded valid terminal seal: %+v, %v", terminal, decodeErr)
		}
		events, readErr := readLocalEvents(root)
		if readErr != nil || len(events) == 0 || events[len(events)-1].Type != LocalEventFinalize || events[len(events)-1].Result != "failure" {
			t.Fatalf("repair boundary preceded durable finalize: %+v, %v", events, readErr)
		}
		durable = logVerbFileBytes(t, path)
		if renameErr := os.Rename(path, retained); renameErr != nil {
			t.Fatal(renameErr)
		}
		if writeErr := os.WriteFile(path, corrupt, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		repaired, repairErr := repairCurrentLocalProjection(root)
		nativeErr = repairErr
		return repaired, repairErr
	})
	if !called || nativeErr == nil || !errors.Is(err, nativeErr) || !strings.Contains(err.Error(), "repair local work-log projection after finalize") || !reflect.DeepEqual(got, LogVerbResult{}) {
		t.Fatalf("native post-seal repair refusal = %+v, %v; called=%v native=%v", got, err, called, nativeErr)
	}
	logVerbAssertFileBytes(t, "immutable sealed terminal", terminalPath, terminalBytes)
	logVerbAssertFileBytes(t, "retained finalize journal", retained, durable)
	logVerbAssertFileBytes(t, "invalid replacement journal", path, corrupt)
	logVerbAssertFileBytes(t, "private claim", claimPath, claimBefore)
	if after := gitTestOutput(t, worktree, "rev-parse", "HEAD"); after != head {
		t.Fatalf("post-seal refusal changed HEAD: %s -> %s", head, after)
	}
}
