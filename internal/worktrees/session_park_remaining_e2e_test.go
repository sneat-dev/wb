//go:build e2e

package worktrees

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
)

func parkedPreparationOptions(fixture *parkedTargetCompletionFixture) ParkedSessionWorkLogPrepareOptions {
	successor := fixture.successor
	return ParkedSessionWorkLogPrepareOptions{
		ProjectsRoot: fixture.base.projectsRoot, Request: fixture.request, RequestDigest: fixture.digest,
		Member: fixture.member, ReceivedAt: fixture.request.CreatedAt, WorktreeDir: fixture.worktree,
		PinnedCommit: fixture.member.Commit, AttemptID: successor.AttemptID, AttemptIndex: successor.AttemptIndex,
		Session: session.Record{PID: successor.PID, WBSessionID: successor.WBSessionID,
			PredecessorWBSessionID: successor.PredecessorWBSessionID, Machine: successor.TargetMachine,
			Runtime: successor.Runtime, Model: successor.Model, TmuxName: successor.TmuxName,
			HandoffID: successor.HandoffID, StartedAt: successor.StartedAt},
	}
}

func parkedCompletionOptions(fixture *parkedTargetCompletionFixture) ParkedTargetCompletionOptions {
	return ParkedTargetCompletionOptions{ProjectsRoot: fixture.base.projectsRoot, Request: fixture.request,
		RequestDigest: fixture.digest, Member: fixture.member, WorktreeDir: fixture.worktree, Successor: fixture.successor}
}

func parkedTargetRunPath(t *testing.T, fixture *parkedTargetCompletionFixture) (string, string) {
	t.Helper()
	targetValue, err := sessionparkTargetReference(fixture)
	if err != nil {
		t.Fatal(err)
	}
	target, err := sessionmove.ParseWorkLogReference(targetValue)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(fixture.base.home, "worklogs", target.EffortID, "runs", target.RunID), target.ClaimID
}

func sessionparkTargetReference(fixture *parkedTargetCompletionFixture) (string, error) {
	return sessionpark.TargetWorkLogReference(fixture.request, fixture.digest, fixture.member)
}

