package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

func testLockedTerminalRun(t *testing.T) *lockedWorkLogRun {
	t.Helper()
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &lockedWorkLogRun{directory: directory, unlock: func() {}}
}

func TestCleanupClaimFenceReportsEachAuthorityFailure(t *testing.T) {
	t.Parallel()
	projection := workLogProjection{EffortID: "task", RunID: "run", ClaimID: "claim"}
	failure := errors.New("injected authority failure")
	for _, stage := range []string{"open run", "lock", "read projection", "projection changed", "open claims", "read claim", "success"} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			ports := cleanupClaimPorts{
				openRun: func(string, string, string, bool) (*os.File, string, error) {
					if stage == "open run" {
						return nil, "", failure
					}
					file, err := os.Open(t.TempDir())
					return file, "", err
				},
				lockClaim: func(*os.File, string) (func(), error) {
					if stage == "lock" {
						return nil, failure
					}
					return func() {}, nil
				},
				readProjection: func(string) (workLogProjection, error) {
					if stage == "read projection" {
						return workLogProjection{}, failure
					}
					if stage == "projection changed" {
						return workLogProjection{}, nil
					}
					return projection, nil
				},
				openChild: func(*os.File, string, bool) (*os.File, error) {
					if stage == "open claims" {
						return nil, failure
					}
					return os.Open(t.TempDir())
				},
				readJSON: func(_ *os.File, _ string, value any) error {
					if stage == "read claim" {
						return failure
					}
					*value.(*workLogClaim) = workLogClaim{ClaimID: "claim"}
					return nil
				},
			}
			locked, claim, err := ports.openCheckedCleanupClaim("home", "worktree", projection)
			if stage == "success" {
				if err != nil || locked == nil || claim.ClaimID != "claim" {
					t.Fatalf("successful fence = %#v, %#v, %v", locked, claim, err)
				}
				locked.close()
			} else if err == nil {
				t.Fatalf("%s unexpectedly succeeded", stage)
			}
		})
	}
}

func TestRecycleSealKeepsClaimFenceThroughTerminalAndProjection(t *testing.T) {
	t.Parallel()
	projection := workLogProjection{EffortID: "task", RunID: "run", ClaimID: "claim", Lifecycle: "active"}
	claim := workLogClaim{ClaimID: "claim"}
	failure := errors.New("injected seal failure")
	for _, stage := range []string{"legacy", "projection", "open claim", "corroborate", "terminal", "projection write", "success"} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			locked := false
			ports := recycleSealPorts{
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
					run := testLockedTerminalRun(t)
					run.unlock = func() { locked = false }
					locked = true
					return run, claim, nil
				},
				corroborate: func(string, string, string, workLogProjection, workLogClaim) error {
					if !locked {
						t.Fatal("corroboration lost claim fence")
					}
					if stage == "corroborate" {
						return failure
					}
					return nil
				},
				sealTerminal: func(_ string, _ *os.File, request worktreeclaims.TerminalSealRequest) (time.Time, error) {
					if !locked || request.Claim.ClaimID != "claim" || request.FinalCommit != "head" || request.Disposition != "removed" {
						t.Fatalf("terminal request without exact held authority: %#v", request)
					}
					if stage == "terminal" {
						return time.Time{}, failure
					}
					return time.Unix(1, 0), nil
				},
				writeProjection: func(_ string, next workLogProjection) error {
					if !locked || next.Lifecycle != "terminal" {
						t.Fatalf("projection advanced outside held fence: %#v", next)
					}
					if stage == "projection write" {
						return failure
					}
					return nil
				},
			}
			err := ports.sealWorkLogForRecycleWithEvidence("home", "worktree", "head", "removed", worktreeclaims.TerminalEvidence{})
			if stage == "legacy" || stage == "success" {
				if err != nil {
					t.Fatalf("%s = %v", stage, err)
				}
			} else if !errors.Is(err, failure) {
				t.Fatalf("%s = %v", stage, err)
			}
			if locked {
				t.Fatal("claim fence leaked")
			}
		})
	}
}

