package worktrees

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

func orphanedAbortTestSetup(t *testing.T) (AbortOptions, orphanedAbortPorts) {
	t.Helper()
	claim := workLogClaim{ClaimID: strings.Repeat("a", 64), EffortID: "task", RunID: "run", Task: "task",
		Repository: "acme/app", Worktree: "/vanished", Branch: "wb/task"}
	candidate := orphanedClaimCandidate{home: t.TempDir(), claim: claim, terminalPath: "/no-terminal"}
	ports := orphanedAbortPorts{
		find: func(string, string, string) (orphanedClaimCandidate, error) { return candidate, nil },
		inspect: func(_ context.Context, _ string, _ orphanedClaimCandidate, actor, reason string) (*workLogOrphanedEvidence, error) {
			return &workLogOrphanedEvidence{Version: 1, Actor: actor, Reason: reason,
				WorktreeAbsent: true, RegistrationAbsent: true, LocalBranchAbsent: true,
				RemoteBranchAbsent: true, TerminalAbsent: true}, nil
		},
		openRun: func(string, string, string, bool) (*os.File, string, error) {
			dir, err := os.Open(t.TempDir())
			return dir, "", err
		},
		lock:    func(*os.File, string) (func(), error) { return func() {}, nil },
		recheck: func(*os.File, workLogClaim) error { return nil },
		seal: func(string, *os.File, worktreeclaims.TerminalSealRequest) (time.Time, error) {
			return time.Unix(1, 0), nil
		},
	}
	return AbortOptions{ProjectsRoot: t.TempDir(), Task: "task", ClaimID: claim.ClaimID,
		Actor: "founder", Reason: "negative evidence checked", Apply: true}, ports
}

func TestOrphanedAbortRejectsInvalidSelectionBeforeEvidenceScan(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		edit func(*AbortOptions)
		want string
	}{
		{"claim ID", func(o *AbortOptions) { o.ClaimID = "invalid" }, "exact --claim ID"},
		{"actor", func(o *AbortOptions) { o.Actor = "\n" }, "single-line --actor"},
		{"reason", func(o *AbortOptions) { o.Reason = "" }, "single-line --reason"},
		{"remote deletion", func(o *AbortOptions) { o.DeleteRemote = true }, "--remote deletion"},
		{"all tasks", func(o *AbortOptions) { o.All = true }, "--all"},
		{"base", func(o *AbortOptions) { o.Base = "invalid branch?" }, "invalid base branch"},
		{"task", func(o *AbortOptions) { o.Task = "" }, "task is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options, ports := orphanedAbortTestSetup(t)
			tc.edit(&options)
			ports.find = func(string, string, string) (orphanedClaimCandidate, error) {
				t.Fatal("invalid selection reached private claim lookup")
				return orphanedClaimCandidate{}, nil
			}
			if _, err := ports.abortOrphanedClaim(context.Background(), options); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s error = %v, want %q", tc.name, err, tc.want)
			}
		})
	}
}

func TestOrphanedAbortKeepsNegativeProofThroughClaimLock(t *testing.T) {
	t.Parallel()
	options, ports := orphanedAbortTestSetup(t)
	beforeSeal := false
	options.beforeOrphanSeal = func() { beforeSeal = true }
	sealed := false
	ports.seal = func(_ string, _ *os.File, request worktreeclaims.TerminalSealRequest) (time.Time, error) {
		sealed = true
		if request.Claim.ClaimID != options.ClaimID || request.Disposition != string(AbortOrphaned) ||
			request.Evidence.Orphaned == nil || !request.Evidence.Orphaned.TerminalAbsent {
			t.Fatalf("orphaned seal request = %#v", request)
		}
		return time.Unix(1, 0), nil
	}
	results, err := ports.abortOrphanedClaim(context.Background(), options)
	if err != nil || len(results) != 1 || !results[0].Applied || !sealed || !beforeSeal {
		t.Fatalf("orphaned seal result = %#v, err = %v, sealed = %v, callback = %v", results, err, sealed, beforeSeal)
	}
	options.Apply = false
	sealed = false
	results, err = ports.abortOrphanedClaim(context.Background(), options)
	if err != nil || len(results) != 1 || results[0].Applied || sealed {
		t.Fatalf("dry run result = %#v, err = %v, sealed = %v", results, err, sealed)
	}
}

func TestOrphanedAbortRefusesEvidenceAndStorageFailures(t *testing.T) {
	t.Parallel()
	failure := errors.New("injected orphan authority failure")
	cases := []struct {
		name string
		edit func(*AbortOptions, *orphanedAbortPorts)
		want string
	}{
		{"claim lookup", func(_ *AbortOptions, p *orphanedAbortPorts) {
			p.find = func(string, string, string) (orphanedClaimCandidate, error) { return orphanedClaimCandidate{}, failure }
		}, "injected orphan authority failure"},
		{"filter", func(o *AbortOptions, _ *orphanedAbortPorts) { o.Filter = "other/repository" }, "does not match --filter"},
		{"first negative scan", func(_ *AbortOptions, p *orphanedAbortPorts) {
			p.inspect = func(context.Context, string, orphanedClaimCandidate, string, string) (*workLogOrphanedEvidence, error) {
				return nil, failure
			}
		}, "not orphaned"},
		{"run open", func(_ *AbortOptions, p *orphanedAbortPorts) {
			p.openRun = func(string, string, string, bool) (*os.File, string, error) { return nil, "", failure }
		}, "open orphaned claim run"},
		{"claim fence", func(_ *AbortOptions, p *orphanedAbortPorts) {
			p.lock = func(*os.File, string) (func(), error) { return nil, failure }
		}, "lock orphaned claim"},
		{"immutable recheck", func(_ *AbortOptions, p *orphanedAbortPorts) {
			p.recheck = func(*os.File, workLogClaim) error { return failure }
		}, "identity changed under lock"},
		{"negative recheck", func(_ *AbortOptions, p *orphanedAbortPorts) {
			original := p.inspect
			reads := 0
			p.inspect = func(ctx context.Context, root string, c orphanedClaimCandidate, actor, reason string) (*workLogOrphanedEvidence, error) {
				reads++
				if reads == 2 {
					return nil, failure
				}
				return original(ctx, root, c, actor, reason)
			}
		}, "safety changed under lock"},
		{"terminal seal", func(_ *AbortOptions, p *orphanedAbortPorts) {
			p.seal = func(string, *os.File, worktreeclaims.TerminalSealRequest) (time.Time, error) {
				return time.Time{}, failure
			}
		}, "seal orphaned Work Log claim"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options, ports := orphanedAbortTestSetup(t)
			tc.edit(&options, &ports)
			if _, err := ports.abortOrphanedClaim(context.Background(), options); err == nil || !strings.Contains(err.Error(), tc.want) ||
				(tc.name != "filter" && !errors.Is(err, failure)) {
				t.Fatalf("%s error = %v, want %q", tc.name, err, tc.want)
			}
		})
	}
	options, ports := orphanedAbortTestSetup(t)
	ports.inspect = func(context.Context, string, orphanedClaimCandidate, string, string) (*workLogOrphanedEvidence, error) {
		return nil, failure
	}
	options.Apply = false
	results, err := ports.abortOrphanedClaim(context.Background(), options)
	if err != nil || len(results) != 1 || results[0].Eligible {
		t.Fatalf("dry refusal = %#v, err = %v", results, err)
	}
}
