package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

//nolint:paralleltest // replaces the package-level atomic journal rewrite seam.
func TestChangedLineRatchetRepairsFailClosedWhenJournalCannotBeRewritten(t *testing.T) {
	previous := rewriteLocalEventJournalAtomicWrite
	rewriteLocalEventJournalAtomicWrite = func(*os.File, string, []byte, os.FileMode) error {
		return errors.New("injected journal rewrite failure")
	}
	t.Cleanup(func() { rewriteLocalEventJournalAtomicWrite = previous })
	for _, tc := range []struct {
		name string
		call func(string, *os.File) error
	}{
		{
			name: "append", call: func(worktree string, directory *os.File) error {
				_, _, err := appendLocalEventUnderLock(worktree, directory, LocalWorkLogEvent{ID: "new", Type: LocalEventHandoff, At: time.Now().UTC(), Message: "new"})
				return err
			},
		},
		{
			name: "projection", call: func(worktree string, _ *os.File) error {
				_, err := repairCurrentLocalProjection(worktree)
				return err
			},
		},
	} {
		//nolint:paralleltest // Each case replaces the package-level atomic journal rewrite seam.
		t.Run(tc.name, func(t *testing.T) {
			worktree := custodyWorktree(t)
			first := LocalWorkLogEvent{ID: "stable", Type: LocalEventHandoff, At: time.Unix(100, 0).UTC(), Message: "stable"}
			if _, _, err := appendLocalEvent(worktree, first); err != nil {
				t.Fatal(err)
			}
			dirPath := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory)
			appendTestBytes(t, filepath.Join(dirPath, localWorkLogEventsName), []byte(`{"version":1,"seq":1,"id":"torn`))
			directory, err := os.Open(dirPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = directory.Close() })
			if err := tc.call(worktree, directory); err == nil || !strings.Contains(err.Error(), "repair torn local work-log journal") {
				t.Fatalf("repair error = %v", err)
			}
		})
	}
}