func TestCleanupSealDistinguishesExactAdvancedAndNewTerminal(t *testing.T) {
	t.Parallel()
	projection := workLogProjection{ClaimID: "claim"}
	failure := errors.New("exact terminal not accepted")
	for _, stage := range []string{"legacy", "projection", "exact", "new", "advanced", "advanced refusal"} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			proof := &worktreeclaims.LandedEvidence{Target: "main"}
			sealed := false
			ports := cleanupSealPorts{
				readProjection: func(string, string) (workLogProjection, error) {
					if stage == "legacy" {
						return workLogProjection{}, errWorkLogProjectionNotFound
					}
					if stage == "projection" {
						return workLogProjection{}, failure
					}
					return projection, nil
				},
				acceptExisting: func(string, string, string) error {
					if stage == "exact" {
						return nil
					}
					return failure
				},
				hasTerminal: func(string, workLogProjection) bool { return stage == "advanced" || stage == "advanced refusal" },
				acceptAdvanced: func(string, string, string, workLogProjection) error {
					if stage == "advanced refusal" {
						return errors.New("advanced proof rejected")
					}
					return nil
				},
				sealRemoval: func(_, _, _ string, landed *worktreeclaims.LandedEvidence) error {
					if stage != "new" || landed != proof {
						t.Fatalf("%s sealed a new terminal with %#v", stage, landed)
					}
					sealed = true
					return nil
				},
			}
			err := ports.sealWorkLogForCleanup("home", "worktree", "head", proof)
			if sealed != (stage == "new") {
				t.Fatalf("%s: new terminal sealed = %t", stage, sealed)
			}
			if stage == "projection" && !errors.Is(err, failure) {
				t.Fatalf("projection = %v", err)
			}
			if stage == "advanced refusal" && !errors.Is(err, failure) {
				t.Fatalf("advanced refusal = %v", err)
			}
			if stage != "projection" && stage != "advanced refusal" && err != nil {
				t.Fatalf("%s = %v", stage, err)
			}
		})
	}
}

func TestTerminalFacadeLegacyAndInvalidHomePaths(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	if err := sealWorkLogForRecycle("home", worktree, "head", "removed"); err != nil {
		t.Fatalf("legacy recycle: %v", err)
	}
	if err := sealWorkLogForSupersession("home", worktree, "head", &SupersessionReceipt{Version: 1}); err != nil {
		t.Fatalf("legacy supersession: %v", err)
	}
	if err := preflightWorkLogSeal("home", worktree, "head"); err != nil {
		t.Fatalf("legacy preflight: %v", err)
	}
	if err := preflightWorkLogSealForCleanup(context.Background(), "home", t.TempDir(), ListResult{WorktreeDir: worktree}); err != nil {
		t.Fatalf("legacy cleanup preflight: %v", err)
	}
	if hasExistingWorkLogTerminal(filepath.Join(t.TempDir(), "absent"), workLogProjection{EffortID: "task", RunID: "run"}) {
		t.Fatal("missing private run yielded a terminal")
	}
	valid := TerminalWorkLogExpectation{Task: "task", Repository: "acme/app", Worktree: "/tmp/removed",
		Branch: "wb/task", FinalCommit: strings.Repeat("a", 40)}
	if err := ValidateRemovedTerminalWorkLogs("\x00", []TerminalWorkLogExpectation{valid}); err == nil {
		t.Fatal("invalid projects root accepted by batch reader")
	}
	if _, err := ReadRemovedTerminalWorkLogClaimBase("\x00", valid); err == nil {
		t.Fatal("invalid projects root accepted by base reader")
	}
	if matched, err := defaultPreflightSealPorts().legacy(context.Background(), "home", t.TempDir(), ListResult{
		OpenPullRequest: &PullRequest{URL: "https://example.test/pull/1"},
	}); err == nil || matched || !strings.Contains(err.Error(), "open pull request") {
		t.Fatalf("default read-only legacy proof with open PR = %t, %v", matched, err)
	}
}