//nolint:paralleltest // Every native fixture configures process-wide Git and WB environment.
func TestE2EParkedTargetPreparationRefusesDurableStageConflicts(t *testing.T) {
	for _, stage := range []string{"home", "claim lock", "claim", "run index", "manifest", "received event", "owner event", "outbox", "projection"} {
		//nolint:paralleltest // Each subtest builds a native fixture with t.Setenv.
		t.Run(stage, func(t *testing.T) {
			fixture := newParkedTargetCompletionFixture(t, "prepare-"+strings.ReplaceAll(stage, " ", "-"))
			options := parkedPreparationOptions(fixture)
			runPath, claimID := parkedTargetRunPath(t, fixture)
			switch stage {
			case "home":
				blocked := filepath.Join(t.TempDir(), "blocked")
				if err := os.WriteFile(blocked, []byte("file"), 0o600); err != nil {
					t.Fatal(err)
				}
				options.ProjectsRoot = filepath.Join(blocked, "child")
			case "claim lock":
				path := filepath.Join(runPath, "locks", claimID+".lock")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "claim":
				if err := os.WriteFile(filepath.Join(runPath, "claims", claimID+".json"), []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "run index":
				if err := os.WriteFile(filepath.Join(runPath, "run.json"), []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "manifest":
				if err := os.WriteFile(filepath.Join(fixture.worktree, journalRootDirectory, journalLocalDirectory, manifestName), []byte("version: 9\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "received event":
				path := filepath.Join(fixture.worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, "events.jsonl")
				if err := os.WriteFile(path, []byte("{invalid\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "owner event":
				options.AttemptID, options.AttemptIndex = "000002-"+strings.Repeat("2", 32), 2
				prepared, err := prepareParkedTarget(context.Background(), options)
				if err != nil {
					t.Fatal(err)
				}
				conflict := prepared.ownerEvent
				conflict.Message = "a different immutable owner event"
				if _, _, err := appendLocalEventWithoutCustody(fixture.worktree, conflict); err != nil {
					t.Fatal(err)
				}
			case "outbox":
				value, err := sessionparkTargetReference(fixture)
				if err != nil {
					t.Fatal(err)
				}
				target, err := sessionmove.ParseWorkLogReference(value)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(fixture.base.home, "worklogs", target.EffortID, "outbox", target.RunID+"-"+claimID+"-claimed.json")
				if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "projection":
				path := filepath.Join(fixture.worktree, workLogProjectionDirectory)
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("occupied"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			claimPath := filepath.Join(runPath, "claims", claimID+".json")
			claimBefore, claimReadErr := os.ReadFile(claimPath)
			journalPath := filepath.Join(fixture.worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, "events.jsonl")
			journalBefore, journalReadErr := os.ReadFile(journalPath)
			result, err := PrepareParkedSessionWorkLog(context.Background(), options)
			if err == nil || result.WorkLogReference != "" || result.ClaimID != "" {
				t.Fatalf("%s accepted or returned published identity: %+v, %v", stage, result, err)
			}
			want := map[string]string{
				"claim lock": "claim-lock file", "claim": "immutable parked target Work Log claim conflicts",
				"run index": "run index identity mismatch", "manifest": "unsupported worktree manifest version",
				"received event": "parse local work-log event", "owner event": "different immutable evidence",
				"outbox": "immutable file already exists", "projection": "open work-log projection directory",
			}[stage]
			if stage == "home" && !errors.Is(err, syscall.ENOTDIR) || stage != "home" && !strings.Contains(err.Error(), want) {
				t.Fatalf("%s refused at wrong stage: %v, want %q", stage, err, want)
			}
			if claimReadErr == nil {
				after, readErr := os.ReadFile(claimPath)
				if readErr != nil || !bytes.Equal(after, claimBefore) {
					t.Fatalf("%s changed durable claim after refusal: %v", stage, readErr)
				}
			}
			if journalReadErr == nil {
				after, readErr := os.ReadFile(journalPath)
				if readErr != nil || !bytes.Equal(after, journalBefore) {
					t.Fatalf("%s changed local journal after refusal: %v", stage, readErr)
				}
			}
		})
	}
}

//nolint:paralleltest // Every native fixture configures process-wide Git and WB environment.
func TestE2EParkedCompletionRefusesChangedAuthorityBeforeEvent(t *testing.T) {
	for _, stage := range []string{"home", "claim lock", "journal", "journal lock", "claims", "claim bytes", "projection", "live branch", "events", "barrier"} {
		//nolint:paralleltest // Each subtest builds a native fixture with t.Setenv.
		t.Run(stage, func(t *testing.T) {
			fixture := newParkedTargetCompletionFixture(t, "complete-"+strings.ReplaceAll(stage, " ", "-"))
			options := parkedCompletionOptions(fixture)
			runPath, claimID := parkedTargetRunPath(t, fixture)
			journal := filepath.Join(fixture.worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory)
			switch stage {
			case "home":
				blocked := filepath.Join(t.TempDir(), "blocked")
				if err := os.WriteFile(blocked, []byte("file"), 0o600); err != nil {
					t.Fatal(err)
				}
				options.ProjectsRoot = filepath.Join(blocked, "child")
			case "claim lock":
				path := filepath.Join(runPath, "locks", claimID+".lock")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "journal":
				if err := os.Rename(journal, journal+".moved"); err != nil {
					t.Fatal(err)
				}
			case "journal lock":
				path := filepath.Join(journal, localWorkLogLockName)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "claims":
				path := filepath.Join(runPath, "claims")
				if err := os.Rename(path, path+".moved"); err != nil {
					t.Fatal(err)
				}
			case "claim bytes":
				if err := os.WriteFile(filepath.Join(runPath, "claims", claimID+".json"), []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "projection":
				if err := os.WriteFile(filepath.Join(fixture.worktree, workLogProjectionDirectory, workLogProjectionName), []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "live branch":
				gitTest(t, fixture.worktree, "branch", "-m", "changed-after-preparation")
			case "events":
				path := filepath.Join(journal, "events.jsonl")
				if err := os.WriteFile(path, []byte("{invalid\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "barrier":
				options.hooks.beforeCompletionBarrier = func() error { return errors.New("selected barrier refusal") }
			}
			journalPath := filepath.Join(journal, "events.jsonl")
			journalBefore, journalReadErr := os.ReadFile(journalPath)
			_, err := RecordParkedTargetCompleted(options)
			if err == nil {
				t.Fatalf("%s completed without required authority", stage)
			}
			want := map[string]string{
				"claim lock":   "claim-lock file",
				"journal lock": "local work-log journal lock",
				"claim bytes":  "claim does not corroborate receipt member", "projection": "projection conflicts with receipt member",
				"live branch": "does not match private claim", "events": "parse local work-log event", "barrier": "selected barrier refusal",
			}[stage]
			if stage == "home" && !errors.Is(err, syscall.ENOTDIR) ||
				(stage == "journal" || stage == "claims") && !errors.Is(err, os.ErrNotExist) ||
				stage != "home" && stage != "journal" && stage != "claims" && !strings.Contains(err.Error(), want) {
				t.Fatalf("%s refused at wrong stage: %v, want %q", stage, err, want)
			}
			if journalReadErr == nil {
				after, readErr := os.ReadFile(journalPath)
				if readErr != nil || !bytes.Equal(after, journalBefore) {
					t.Fatalf("%s changed local journal after completion refusal: %v", stage, readErr)
				}
			}
			events, readErr := readLocalEvents(fixture.worktree)
			if readErr == nil && eventByID(events, externalLocalEventID("park-target-completed-"+fixture.member.MemberID, fixture.digest, "")) != nil {
				t.Fatalf("%s published completion after refusal", stage)
			}
		})
	}
}

//nolint:paralleltest // The native source fixture configures process-wide Git and WB environment.
func TestE2ESessionMoveWorkLogInspectionRefusesDrift(t *testing.T) {
	for _, stage := range []string{"home", "projection missing", "projection corrupt", "inactive", "claim missing", "run missing", "owners unreadable", "owner mismatch"} {
		//nolint:paralleltest // Each subtest builds a native fixture with t.Setenv.
		t.Run(stage, func(t *testing.T) {
			fixture, worktree, source := newSessionCheckpointFixture(t, "inspect-"+strings.ReplaceAll(stage, " ", "-"))
			projectsRoot := fixture.projectsRoot
			projection := filepath.Join(worktree, workLogProjectionDirectory, workLogProjectionName)
			home, err := wbhomeRootForTest(projectsRoot)
			if err != nil {
				t.Fatal(err)
			}
			claim, _, _, err := activeWorkLogClaim(home, worktree)
			if err != nil {
				t.Fatal(err)
			}
			switch stage {
			case "home":
				blocked := filepath.Join(t.TempDir(), "blocked")
				if err := os.WriteFile(blocked, []byte("file"), 0o600); err != nil {
					t.Fatal(err)
				}
				projectsRoot = filepath.Join(blocked, "child")
			case "projection missing":
				if err := os.Remove(projection); err != nil {
					t.Fatal(err)
				}
			case "projection corrupt":
				if err := os.WriteFile(projection, []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "inactive":
				current, readErr := readWorkLogProjection(worktree)
				if readErr != nil {
					t.Fatal(readErr)
				}
				current.Lifecycle = "terminal"
				wtLifeCovWriteJSON(t, projection, current)
			case "claim missing":
				path := filepath.Join(home, "worklogs", claim.EffortID, "runs", claim.RunID, "claims", claim.ClaimID+".json")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "run missing":
				path := filepath.Join(home, "worklogs", claim.EffortID, "runs", claim.RunID)
				if err := os.Rename(path, path+".moved"); err != nil {
					t.Fatal(err)
				}
			case "owner mismatch":
				source.PID++
			case "owners unreadable":
				path := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, "events.jsonl")
				if err := os.WriteFile(path, []byte("{invalid\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, reference, err := inspectSessionMoveWorkLog(projectsRoot, worktree, source)
			if err == nil || reference != "" {
				t.Fatalf("%s admitted source: reference=%q err=%v", stage, reference, err)
			}
			want := map[string]string{
				"projection missing": "requires an active managed Work Log",
				"projection corrupt": "inspect managed Work Log projection", "inactive": "not active",
				"claim missing": "corroborate managed Work Log", "run missing": "corroborate managed Work Log",
				"owners unreadable": "inspect Work Log owners", "owner mismatch": "does not own the active Work Log",
			}[stage]
			if stage == "home" && !errors.Is(err, syscall.ENOTDIR) || stage != "home" && !strings.Contains(err.Error(), want) {
				t.Fatalf("%s refused at wrong stage: %v, want %q", stage, err, want)
			}
		})
	}
}

//nolint:paralleltest // The native source fixture configures process-wide Git and WB environment.
func TestE2EParkedRemoteValidationRetainsExactPushedSource(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "remote-validate-boundaries")
	branch := preparePushedParkedWorktree(t, fixture, worktree)
	guard, snapshot := captureParkedWorktreeMember(t, fixture, worktree, source, branch)
	prepared := parkedSessionCaptureMember{snapshot: snapshot, resolvedWorktreeDir: worktree,
		listed: ListResult{Repository: snapshot.Repository, CanonicalDir: guard.CanonicalDir,
			WorktreeDir: worktree, WorktreesRoot: guard.WorktreesRoot, Branch: branch}}
	if err := prepared.acquire(context.Background(), fixture.projectsRoot); err != nil {
		t.Fatal(err)
	}
	defer closeParkedSessionMembers([]parkedSessionCaptureMember{prepared})
	if err := validateRemoteParkedMember(context.Background(), fixture.projectsRoot, &prepared); err != nil {
		t.Fatalf("unchanged pushed source failed validation: %v", err)
	}
	for _, tc := range []struct {
		name, want string
		mutate     func(*testing.T, *parkedSessionCaptureMember) context.Context
	}{
		{"snapshot", "clean pushed member", func(_ *testing.T, member *parkedSessionCaptureMember) context.Context {
			member.snapshot.RemoteHead = strings.Repeat("a", 40)
			return context.Background()
		}},
		{"branch observation", "branch, HEAD, or clean status", func(_ *testing.T, member *parkedSessionCaptureMember) context.Context {
			member.query = func(ctx context.Context, args []string, next func() (string, error)) (string, error) {
				if args[0] == "symbolic-ref" {
					return "wrong-branch", nil
				}
				return next()
			}
			return context.Background()
		}},
		{"head observation", "branch, HEAD, or clean status", func(_ *testing.T, member *parkedSessionCaptureMember) context.Context {
			member.query = func(ctx context.Context, args []string, next func() (string, error)) (string, error) {
				if args[0] == "rev-parse" {
					return "", errors.New("HEAD unavailable")
				}
				return next()
			}
			return context.Background()
		}},
		{"remote tip", "remote branch no longer has the exact parked commit", func(_ *testing.T, _ *parkedSessionCaptureMember) context.Context {
			return withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, next func() ([]byte, error)) ([]byte, error) {
				if len(args) > 0 && args[0] == "ls-remote" {
					return nil, errors.New("remote unavailable")
				}
				return next()
			})
		}},
		{"fetch origin", "credential-free origin identity changed after park", func(_ *testing.T, _ *parkedSessionCaptureMember) context.Context {
			return withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, next func() ([]byte, error)) ([]byte, error) {
				if len(args) >= 2 && args[0] == "remote" && args[1] == "get-url" {
					return nil, errors.New("origin unavailable")
				}
				return next()
			})
		}},
		{"push origin", "credential-free origin identity changed after park", func(_ *testing.T, _ *parkedSessionCaptureMember) context.Context {
			return withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, next func() ([]byte, error)) ([]byte, error) {
				if len(args) >= 4 && args[0] == "remote" && args[1] == "get-url" && args[3] == "--push" {
					return []byte("https://github.com/acme/other.git\n"), nil
				}
				return next()
			})
		}},
		{"private claim", "corroborate source Work Log claim", func(t *testing.T, _ *parkedSessionCaptureMember) context.Context {
			home, err := wbhomeRootForTest(fixture.projectsRoot)
			if err != nil {
				t.Fatal(err)
			}
			projection, err := readWorkLogProjection(worktree)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, "worklogs", projection.EffortID, "runs", projection.RunID, "claims", projection.ClaimID+".json")
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.WriteFile(path, original, 0o600); err != nil {
					t.Error(err)
				}
			})
			return context.Background()
		}},
		{"source projection", "active source Work Log claim changed after park", func(t *testing.T, _ *parkedSessionCaptureMember) context.Context {
			path := filepath.Join(worktree, workLogProjectionDirectory, workLogProjectionName)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.WriteFile(path, original, 0o600); err != nil {
					t.Error(err)
				}
			})
			return context.Background()
		}},
		{"journal", "invalid", func(t *testing.T, _ *parkedSessionCaptureMember) context.Context {
			path := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, "events.jsonl")
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("{invalid\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.WriteFile(path, original, 0o600); err != nil {
					t.Error(err)
				}
			})
			return context.Background()
		}},
		{"worktree descriptor", "worktree descriptor changed", func(t *testing.T, member *parkedSessionCaptureMember) context.Context {
			descriptor, err := openAdoptedCleanupWorktree(worktree)
			if err != nil {
				t.Fatal(err)
			}
			member.worktree = descriptor
			descriptor.close()
			return context.Background()
		}},
		{"canonical descriptor", "canonical repository descriptor changed", func(t *testing.T, member *parkedSessionCaptureMember) context.Context {
			descriptor, err := openCanonicalRepository(guard.CanonicalDir)
			if err != nil {
				t.Fatal(err)
			}
			member.canonical = descriptor
			descriptor.close()
			return context.Background()
		}},
	} {
		//nolint:paralleltest // Each subtest shares the held source descriptors and lock.
		t.Run(tc.name, func(t *testing.T) {
			trial := prepared
			ctx := tc.mutate(t, &trial)
			err := validateRemoteParkedMember(ctx, fixture.projectsRoot, &trial)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s validation = %v, want %q refusal", tc.name, err, tc.want)
			}
		})
	}
}
