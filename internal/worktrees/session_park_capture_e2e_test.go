//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionpark"
)

func newParkedCaptureTestMember(t *testing.T, operation string) (*gitFixture, *parkedSessionCaptureMember, session.Record) {
	t.Helper()
	fixture, worktree, source := newSessionCheckpointFixture(t, operation)
	branch := preparePushedParkedWorktree(t, fixture, worktree)
	guard, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot, Admission: AdmissionEnforce})
	if err != nil {
		t.Fatal(err)
	}
	member := &parkedSessionCaptureMember{listed: ListResult{Repository: "acme/app", CanonicalDir: guard.CanonicalDir,
		WorktreeDir: worktree, WorktreesRoot: guard.WorktreesRoot, Branch: branch}}
	t.Cleanup(func() { closeParkedSessionMembers([]parkedSessionCaptureMember{*member}) })
	return fixture, member, source
}

//nolint:paralleltest // fixture configures process-wide Git and agent environment
func TestE2EParkedCaptureSingleAndAggregatePreserveSnapshotAndLockBoundary(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "park-capture-parity")
	branch := preparePushedParkedWorktree(t, fixture, worktree)
	guard, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot, Admission: AdmissionEnforce})
	if err != nil {
		t.Fatal(err)
	}
	listed := ListResult{Repository: "acme/app", CanonicalDir: guard.CanonicalDir, WorktreeDir: worktree,
		WorktreesRoot: guard.WorktreesRoot, Branch: branch}
	journal, err := openJournalSubdirectory(worktree, worklogDirectory, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	unlock, err := lockLocalWorkLog(journal)
	if err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	result := make(chan struct {
		member sessionpark.Worktree
		err    error
	}, 1)
	go func() {
		member, captureErr := CaptureParkedSessionWorktree(context.Background(), fixture.projectsRoot, listed, source)
		result <- struct {
			member sessionpark.Worktree
			err    error
		}{member, captureErr}
	}()
	var single sessionpark.Worktree
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatal(got.err)
		}
		single = got.member
	case <-time.After(5 * time.Second):
		unlock()
		locked = false
		<-result
		t.Fatal("single capture waited for a local Work Log custody lock")
	}
	unlock()
	locked = false
	var aggregate sessionpark.Worktree
	if err := CaptureParkedSessionAggregate(context.Background(), fixture.projectsRoot, []ListResult{listed}, source,
		func(members []sessionpark.Worktree) error {
			if len(members) != 1 {
				t.Fatalf("aggregate members = %d", len(members))
			}
			aggregate = members[0]
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(single, aggregate) || single.OwnerEventID == "" || single.WorkLogReference == "" || single.RemoteHead == "" {
		t.Fatalf("single/aggregate snapshot mismatch: single=%#v aggregate=%#v", single, aggregate)
	}
}

//nolint:paralleltest // fixture configures process-wide Git and agent environment
func TestE2EParkedAggregateReleasesPartialAcquisitionAndCallbackFailure(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "park-capture-release")
	branch := preparePushedParkedWorktree(t, fixture, worktree)
	guard, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot, Admission: AdmissionEnforce})
	if err != nil {
		t.Fatal(err)
	}
	listed := ListResult{Repository: "acme/app", CanonicalDir: guard.CanonicalDir, WorktreeDir: worktree,
		WorktreesRoot: guard.WorktreesRoot, Branch: branch}
	called := false
	err = CaptureParkedSessionAggregate(context.Background(), fixture.projectsRoot, []ListResult{listed, {
		WorktreeDir: filepath.Join(fixture.projectsRoot, "zzzz-not-managed"),
	}}, source, func([]sessionpark.Worktree) error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("partial acquisition = (%v, called=%t)", err, called)
	}
	assertParkedCaptureLockAvailable(t, worktree)
	fault := errors.New("persistence refused")
	err = CaptureParkedSessionAggregate(context.Background(), fixture.projectsRoot, []ListResult{listed}, source,
		func(members []sessionpark.Worktree) error {
			called = true
			if len(members) != 1 {
				t.Fatalf("callback members=%d", len(members))
			}
			return fault
		})
	if !called || !errors.Is(err, fault) {
		t.Fatalf("callback failure = (%v, called=%t)", err, called)
	}
	assertParkedCaptureLockAvailable(t, worktree)
}

func assertParkedCaptureLockAvailable(t *testing.T, worktree string) {
	t.Helper()
	journal, err := openJournalSubdirectory(worktree, worklogDirectory, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		unlock, lockErr := lockLocalWorkLog(journal)
		if lockErr == nil {
			unlock()
		}
		done <- lockErr
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("aggregate retained local custody lock after returning")
	}
}

