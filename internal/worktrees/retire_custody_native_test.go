package worktrees

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktreecollab"
)

func custodyLiveProcess(t *testing.T) int {
	t.Helper()
	command := exec.Command("sleep", "300")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	return command.Process.Pid
}

func custodyRegisterSession(t *testing.T, directory, id string, pid int) session.Record {
	t.Helper()
	record, err := session.Register(directory, session.Record{PID: pid, Runtime: "codex", Model: "test-model", WBSessionID: id, NativeHarnessID: id})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func custodyIdentity(record session.Record) AgentIdentity {
	return AgentIdentity{Runtime: record.Runtime, AgentID: record.NativeHarnessID, Model: record.Model, PID: record.PID, WBSessionID: record.WBSessionID, Registered: true}
}

func custodySessionLive(directory, id string) (bool, error) {
	record, found := session.LookupByWBSessionID(directory, id)
	if !found {
		return false, nil
	}
	exact, live, err := session.LookupExact(directory, record.PID)
	if err != nil {
		return false, err
	}
	return live && exact.WBSessionID == id && exact.Lifecycle == "", nil
}

func custodySessionStatus(directory, id string) (string, error) {
	views, err := session.List(directory)
	if err != nil {
		return "unknown", nil
	}
	status := "unknown"
	matches := 0
	for _, view := range views {
		if view.WBSessionID != id {
			continue
		}
		matches++
		if matches != 1 {
			return "unknown", nil
		}
		exact, live, err := session.LookupExact(directory, view.PID)
		if err != nil || exact.WBSessionID != id || exact.Lifecycle != "" {
			return "unknown", nil
		}
		if live {
			status = "live"
		} else {
			status = "inactive"
		}
	}
	return status, nil
}

func custodyTakeover(t *testing.T, fixture *gitFixture, worktree, caller, expected string) worktreecollab.State {
	t.Helper()
	directory := filepath.Join(fixture.home, session.DirName)
	service := worktreecollab.Service{
		Store: worktreecollab.NewStore(fixture.home),
		Ports: worktreecollab.ServicePorts{
			Resolve: func(ctx context.Context, idOrPath string) (worktreecollab.Checkout, error) {
				return ResolveCollaborationCheckout(ctx, fixture.projectsRoot, idOrPath)
			},
			Caller:      func() (string, error) { return caller, nil },
			Live:        func(id string) (bool, error) { return custodySessionLive(directory, id) },
			OwnerStatus: func(id string) (string, error) { return custodySessionStatus(directory, id) },
			ObserveLegacy: func(checkout worktreecollab.Checkout) (worktreecollab.ObservedOwner, error) {
				return ObserveCollaborationLegacyOwner(checkout.Root, directory)
			},
			ObserveLegacyForInspection: func(checkout worktreecollab.Checkout) (worktreecollab.ObservedOwner, error) {
				return ObserveCollaborationLegacyOwnerReadOnly(checkout.Root, directory)
			},
			Now:          time.Now,
			NewMessageID: func() (string, error) { return "custody-test-message", nil },
		},
	}
	state, err := service.Take(context.Background(), worktree, expected, true, "preserve legacy custody after audited takeover")
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func custodyLegacyExpected(t *testing.T, fixture *gitFixture, worktree string) string {
	t.Helper()
	observed, err := ObserveCollaborationLegacyOwnerReadOnly(worktree, filepath.Join(fixture.home, session.DirName))
	if err != nil {
		t.Fatal(err)
	}
	if observed.ID == "" {
		t.Fatal("fixture has no legacy owner observation")
	}
	return observed.ID
}

// TestRetireAcceptsAuditedTakeoverOfLiveLegacyCustody is the former red path:
// a historical live PID still vetoes retirement even after an audited current
// owner exists. Retirement now honors the corroborated current owner and
// preserves every historical custody record.
//
//nolint:paralleltest // newGitFixture sets process-wide WB and Git configuration variables.
func TestRetireAcceptsAuditedTakeoverOfLiveLegacyCustody(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "retire-custody", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	legacyPID := custodyLiveProcess(t)
	if _, err := recordOwner(worktree, "legacy-effort", "codex/legacy", "legacy-model", legacyPID); err != nil {
		t.Fatal(err)
	}
	owners, err := ownerViews(worktree)
	if err != nil || len(owners) == 0 {
		t.Fatalf("owner views = (%+v, %v)", owners, err)
	}
	entry := ListResult{Task: "retire-custody", Repository: "acme/app", Branch: "retire-custody", WorktreeDir: worktree, Owners: owners}
	if _, err := retireCheckCustody(context.Background(), fixture.projectsRoot, fixture.home, entry); err == nil || !strings.Contains(err.Error(), "competing active claim") {
		t.Fatalf("retirement without current authority = %v", err)
	}

	directory := filepath.Join(fixture.home, session.DirName)
	successor := custodyRegisterSession(t, directory, "wbs-successor", os.Getpid())
	SetSessionResolver(func() (AgentIdentity, bool) { return custodyIdentity(successor), true })
	t.Cleanup(func() { SetSessionResolver(nil) })

	state := custodyTakeover(t, fixture, worktree, successor.WBSessionID, custodyLegacyExpected(t, fixture, worktree))
	if state.Owner != successor.WBSessionID || state.OwnerEpoch != 1 || state.Revision != 1 ||
		len(state.OwnerChanges) != 1 || !state.OwnerChanges[0].Forced || state.OwnerChanges[0].Reason == "" {
		t.Fatalf("audited takeover = %+v", state)
	}
	fence, err := retireCheckCustody(context.Background(), fixture.projectsRoot, fixture.home, entry)
	if err != nil {
		t.Fatal(err)
	}
	if fence.mode != "collaboration" || fence.sessionID != successor.WBSessionID || fence.epoch != 1 || fence.revision != 1 {
		t.Fatalf("custody fence = %+v", fence)
	}

	gitTest(t, worktree, "push", "-u", "origin", "retire-custody")
	archive := filepath.Join(t.TempDir(), "archive.git")
	gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
	testenv.ConfigureGitAutoMaintenanceOff(t, archive)
	options := RetireOptions{ProjectsRoot: fixture.projectsRoot, Task: "retire-custody", ArchiveRemote: archive,
		RemoteOwnership: retireAllowRemoteOwner,
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
		OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
	}
	planned, err := Retire(context.Background(), options)
	if err != nil {
		t.Fatalf("dry run after audited takeover: %v", err)
	}
	if planned.Phase != "planned" {
		t.Fatalf("plan = %+v", planned)
	}
	options.Apply = true
	applied, err := Retire(context.Background(), options)
	if err != nil {
		t.Fatalf("apply after audited takeover: %v", err)
	}
	if applied.Phase != "complete" {
		t.Fatalf("retirement = %+v", applied)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("retired checkout remains: %v", err)
	}
}

// TestRetireCustodyRefusesChangeBeforeDeletion proves the last custody check
// runs immediately before deletion intent: even after the retired source and
// private archive were published, a changed or unreadable current authority
// refuses without removing the checkout or deleting the original branch.
//
//nolint:paralleltest // newGitFixture sets process-wide WB and Git configuration variables.
func TestRetireCustodyRefusesChangeBeforeDeletion(t *testing.T) {
	for _, mode := range []string{"changed", "corrupt"} {
		//nolint:paralleltest // newGitFixture sets process-wide WB and Git configuration variables.
		t.Run(mode, func(t *testing.T) {
			fixture := newGitFixture(t)
			created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
				ProjectsRoot: fixture.projectsRoot, Operation: "retire-custody-deletion", WorkLog: WorkLogOptions{Model: "unknown"},
			})
			if err != nil {
				t.Fatal(err)
			}
			worktree := created[0].WorktreeDir
			directory := filepath.Join(fixture.home, session.DirName)
			current := custodyRegisterSession(t, directory, "wbs-current", os.Getpid())
			SetSessionResolver(func() (AgentIdentity, bool) { return custodyIdentity(current), true })
			t.Cleanup(func() { SetSessionResolver(nil) })
			custodyTakeover(t, fixture, worktree, current.WBSessionID, custodyLegacyExpected(t, fixture, worktree))
			checkout, err := ResolveCollaborationCheckout(context.Background(), fixture.projectsRoot, worktree)
			if err != nil {
				t.Fatal(err)
			}
			gitTest(t, worktree, "push", "-u", "origin", "retire-custody-deletion")
			archive := filepath.Join(t.TempDir(), "archive.git")
			gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
			testenv.ConfigureGitAutoMaintenanceOff(t, archive)
			options := RetireOptions{
				ProjectsRoot: fixture.projectsRoot, Task: "retire-custody-deletion", ArchiveRemote: archive, Apply: true,
				RemoteOwnership: retireAllowRemoteOwner,
				Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
					return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
				},
				OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
				afterPhase: func(phase string) error {
					if phase != "archive_published" {
						return nil
					}
					if mode == "corrupt" {
						return os.WriteFile(filepath.Join(fixture.home, "worktree-collaboration", checkout.ID+".json"), []byte("{corrupt"), 0o600)
					}
					intruder := custodyRegisterSession(t, directory, "wbs-intruder", custodyLiveProcess(t))
					custodyTakeover(t, fixture, worktree, intruder.WBSessionID, current.WBSessionID)
					SetSessionResolver(func() (AgentIdentity, bool) { return custodyIdentity(intruder), true })
					return nil
				},
			}
			_, err = Retire(context.Background(), options)
			want := "custody changed before deletion"
			if mode == "corrupt" {
				want = "custody authority"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s refusal = %v, want containing %q", mode, err, want)
			}
			if _, statErr := os.Stat(worktree); statErr != nil {
				t.Fatalf("%s removed checkout: %v", mode, statErr)
			}
			if remote := gitTestOutput(t, fixture.canonical, "ls-remote", "origin", "refs/heads/retire-custody-deletion"); remote == "" {
				t.Fatalf("%s deleted the original remote branch", mode)
			}
		})
	}
}

