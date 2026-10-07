package worktrees

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

func TestCleanupOfStagedFailureFinalizedThenLandedContinuation(t *testing.T) {
	for _, state := range []string{"landed", "unlanded", "dirty"} {
		t.Run(state, func(t *testing.T) {
			const task = "reviewed-staged-continuation"
			fixture := newGitFixture(t)
			created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
				ProjectsRoot: fixture.projectsRoot, Operation: task, WorkLog: WorkLogOptions{Model: "unknown"},
			})
			if err != nil {
				t.Fatal(err)
			}
			result := created[0]
			base := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")
			feature := filepath.Join(result.WorktreeDir, "feature.txt")
			if err := os.WriteFile(feature, []byte("reviewed staged implementation\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitTest(t, result.WorktreeDir, "add", "feature.txt")
			stagedTree := gitTestOutput(t, result.WorktreeDir, "write-tree")
			if _, err := LogFinalize(context.Background(), LogFinalizeOptions{
				ProjectsRoot: fixture.projectsRoot, Worktree: result.WorktreeDir, Result: "failure",
				Message: "Validated staged implementation handed to reviewer for commit", Report: []byte("reviewed staged tree"),
				Apply: true,
			}); err != nil {
				t.Fatal(err)
			}
			projection, err := readWorkLogProjection(result.WorktreeDir)
			if err != nil {
				t.Fatal(err)
			}
			runRoot := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID)
			terminalPath := filepath.Join(runRoot, "terminals", projection.ClaimID+".json")
			outboxPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "outbox", projection.RunID+"-"+projection.ClaimID+"-sealed.json")
			terminalBefore, err := os.ReadFile(terminalPath)
			if err != nil {
				t.Fatal(err)
			}
			var terminal workLogTerminalRecord
			if err := json.Unmarshal(terminalBefore, &terminal); err != nil {
				t.Fatal(err)
			}
			if terminal.Disposition != "not_landed" || terminal.FinalCommit != base || terminal.FinalizeReport == nil || terminal.FinalizeReport.Result != "failure" {
				t.Fatalf("staged review did not seal failure at original base: %#v", terminal)
			}
			outboxBefore, err := os.ReadFile(outboxPath)
			if err != nil {
				t.Fatal(err)
			}
			gitTest(t, result.WorktreeDir, "commit", "-m", "commit reviewed implementation")
			head := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")
			if tree := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD^{tree}"); tree != stagedTree {
				t.Fatalf("reviewed staged tree changed: %s != %s", tree, stagedTree)
			}
			gitTest(t, result.WorktreeDir, "push", "-u", "origin", result.Branch)
			if state != "unlanded" {
				gitTest(t, fixture.canonical, "merge", "--no-ff", result.Branch, "-m", "land reviewed implementation")
				gitTest(t, fixture.canonical, "push", "origin", "main")
			}
			gitTest(t, fixture.canonical, "push", "origin", "--delete", result.Branch)
			if state == "unlanded" {
				installPullRequestResponses(t, "[]", "")
			} else {
				installMergedPullRequestFixture(t, head, time.Now().Add(-time.Hour))
			}
			if state == "dirty" {
				if err := os.WriteFile(feature, []byte("uncommitted follow-up\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			outcome, cleanupErr := Cleanup(context.Background(), CleanupOptions{
				ProjectsRoot: fixture.projectsRoot, Task: task, Apply: true, OlderThan: 0,
			})
			if len(outcome.Results) != 1 {
				t.Fatalf("continuation was omitted from cleanup inventory: %#v, %v", outcome.Results, cleanupErr)
			}
			if state == "landed" {
				if cleanupErr != nil || !outcome.Results[0].Applied {
					t.Fatalf("landed continuation cleanup = %#v, %v", outcome.Results, cleanupErr)
				}
				if _, err := os.Stat(result.WorktreeDir); !os.IsNotExist(err) {
					t.Fatalf("landed worktree remains: %v", err)
				}
				var record workLogCleanupRecord
				body, err := os.ReadFile(filepath.Join(runRoot, "cleanups", projection.ClaimID+".json"))
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(body, &record); err != nil {
					t.Fatal(err)
				}
				if record.TerminalFinalCommit != base || record.FinalCommit != head || record.CleanedAt.IsZero() {
					t.Fatalf("additive cleanup lost original failure or landed head: %#v", record)
				}
			} else {
				if outcome.Results[0].Applied || outcome.Results[0].Eligible || outcome.Results[0].Reason == "" {
					t.Fatalf("%s continuation was removed", state)
				}
				if _, err := os.Stat(result.WorktreeDir); err != nil {
					t.Fatalf("refused continuation worktree was touched: %v", err)
				}
				if _, err := os.Stat(filepath.Join(runRoot, "cleanups", projection.ClaimID+".json")); !os.IsNotExist(err) {
					t.Fatalf("refused continuation gained cleanup authority: %v", err)
				}
			}
			for path, before := range map[string][]byte{terminalPath: terminalBefore, outboxPath: outboxBefore} {
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("cleanup rewrote original finalized failure %s: %v", path, err)
				}
			}
		})
	}
}