//nolint:paralleltest // each fixture configures process-wide Git and agent environment
func TestE2EParkedCaptureRefusesChangedGitAndRemoteAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*testing.T, *parkedSessionCaptureMember, string)
		context    func(context.Context) context.Context
	}{
		{"named branch", "named branch changed", func(t *testing.T, member *parkedSessionCaptureMember, worktree string) {
			gitTest(t, worktree, "branch", "-m", "renamed-after-guard")
		}, nil},
		{"fetch missing", "no usable origin fetch remote", func(t *testing.T, _ *parkedSessionCaptureMember, worktree string) {
			gitTest(t, worktree, "remote", "remove", "origin")
		}, nil},
		{"fetch unsafe", "origin fetch remote is unsafe", func(t *testing.T, _ *parkedSessionCaptureMember, worktree string) {
			gitTest(t, worktree, "remote", "set-url", "origin", "https://user:secret@example.invalid/acme/app.git")
		}, nil},
		{"push unsafe", "origin push remote is unsafe", func(t *testing.T, _ *parkedSessionCaptureMember, worktree string) {
			gitTest(t, worktree, "remote", "set-url", "--push", "origin", "https://user:secret@example.invalid/acme/app.git")
		}, nil},
		{"remote identities differ", "identify different repositories", func(t *testing.T, _ *parkedSessionCaptureMember, worktree string) {
			gitTest(t, worktree, "remote", "set-url", "--push", "origin", "https://github.com/acme/other.git")
		}, nil},
		{"listed repository differs", "repository identity changed", func(_ *testing.T, member *parkedSessionCaptureMember, _ string) {
			member.listed.Repository = "other/repo"
		}, nil},
		{"remote tip query fails", "observe parked source remote branch", nil, func(ctx context.Context) context.Context {
			return withCanonicalGitInterceptor(ctx, func(_ context.Context, args []string, next func() ([]byte, error)) ([]byte, error) {
				if len(args) > 0 && args[0] == "ls-remote" {
					return nil, errors.New("remote unavailable")
				}
				return next()
			})
		}},
	} {
		//nolint:paralleltest // each fixture calls t.Setenv and runs native Git
		t.Run(tc.name, func(t *testing.T) {
			fixture, worktree, source := newSessionCheckpointFixture(t, "park-capture-"+strings.ReplaceAll(tc.name, " ", "-"))
			branch := preparePushedParkedWorktree(t, fixture, worktree)
			guard, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot, Admission: AdmissionEnforce})
			if err != nil {
				t.Fatal(err)
			}
			member := &parkedSessionCaptureMember{listed: ListResult{Repository: "acme/app", CanonicalDir: guard.CanonicalDir,
				WorktreeDir: worktree, WorktreesRoot: guard.WorktreesRoot, Branch: branch}}
			if err := member.acquireParkedCaptureGit(context.Background(), fixture.projectsRoot); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closeParkedSessionMembers([]parkedSessionCaptureMember{*member}) })
			if tc.change != nil {
				tc.change(t, member, worktree)
			}
			ctx := context.Background()
			if tc.context != nil {
				ctx = tc.context(ctx)
			}
			err = member.captureParkedObservation(ctx, func() (string, string, error) {
				return ParkedSessionWorkLogSnapshot(fixture.projectsRoot, worktree, source)
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) || member.snapshot.Head != "" {
				t.Fatalf("%s capture = (%#v, %v), want %q refusal", tc.name, member.snapshot, err, tc.want)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("credential leaked: %v", err)
			}
		})
	}
}