func TestDiscardedSealUsesAdvancedAuthorityOnlyForImmutableConflict(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"sealed", "ordinary failure", "projection failed", "advanced failed", "advanced accepted"} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			failure := errors.New("ordinary seal failure")
			ports := discardedSealPorts{
				seal: func(_, _, _, disposition string, evidence worktreeclaims.TerminalEvidence) error {
					if disposition != string(AbortDiscarded) || evidence.DirtyCapture == nil {
						t.Fatalf("discarded seal lost evidence: %#v", evidence)
					}
					if stage == "sealed" {
						return nil
					}
					if stage == "ordinary failure" {
						return failure
					}
					return errImmutableTerminalConflict
				},
				readProjection: func(string, string) (workLogProjection, error) {
					if stage == "projection failed" {
						return workLogProjection{}, failure
					}
					return workLogProjection{ClaimID: "claim"}, nil
				},
				acceptAdvanced: func(string, string, string, workLogProjection) error {
					if stage == "advanced failed" {
						return failure
					}
					return nil
				},
			}
			err := ports.sealDiscardedWorkLogAfterAbsorbedByProof("home", "worktree", "head", &DirtyWorktreeEvidence{SHA256: "digest"})
			if stage == "sealed" || stage == "advanced accepted" {
				if err != nil {
					t.Fatalf("%s = %v", stage, err)
				}
			} else if stage == "ordinary failure" {
				if !errors.Is(err, failure) {
					t.Fatalf("ordinary failure = %v", err)
				}
			} else if !errors.Is(err, errImmutableTerminalConflict) {
				t.Fatalf("%s lost original immutable conflict: %v", stage, err)
			}
		})
	}
}

func TestTerminalPreflightReadsWithoutMutationAndChecksLegacyProof(t *testing.T) {
	t.Parallel()
	failure := errors.New("ordinary corroboration refused")
	for _, stage := range []string{"absent", "read error", "corroboration error", "ordinary success", "legacy error", "legacy unmatched", "legacy matched"} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			ports := preflightSealPorts{
				readProjection: func(string, string) (workLogProjection, error) {
					if stage == "absent" {
						return workLogProjection{}, errWorkLogProjectionNotFound
					}
					if stage == "read error" {
						return workLogProjection{}, failure
					}
					return workLogProjection{ClaimID: "claim"}, nil
				},
				readReadOnly: func(string) (workLogProjection, error) {
					if stage == "absent" {
						return workLogProjection{}, errWorkLogProjectionNotFound
					}
					if stage == "read error" {
						return workLogProjection{}, failure
					}
					return workLogProjection{ClaimID: "claim"}, nil
				},
				corroborate: func(string, string, string, workLogProjection) error {
					if stage == "ordinary success" {
						return nil
					}
					return failure
				},
				legacy: func(context.Context, string, string, ListResult) (bool, error) {
					if stage == "legacy error" {
						return false, errors.New("legacy proof failed")
					}
					return stage == "legacy matched", nil
				},
			}
			sealErr := ports.preflightWorkLogSeal("home", "worktree", "head")
			if stage == "absent" || stage == "ordinary success" {
				if sealErr != nil {
					t.Fatalf("%s seal preflight = %v", stage, sealErr)
				}
			} else if !errors.Is(sealErr, failure) {
				t.Fatalf("%s seal preflight = %v", stage, sealErr)
			}
			cleanupErr := ports.preflightWorkLogSealForCleanup(context.Background(), "home", "root", ListResult{WorktreeDir: "worktree", HeadSHA: "head"})
			switch stage {
			case "absent", "ordinary success", "legacy matched":
				if cleanupErr != nil {
					t.Fatalf("%s cleanup preflight = %v", stage, cleanupErr)
				}
			case "legacy error":
				if cleanupErr == nil || !strings.Contains(cleanupErr.Error(), "legacy proof failed") {
					t.Fatalf("legacy error = %v", cleanupErr)
				}
			default:
				if !errors.Is(cleanupErr, failure) {
					t.Fatalf("%s cleanup preflight = %v", stage, cleanupErr)
				}
			}
		})
	}
}
