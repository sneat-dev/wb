package worktrees

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func testTerminalCleanupAuthority(t *testing.T) (workLogProjection, workLogClaim, workLogTerminalRecord, workLogPublicEvent) {
	t.Helper()
	projection := workLogProjection{EffortID: "task", RunID: "run", ClaimID: "claim", Lifecycle: "terminal"}
	claim := workLogClaim{Version: 2, EffortID: "task", RunID: "run", ClaimID: "claim", Repository: "acme/app",
		Branch: "wb/task", Base: "main", BaseSHA: "base", Lifecycle: "active"}
	terminalClaim := claim
	terminalClaim.Lifecycle = "terminal"
	terminal := workLogTerminalRecord{Claim: terminalClaim, FinalCommit: "sealed", Disposition: "landed", SealedAt: time.Unix(123, 0).UTC()}
	event := workLogPublicEvent{Version: 1, Type: "worktree.sealed", At: terminal.SealedAt,
		EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID, Repository: claim.Repository,
		Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA, FinalCommit: terminal.FinalCommit,
		Lifecycle: "terminal", Disposition: terminal.Disposition}
	return projection, claim, terminal, event
}

func TestExistingCleanupTerminalCorroboratesEveryPrivateRecord(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"legacy", "projection", "open claim", "corroborate", "open terminal", "read terminal",
		"wrong terminal", "open outbox", "read outbox", "wrong outbox", "repair projection", "repair local", "success", "stale projection"} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			projection, claim, terminal, event := testTerminalCleanupAuthority(t)
			if stage == "stale projection" {
				projection.Lifecycle = "active"
			}
			failure := errors.New(stage)
			ports := existingCleanupPorts{
				readProjection: func(string, string) (workLogProjection, error) {
					if stage == "legacy" {
						return workLogProjection{}, errWorkLogProjectionNotFound
					}
					if stage == "projection" {
						return workLogProjection{}, failure
					}
					return projection, nil
				},
				openClaim: func(string, string, workLogProjection) (*lockedWorkLogRun, workLogClaim, error) {
					if stage == "open claim" {
						return nil, claim, failure
					}
					return testLockedTerminalRun(t), claim, nil
				},
				corroborate: func(string, string, string, workLogProjection, workLogClaim) error {
					if stage == "corroborate" {
						return failure
					}
					return nil
				},
				openChild: func(*os.File, string, bool) (*os.File, error) {
					if stage == "open terminal" {
						return nil, failure
					}
					return os.Open(t.TempDir())
				},
				readJSON: func(_ *os.File, _ string, value any) error {
					switch target := value.(type) {
					case *workLogTerminalRecord:
						if stage == "read terminal" {
							return failure
						}
						*target = terminal
						if stage == "wrong terminal" {
							target.Disposition = "discarded"
						}
					case *workLogPublicEvent:
						if stage == "read outbox" {
							return failure
						}
						*target = event
						if stage == "wrong outbox" {
							target.Disposition = "discarded"
						}
					}
					return nil
				},
				openOutbox: func(string, string, bool) (*os.File, error) {
					if stage == "open outbox" {
						return nil, failure
					}
					return os.Open(t.TempDir())
				},
				writeProjection: func(_ string, next workLogProjection) error {
					if next.Lifecycle != "terminal" {
						t.Fatalf("repair lifecycle = %q", next.Lifecycle)
					}
					if stage == "repair projection" {
						return failure
					}
					return nil
				},
				repairLocal: func(string) error {
					if stage == "repair local" {
						return failure
					}
					return nil
				},
			}
			if stage == "repair projection" || stage == "repair local" {
				projection.Lifecycle = "active"
			}
			err := ports.acceptExistingCleanupTerminal("home", "worktree", "sealed")
			switch stage {
			case "legacy", "success", "stale projection":
				if err != nil {
					t.Fatalf("%s = %v", stage, err)
				}
			case "wrong terminal", "wrong outbox":
				if err == nil || !strings.Contains(err.Error(), "does not") {
					t.Fatalf("%s = %v", stage, err)
				}
			default:
				if !errors.Is(err, failure) {
					t.Fatalf("%s = %v", stage, err)
				}
			}
		})
	}
}