//nolint:paralleltest // custodyWorktree clears inherited WB identity through t.Setenv.
//nolint:paralleltest // newExternalTargetFixture configures process-wide WB and Git fixture environment.
func TestChangedLineRatchetAppendsAuthenticatedExternalFailureRecord(t *testing.T) {
	fixture := newExternalTargetFixture(t)
	fixture.session.PID = 999_999
	fixture.options.Session.PID = fixture.session.PID
	prepared, err := PrepareExternalSessionWorkLog(context.Background(), fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	failure := sessionlaunch.FailureEvidence{
		HandoffID: fixture.base.request.HandoffID, RequestDigest: fixture.digest,
		AttemptID: fixture.options.AttemptID, AttemptIndex: fixture.options.AttemptIndex, PID: fixture.session.PID,
		StartedAt: fixture.session.StartedAt, FailedAt: fixture.session.StartedAt.Add(time.Second),
		TargetWorkLogReference: prepared.WorkLogReference, Diagnostic: "launcher exited",
	}
	options := ExternalTargetAttemptFailureOptions{
		ProjectsRoot: fixture.base.projectsRoot, Request: fixture.base.request, RequestDigest: fixture.digest,
		WorktreeDir: fixture.worktree, Failure: failure,
	}
	authenticates := func(got sessionlaunch.FailureEvidence, handoffID string, digest sessionmove.Digest, targetReference string) bool {
		return got == failure && handoffID == fixture.base.request.HandoffID && digest == fixture.digest && targetReference == prepared.WorkLogReference
	}
	missingProjection := options
	missingProjection.WorktreeDir = t.TempDir()
	if _, err := recordExternalTargetAttemptFailed(missingProjection, authenticates); err == nil {
		t.Fatal("failure record without the target projection was accepted")
	}
	wrongOwner := options
	wrongOwner.Failure.AttemptID = "000002-" + strings.Repeat("2", 32)
	wrongOwner.Failure.AttemptIndex = 2
	failure = wrongOwner.Failure
	if _, err := recordExternalTargetAttemptFailed(wrongOwner, authenticates); err == nil {
		t.Fatal("failure record without the exact attempt owner was accepted")
	}
	failure = options.Failure
	event, err := recordExternalTargetAttemptFailed(options, authenticates)
	if err != nil {
		t.Fatal(err)
	}
	if event.Result != "failed" || event.Extra["attempt_id"] != failure.AttemptID {
		t.Fatalf("event = %#v", event)
	}
}

func TestChangedLineRatchetRejectsIncompleteDependencyDeltaAfterAuthenticatingSource(t *testing.T) {
	t.Parallel()
	receipt := SupersessionReceipt{
		OriginalPR: "https://github.com/acme/app/pull/17", OriginalPRNumber: 17,
		OriginalPRRepository: "acme/app", OriginalPRHead: "head", OriginalHead: "head",
		DependencyDeltasComplete: true,
		DependencyDeltas: []SupersessionDependencyDelta{{
			SourcePR: "https://github.com/acme/app/pull/17", SourceHead: "head", Consumer: "acme/app",
		}},
	}
	entry := ListResult{Repository: "acme/app", HeadSHA: "head", OpenPullRequest: dependencyTestPullRequest("head")}
	if rejection := validateDependencyDeltas(context.Background(), receipt, entry); !strings.Contains(rejection, "missing ecosystem proof") {
		t.Fatalf("dependency rejection = %q", rejection)
	}
}

func TestChangedLineRatchetRejectsUnreadableAndMismatchedExternalPrompts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, prompts string)
	}{
		{
			name: "unreadable matching prompt",
			setup: func(t *testing.T, prompts string) {
				if err := os.Mkdir(filepath.Join(prompts, "0000-blocked.md"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "header sequence differs from filename",
			setup: func(t *testing.T, prompts string) {
				if err := os.WriteFile(filepath.Join(prompts, "0000-mismatch.md"), []byte("---\nseq: 1\nsource: agent_declared\n---\nbody\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			worktree := t.TempDir()
			prompts := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, promptsDirectory)
			if err := os.MkdirAll(prompts, 0o700); err != nil {
				t.Fatal(err)
			}
			tc.setup(t, prompts)
			if err := validateExternalHandoverPrompt(worktree, time.Time{}, "", "", "", nil); err == nil {
				t.Fatal("invalid external handover prompt was accepted")
			}
		})
	}
}

//nolint:paralleltest // newGitFixture configures process-wide WB and Git fixture environment.
func TestChangedLineRatchetResumeExtendsOneRunAndRejectsDifferentRuns(t *testing.T) {
	fixture := newGitFixture(t)
	addRepositoryToFixture(t, fixture, "lib")
	addRepositoryToFixture(t, fixture, "tool")
	options := CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "coordinated-resume", WorkLog: WorkLogOptions{Model: "unknown"}}
	if _, err := Create(context.Background(), []string{"acme/app"}, options); err != nil {
		t.Fatal(err)
	}
	options.Resume = true
	resumed, err := Create(context.Background(), []string{"acme/app", "acme/lib"}, options)
	if err != nil {
		t.Fatalf("resume extension = %v", err)
	}
	if len(resumed) != 2 {
		t.Fatalf("resume results = %#v", resumed)
	}
	if _, err := recordWorkLogWithHooks(fixture.home, options.Operation, resumed[1], WorkLogOptions{
		RunID: "other-run", Model: "unknown",
	}, workLogPublicationHooks{}); err != nil {
		t.Fatalf("publish distinct active run = %v", err)
	}
	_, err = Create(context.Background(), []string{"acme/app", "acme/lib", "acme/tool"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "coordinated-resume", Resume: true, WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err == nil || !strings.Contains(err.Error(), "different active Work Log runs") {
		t.Fatalf("mixed run resume error = %v", err)
	}
}