// TestRetireCustodyRefusesAuthorityChangesUnderLock proves the fence is
// revalidated beneath the held task/checkout locks: an ownership change or any
// other coordination revision advance refuses before any source or remote
// mutation.
//
//nolint:paralleltest // newGitFixture sets process-wide WB and Git configuration variables.
func TestRetireCustodyRefusesAuthorityChangesUnderLock(t *testing.T) {
	for _, change := range []string{"owner", "revision"} {
		//nolint:paralleltest // newGitFixture sets process-wide WB and Git configuration variables.
		t.Run(change, func(t *testing.T) {
			fixture := newGitFixture(t)
			created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
				ProjectsRoot: fixture.projectsRoot, Operation: "retire-custody-race", WorkLog: WorkLogOptions{Model: "unknown"},
			})
			if err != nil {
				t.Fatal(err)
			}
			worktree := created[0].WorktreeDir
			directory := filepath.Join(fixture.home, session.DirName)
			current := custodyRegisterSession(t, directory, "wbs-current", os.Getpid())
			SetSessionResolver(func() (AgentIdentity, bool) { return custodyIdentity(current), true })
			t.Cleanup(func() { SetSessionResolver(nil) })
			custodyTakeover(t, fixture, worktree, current.WBSessionID, custodyLegacyExpected(t, fixture, worktree))
			gitTest(t, worktree, "push", "-u", "origin", "retire-custody-race")
			before := gitTestOutput(t, worktree, "rev-parse", "HEAD")

			archive := filepath.Join(t.TempDir(), "archive.git")
			gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
			testenv.ConfigureGitAutoMaintenanceOff(t, archive)
			ports := productionRetireEntryPorts()
			productionCheck := ports.checkCustody
			calls := 0
			ports.checkCustody = func(ctx context.Context, root, home string, entry ListResult) (retireCustodyFence, error) {
				fence, checkErr := productionCheck(ctx, root, home, entry)
				calls++
				if checkErr != nil || calls != 1 {
					return fence, checkErr
				}
				switch change {
				case "owner":
					intruderPID := custodyLiveProcess(t)
					intruder := custodyRegisterSession(t, directory, "wbs-intruder", intruderPID)
					custodyTakeover(t, fixture, worktree, intruder.WBSessionID, current.WBSessionID)
					SetSessionResolver(func() (AgentIdentity, bool) { return custodyIdentity(intruder), true })
				case "revision":
					service := worktreecollab.Service{
						Store: worktreecollab.NewStore(fixture.home),
						Ports: worktreecollab.ServicePorts{
							Resolve: func(ctx context.Context, idOrPath string) (worktreecollab.Checkout, error) {
								return ResolveCollaborationCheckout(ctx, fixture.projectsRoot, idOrPath)
							},
							Caller:      func() (string, error) { return "wbs-member", nil },
							Live:        func(string) (bool, error) { return true, nil },
							OwnerStatus: func(string) (string, error) { return "live", nil },
							ObserveLegacy: func(checkout worktreecollab.Checkout) (worktreecollab.ObservedOwner, error) {
								return ObserveCollaborationLegacyOwner(checkout.Root, directory)
							},
							ObserveLegacyForInspection: func(checkout worktreecollab.Checkout) (worktreecollab.ObservedOwner, error) {
								return ObserveCollaborationLegacyOwnerReadOnly(checkout.Root, directory)
							},
							Now:          time.Now,
							NewMessageID: func() (string, error) { return "custody-test-message", nil },
						},
					}
					if _, err := service.Join(ctx, worktree); err != nil {
						t.Fatal(err)
					}
				}
				return fence, nil
			}
			_, err = retireWithEntryPorts(context.Background(), RetireOptions{
				ProjectsRoot: fixture.projectsRoot, Task: "retire-custody-race", Apply: true, ArchiveRemote: archive,
				RemoteOwnership: retireAllowRemoteOwner,
				Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
					return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
				},
				OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
			}, ports)
			if err == nil || !strings.Contains(err.Error(), "custody changed") {
				t.Fatalf("custody race refusal = %v", err)
			}
			if after := gitTestOutput(t, worktree, "rev-parse", "HEAD"); after != before {
				t.Fatalf("custody race moved source HEAD from %s to %s", before, after)
			}
			if refs := gitTestOutput(t, fixture.canonical, "ls-remote", "origin", "refs/heads/retired/*"); refs != "" {
				t.Fatalf("custody race published retired ref: %s", refs)
			}
		})
	}
}