func TestFailedFinalizeCleanupRequiresExactAuthorityAndStrictDescendance(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"success", "not authorized", "missing report", "different base", "open outbox", "read outbox", "wrong outbox", "not descended"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			projection, claim, terminal, event := testTerminalCleanupAuthority(t)
			claim.BaseSHA = terminal.FinalCommit
			terminal.Claim.BaseSHA = claim.BaseSHA
			terminal.Disposition = "not_landed"
			terminal.FinalizeReport = &worktreeclaims.FinalizeReport{Result: "failure", ReportPath: "private-report.md"}
			event.BaseSHA, event.Disposition, event.FinalizeReport = claim.BaseSHA, terminal.Disposition, terminal.FinalizeReport
			if stage == "missing report" {
				terminal.FinalizeReport = nil
			}
			if stage == "different base" {
				terminal.FinalCommit = "different"
			}
			failure := errors.New(stage)
			writes := 0
			ports := advancedCleanupPorts{
				allowFailedFinalize: stage != "not authorized",
				openClaim: func(string, string, workLogProjection) (*lockedWorkLogRun, workLogClaim, error) {
					return testLockedTerminalRun(t), claim, nil
				},
				openChild: func(*os.File, string, bool) (*os.File, error) { return os.Open(t.TempDir()) },
				readJSON: func(_ *os.File, _ string, value any) error {
					switch target := value.(type) {
					case *workLogTerminalRecord:
						*target = terminal
					case *workLogPublicEvent:
						if stage == "read outbox" {
							return failure
						}
						*target = event
						if stage == "wrong outbox" {
							target.FinalCommit = "other"
						}
					case *workLogCleanupRecord:
						return os.ErrNotExist
					}
					return nil
				},
				openOutbox: func(string, string, bool) (*os.File, error) {
					if stage == "open outbox" {
						return nil, failure
					}
					return os.Open(t.TempDir())
				},
				isAncestor: func(context.Context, string, string, string) (bool, error) { return stage != "not descended", nil },
				patchEquivalent: func(context.Context, string, string, string) (bool, error) {
					t.Fatal("failed finalize must never authorize a history rewrite using patch IDs")
					return false, nil
				},
				now: time.Now,
				writeImmutable: func(_ *os.File, name string, _ any, _ bool) error {
					if name == claim.RunID+"-"+claim.ClaimID+"-sealed.json" {
						t.Fatal("rewrote failure outbox")
					}
					writes++
					return nil
				},
			}
			err := ports.acceptAdvancedCleanupTerminal("home", "worktree", "current", projection)
			if stage == "success" {
				if err != nil || writes != 2 {
					t.Fatalf("continuation = %v, writes = %d", err, writes)
				}
			} else if err == nil || writes != 0 {
				t.Fatalf("%s accepted or wrote cleanup authority: %v, writes = %d", stage, err, writes)
			}
		})
	}
}