//nolint:paralleltest // each fixture configures process-wide Git and agent environment
func TestE2EParkedCaptureReportsInaccessibleOpenAndObservationFailures(t *testing.T) {
	for _, stage := range []string{"canonical opener", "worktree opener", "branch query", "head query", "status query", "Work Log snapshot", "push remote query"} {
		//nolint:paralleltest // each fixture calls t.Setenv and runs native Git
		t.Run(stage, func(t *testing.T) {
			fixture, member, source := newParkedCaptureTestMember(t, "park-inaccessible-"+strings.ReplaceAll(stage, " ", "-"))
			fault := errors.New("injected operation-local failure")
			switch stage {
			case "canonical opener":
				member.openCanonical = func(string) (*canonicalRepository, error) { return nil, fault }
			case "worktree opener":
				member.openWorktree = func(string) (*cleanupWorktreeHandle, error) { return nil, fault }
			}
			if stage == "canonical opener" || stage == "worktree opener" {
				err := member.acquireParkedCaptureGit(context.Background(), fixture.projectsRoot)
				if !errors.Is(err, fault) {
					t.Fatalf("%s error = %v", stage, err)
				}
				return
			}
			if err := member.acquireParkedCaptureGit(context.Background(), fixture.projectsRoot); err != nil {
				t.Fatal(err)
			}
			member.query = func(_ context.Context, args []string, next func() (string, error)) (string, error) {
				switch {
				case stage == "branch query" && args[0] == "symbolic-ref":
					return "", fault
				case stage == "head query" && args[0] == "rev-parse":
					return "invalid-object", nil
				case stage == "status query" && args[0] == "status":
					return "", fault
				default:
					return next()
				}
			}
			ctx := context.Background()
			if stage == "push remote query" {
				ctx = withCanonicalGitInterceptor(ctx, func(_ context.Context, args []string, next func() ([]byte, error)) ([]byte, error) {
					if len(args) >= 4 && args[0] == "remote" && args[1] == "get-url" && args[3] == "--push" {
						return nil, fault
					}
					return next()
				})
			}
			err := member.captureParkedObservation(ctx, func() (string, string, error) {
				if stage == "Work Log snapshot" {
					return "", "", fault
				}
				return ParkedSessionWorkLogSnapshot(fixture.projectsRoot, member.guard.Path, source)
			})
			if err == nil || member.snapshot.Head != "" {
				t.Fatalf("%s capture = (%#v, %v)", stage, member.snapshot, err)
			}
			if stage == "Work Log snapshot" || stage == "push remote query" || stage == "status query" || stage == "branch query" {
				if !errors.Is(err, fault) && stage != "branch query" {
					t.Fatalf("%s lost fault: %v", stage, err)
				}
			}
		})
	}
}

//nolint:paralleltest // each fixture configures process-wide Git and agent environment
func TestE2EParkedAggregateRefusesMissingJournalAndOccupiedLock(t *testing.T) {
	for _, stage := range []string{"missing journal", "occupied lock"} {
		//nolint:paralleltest // each fixture calls t.Setenv and runs native Git
		t.Run(stage, func(t *testing.T) {
			fixture, member, _ := newParkedCaptureTestMember(t, "park-journal-"+strings.ReplaceAll(stage, " ", "-"))
			journal := filepath.Join(member.listed.WorktreeDir, journalRootDirectory, journalLocalDirectory, worklogDirectory)
			if stage == "missing journal" {
				if err := os.Rename(journal, journal+".moved"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(filepath.Join(journal, localWorkLogLockName)); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(journal, localWorkLogLockName), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			err := member.acquire(context.Background(), fixture.projectsRoot)
			if err == nil {
				t.Fatalf("%s acquired custody", stage)
			}
			if stage == "missing journal" && !strings.Contains(err.Error(), "open parked source Work Log journal") {
				t.Fatalf("missing journal error = %v", err)
			}
			if member.canonical == nil || member.worktree == nil {
				t.Fatalf("partial %s acquisition lost descriptor handles", stage)
			}
		})
	}
}

//nolint:paralleltest // each fixture configures process-wide Git and agent environment
func TestE2EParkedCaptureRevalidatesRemoteWithoutSecondTipQuery(t *testing.T) {
	for _, aggregate := range []bool{false, true} {
		name := "single"
		if aggregate {
			name = "aggregate"
		}
		//nolint:paralleltest // each fixture calls t.Setenv and runs native Git
		t.Run(name, func(t *testing.T) {
			fixture, member, source := newParkedCaptureTestMember(t, "park-drift-"+name)
			remoteQueries := 0
			ctx := withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, next func() ([]byte, error)) ([]byte, error) {
				if len(args) > 0 && args[0] == "ls-remote" {
					remoteQueries++
					return next()
				}
				if remoteQueries > 0 && len(args) > 1 && args[0] == "remote" && args[1] == "get-url" {
					return []byte("https://github.com/acme/changed.git\n"), nil
				}
				return next()
			})
			var err error
			if aggregate {
				called := false
				err = CaptureParkedSessionAggregate(ctx, fixture.projectsRoot, []ListResult{member.listed}, source,
					func([]sessionpark.Worktree) error { called = true; return nil })
				if called {
					t.Fatal("aggregate persisted drifted remote")
				}
			} else {
				_, err = CaptureParkedSessionWorktree(ctx, fixture.projectsRoot, member.listed, source)
			}
			if err == nil || !strings.Contains(err.Error(), "changed during") || remoteQueries != 1 {
				t.Fatalf("%s drift = (%v, remote tip queries=%d)", name, err, remoteQueries)
			}
		})
	}
}