// TestRetireCustodyRefusesChangeBeforeLocalRemoval proves the fence also
// guards the last destructive phase: after the original remote branch and its
// atomic deletion proof exist, an ownership change refuses before the local
// checkout is removed, leaving the exact checkout for a later resume.
//
//nolint:paralleltest // newGitFixture sets process-wide WB and Git configuration variables.
func TestRetireCustodyRefusesChangeBeforeLocalRemoval(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "retire-custody-local", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	directory := filepath.Join(fixture.home, session.DirName)
	current := custodyRegisterSession(t, directory, "wbs-current", os.Getpid())
	SetSessionResolver(func() (AgentIdentity, bool) { return custodyIdentity(current), true })
	t.Cleanup(func() { SetSessionResolver(nil) })
	custodyTakeover(t, fixture, worktree, current.WBSessionID, custodyLegacyExpected(t, fixture, worktree))
	gitTest(t, worktree, "push", "-u", "origin", "retire-custody-local")
	archive := filepath.Join(t.TempDir(), "archive.git")
	gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
	testenv.ConfigureGitAutoMaintenanceOff(t, archive)
	_, err = Retire(context.Background(), RetireOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "retire-custody-local", ArchiveRemote: archive, Apply: true,
		RemoteOwnership: retireAllowRemoteOwner,
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
		OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
		afterPhase: func(phase string) error {
			if phase != "original_deleted" {
				return nil
			}
			intruder := custodyRegisterSession(t, directory, "wbs-intruder", custodyLiveProcess(t))
			custodyTakeover(t, fixture, worktree, intruder.WBSessionID, current.WBSessionID)
			SetSessionResolver(func() (AgentIdentity, bool) { return custodyIdentity(intruder), true })
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "custody changed before local removal") {
		t.Fatalf("custody change before local removal = %v", err)
	}
	if _, statErr := os.Stat(worktree); statErr != nil {
		t.Fatalf("removed checkout after custody change: %v", statErr)
	}
}

// TestRetireCustodyRefusesUncorroboratedCurrentAuthority covers a current
// owner that is not the invoking registered session, a different live
// session, and corrupt/ambiguous private authority.
//
//nolint:paralleltest // newGitFixture sets process-wide WB and Git configuration variables.
func TestRetireCustodyRefusesUncorroboratedCurrentAuthority(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "retire-custody-refuse", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	directory := filepath.Join(fixture.home, session.DirName)
	successor := custodyRegisterSession(t, directory, "wbs-successor", os.Getpid())
	SetSessionResolver(func() (AgentIdentity, bool) { return custodyIdentity(successor), true })
	t.Cleanup(func() { SetSessionResolver(nil) })
	custodyTakeover(t, fixture, worktree, successor.WBSessionID, custodyLegacyExpected(t, fixture, worktree))
	entry := ListResult{Task: "retire-custody-refuse", Repository: "acme/app", WorktreeDir: worktree}

	unregistered := successor
	unregistered.WBSessionID = ""
	SetSessionResolver(func() (AgentIdentity, bool) { return custodyIdentity(unregistered), true })
	if _, err := retireCheckCustody(context.Background(), fixture.projectsRoot, fixture.home, entry); err == nil || !strings.Contains(err.Error(), "registered invoking session") {
		t.Fatalf("unregistered caller = %v", err)
	}
	other := successor
	other.WBSessionID = "wbs-other"
	SetSessionResolver(func() (AgentIdentity, bool) { return custodyIdentity(other), true })
	if _, err := retireCheckCustody(context.Background(), fixture.projectsRoot, fixture.home, entry); err == nil || !strings.Contains(err.Error(), "current owner") {
		t.Fatalf("different owner = %v", err)
	}
	SetSessionResolver(func() (AgentIdentity, bool) { return custodyIdentity(successor), true })

	checkout, err := ResolveCollaborationCheckout(context.Background(), fixture.projectsRoot, worktree)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(fixture.home, "worktree-collaboration", checkout.ID+".json")
	if err := os.WriteFile(statePath, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := retireCheckCustody(context.Background(), fixture.projectsRoot, fixture.home, entry); err == nil || !strings.Contains(err.Error(), "custody authority") {
		t.Fatalf("corrupt authority = %v", err)
	}
}

// TestRetireCustodyRefusesAmbiguousRegisteredSession proves duplicate session
// IDs refuse rather than selecting one.
//
//nolint:paralleltest // newGitFixture sets process-wide WB and Git configuration variables.
func TestRetireCustodyRefusesAmbiguousRegisteredSession(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "retire-custody-ambiguous", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	directory := filepath.Join(fixture.home, session.DirName)
	current := custodyRegisterSession(t, directory, "wbs-ambiguous", os.Getpid())
	SetSessionResolver(func() (AgentIdentity, bool) { return custodyIdentity(current), true })
	t.Cleanup(func() { SetSessionResolver(nil) })
	custodyTakeover(t, fixture, worktree, current.WBSessionID, custodyLegacyExpected(t, fixture, worktree))
	custodyRegisterSession(t, directory, current.WBSessionID, custodyLiveProcess(t))
	entry := ListResult{Task: "retire-custody-ambiguous", Repository: "acme/app", WorktreeDir: worktree}
	if _, err := retireCheckCustody(context.Background(), fixture.projectsRoot, fixture.home, entry); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous session = %v", err)
	}
}