func TestAdvancedCleanupKeepsTerminalAndPublishesAdditiveReceipt(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"open claim", "open terminal", "read terminal", "wrong terminal", "same head",
		"ancestor error", "patch error", "patch unequal", "open cleanups", "conflicting cleanup", "inspect cleanup",
		"write cleanup", "open outbox", "write outbox", "success", "retry"} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			projection, claim, terminal, _ := testTerminalCleanupAuthority(t)
			failure := errors.New(stage)
			sealedAt := terminal.SealedAt
			writes := 0
			ports := advancedCleanupPorts{
				openClaim: func(string, string, workLogProjection) (*lockedWorkLogRun, workLogClaim, error) {
					if stage == "open claim" {
						return nil, claim, failure
					}
					return testLockedTerminalRun(t), claim, nil
				},
				openChild: func(_ *os.File, name string, _ bool) (*os.File, error) {
					if (stage == "open terminal" && name == "terminals") || (stage == "open cleanups" && name == "cleanups") {
						return nil, failure
					}
					return os.Open(t.TempDir())
				},
				readJSON: func(_ *os.File, _ string, value any) error {
					switch target := value.(type) {
					case *workLogTerminalRecord:
						if stage == "read terminal" {
							return failure
						}
						*target = terminal
						if stage == "wrong terminal" {
							target.Disposition = "not_landed"
						}
					case *workLogCleanupRecord:
						if stage == "inspect cleanup" {
							return failure
						}
						if stage == "retry" || stage == "conflicting cleanup" {
							*target = workLogCleanupRecord{TerminalFinalCommit: "sealed", FinalCommit: "current", CleanedAt: sealedAt}
							if stage == "conflicting cleanup" {
								target.FinalCommit = "other"
							}
							return nil
						}
						return os.ErrNotExist
					}
					return nil
				},
				isAncestor: func(context.Context, string, string, string) (bool, error) {
					if stage == "ancestor error" {
						return false, failure
					}
					if strings.HasPrefix(stage, "patch") {
						return false, nil
					}
					return true, nil
				},
				patchEquivalent: func(context.Context, string, string, string) (bool, error) {
					if stage == "patch error" {
						return false, failure
					}
					return stage != "patch unequal", nil
				},
				now: func() time.Time { return time.Unix(456, 0) },
				writeImmutable: func(_ *os.File, _ string, value any, idempotent bool) error {
					writes++
					if stage == "write cleanup" && !idempotent {
						return failure
					}
					if stage == "write outbox" && idempotent {
						return failure
					}
					if event, ok := value.(workLogPublicEvent); ok && stage == "retry" && !event.At.Equal(sealedAt) {
						t.Fatalf("retry changed original cleanup authority time: %s", event.At)
					}
					return nil
				},
				openOutbox: func(string, string, bool) (*os.File, error) {
					if stage == "open outbox" {
						return nil, failure
					}
					return os.Open(t.TempDir())
				},
			}
			finalCommit := "current"
			if stage == "same head" {
				finalCommit = "sealed"
			}
			err := ports.acceptAdvancedCleanupTerminal("home", "worktree", finalCommit, projection)
			switch stage {
			case "success", "retry":
				if err != nil {
					t.Fatalf("%s = %v", stage, err)
				}
				if stage == "success" && writes != 2 {
					t.Fatalf("new cleanup writes = %d", writes)
				}
				if stage == "retry" && writes != 1 {
					t.Fatalf("retry cleanup writes = %d", writes)
				}
			case "wrong terminal", "same head", "patch unequal", "conflicting cleanup":
				if err == nil {
					t.Fatalf("%s accepted", stage)
				}
			default:
				if !errors.Is(err, failure) {
					t.Fatalf("%s = %v", stage, err)
				}
			}
		})
	}
}