//nolint:paralleltest // each fixture configures process-wide Git and agent environment
func TestE2EParkedCaptureRejectsDescriptorAndObservationDrift(t *testing.T) {
	for _, stage := range []string{"canonical descriptor", "worktree descriptor", "branch", "head", "status", "fetch remote", "owner event", "Work Log read"} {
		//nolint:paralleltest // each fixture calls t.Setenv and runs native Git
		t.Run(stage, func(t *testing.T) {
			fixture, member, source := newParkedCaptureTestMember(t, "park-revalidate-"+strings.ReplaceAll(stage, " ", "-"))
			if err := member.acquireParkedCaptureGit(context.Background(), fixture.projectsRoot); err != nil {
				t.Fatal(err)
			}
			readWorkLog := func() (string, string, error) {
				return ParkedSessionWorkLogSnapshot(fixture.projectsRoot, member.guard.Path, source)
			}
			if err := member.captureParkedObservation(context.Background(), readWorkLog); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			switch stage {
			case "canonical descriptor":
				member.canonical.close()
			case "worktree descriptor":
				if err := os.Rename(member.guard.Path, member.guard.Path+".moved"); err != nil {
					t.Fatal(err)
				}
			case "branch", "head", "status":
				member.query = func(_ context.Context, args []string, next func() (string, error)) (string, error) {
					if (stage == "branch" && args[0] == "symbolic-ref") || (stage == "head" && args[0] == "rev-parse") ||
						(stage == "status" && args[0] == "status") {
						return "changed after capture", nil
					}
					return next()
				}
			case "fetch remote":
				ctx = withCanonicalGitInterceptor(ctx, func(_ context.Context, args []string, next func() ([]byte, error)) ([]byte, error) {
					if len(args) > 1 && args[0] == "remote" && args[1] == "get-url" {
						return []byte("changed-after-capture\n"), nil
					}
					return next()
				})
			case "owner event":
				original := readWorkLog
				readWorkLog = func() (string, string, error) {
					reference, _, err := original()
					return reference, "different-owner", err
				}
			case "Work Log read":
				readWorkLog = func() (string, string, error) { return "", "", errors.New("journal read failed") }
			}
			err := member.revalidateParkedObservation(ctx, readWorkLog, "parked authority drift")
			if err == nil {
				t.Fatalf("%s drift accepted", stage)
			}
			if stage == "canonical descriptor" && !strings.Contains(err.Error(), "canonical repository changed") {
				t.Fatalf("canonical error: %v", err)
			}
			if stage == "worktree descriptor" && !strings.Contains(err.Error(), "source worktree changed") {
				t.Fatalf("worktree error: %v", err)
			}
			if stage != "canonical descriptor" && stage != "worktree descriptor" && err.Error() != "parked authority drift" {
				t.Fatalf("%s drift error = %v", stage, err)
			}
		})
	}
}

//nolint:paralleltest // fixture configures process-wide Git and agent environment
func TestE2EParkedSingleCaptureRefusesInvalidSourceInventoryAndClaim(t *testing.T) {
	fixture, member, source := newParkedCaptureTestMember(t, "park-single-refusals")
	invalidSource := source
	invalidSource.PID = 0
	if _, err := CaptureParkedSessionWorktree(context.Background(), fixture.projectsRoot, member.listed, invalidSource); err == nil {
		t.Fatal("invalid source session accepted")
	}
	invalidPath := member.listed
	invalidPath.WorktreeDir = filepath.Join(fixture.projectsRoot, "missing-worktree")
	if _, err := CaptureParkedSessionWorktree(context.Background(), fixture.projectsRoot, invalidPath, source); err == nil {
		t.Fatal("unmanaged worktree accepted")
	}
	canonical := member.listed
	canonical.WorktreeDir = member.listed.CanonicalDir
	if _, err := CaptureParkedSessionWorktree(context.Background(), fixture.projectsRoot, canonical, source); err == nil || !strings.Contains(err.Error(), "named linked worktree") {
		t.Fatalf("canonical checkout refusal = %v", err)
	}
	invalidRepository := member.listed
	invalidRepository.Repository = "other/repo"
	if _, err := CaptureParkedSessionWorktree(context.Background(), fixture.projectsRoot, invalidRepository, source); err == nil || !strings.Contains(err.Error(), "repository identity changed") {
		t.Fatalf("listed repository refusal = %v", err)
	}
	invalidBranch := member.listed
	invalidBranch.Branch = "other-branch"
	if _, err := CaptureParkedSessionWorktree(context.Background(), fixture.projectsRoot, invalidBranch, source); err == nil || !strings.Contains(err.Error(), "inventory changed") {
		t.Fatalf("listed branch refusal = %v", err)
	}
}
