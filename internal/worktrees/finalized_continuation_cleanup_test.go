package worktrees

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/worktreeclaims"
	"os"
	"testing"
	"time"
)

func TestFailedFinalizeCleanupRequiresExactAuthorityAndStrictDescendance(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"success", "not authorized", "missing report", "different base", "open outbox", "read outbox", "wrong outbox", "not descended"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			projection, claim, terminal, event := testTerminalCleanupAuthority(t)
			claim.BaseSHA = terminal.FinalCommit
			terminal.BaseSHA = claim.BaseSHA
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
