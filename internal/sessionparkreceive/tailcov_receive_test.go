package sessionparkreceive

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/worktrees"
)

const (
	tailCovTargetEventsDirName = "events"
)

// tailCovResumeIDFixture builds the standard receiver fixture but admits an
// envelope carrying resumeID, so tests can drive identity mismatches between
// what atomic admission accepts and what the execution lock requires.
func tailCovResumeIDFixture(t *testing.T, resumeID string, members int) *receiveFixture {
	t.Helper()
	fixture := newReceiveFixture(t, members)
	fixture.request.ResumeID = resumeID
	raw, err := sessionpark.EncodeEnvelope(sessionpark.Envelope{
		SchemaVersion: sessionpark.EnvelopeSchemaVersion, Kind: sessionpark.EnvelopeKind, Request: fixture.request,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.raw = raw
	return fixture
}

// tailCovCorruptTargetEvents drops one artifact the receiver never wrote into
// the admitted event history, so the next history read or append must fail.
func tailCovCorruptTargetEvents(t *testing.T, store sessionpark.TargetStore, resumeID string) {
	t.Helper()
	path := filepath.Join(store.Root, resumeID, tailCovTargetEventsDirName, "tailcov-tampered.txt")
	if err := os.WriteFile(path, []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTailCovReceiveRejectsMalformedEnvelopeAndMissingMachine(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	t.Run("malformed envelope", func(t *testing.T) {
		options := fixture.options()
		options.RawEnvelope = []byte("{not-an-envelope")
		if _, err := Receive(context.Background(), options); err == nil || !strings.Contains(err.Error(), "parse park resume envelope") {
			t.Fatalf("error = %v, want an envelope parse failure", err)
		}
	})
	t.Run("missing local machine", func(t *testing.T) {
		options := fixture.options()
		options.LocalMachine = ""
		if _, err := Receive(context.Background(), options); err == nil || !strings.Contains(err.Error(), "remote.machine identity is required") {
			t.Fatalf("error = %v, want a missing machine identity failure", err)
		}
	})
}

func TestTailCovReceiveRejectsStoreRootThatCannotBeAdmitted(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	options.Store = sessionpark.NewTargetStore("relative-" + filepath.Base(fixture.t.TempDir()))
	if _, err := Receive(context.Background(), options); err == nil || !strings.Contains(err.Error(), "clean and absolute") {
		t.Fatalf("error = %v, want an unusable store root failure", err)
	}
}

func TestTailCovReceiveRejectsAggregateIdentityTheStoreCannotLock(t *testing.T) {
	fixture := tailCovResumeIDFixture(t, "unprefixed-aggregate", 1)
	options := fixture.options()
	if _, err := Receive(context.Background(), options); err == nil || !strings.Contains(err.Error(), "target authority identity is invalid") {
		t.Fatalf("error = %v, want an execution-lock identity failure", err)
	}
	// Admission ran first: the aggregate it created must be on disk, proving
	// the refusal happened at lock acquisition rather than at decode.
	admitted := filepath.Join(options.Store.Root, fixture.request.ResumeID, sessionpark.EnvelopeFileName)
	if _, err := os.Stat(admitted); err != nil {
		t.Fatalf("admission did not persist the aggregate envelope: %v", err)
	}
}

func TestTailCovReceiveRecordsEventTimesFromInjectedClock(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	fixed := time.Date(2027, time.March, 4, 5, 6, 7, 0, time.UTC)
	options.Now = func() time.Time { return fixed }

	result, err := Receive(context.Background(), options)
	if err != nil || result.Phase != PhaseCompleted || result.Receipt == nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	entries, err := os.ReadDir(filepath.Join(options.Store.Root, fixture.request.ResumeID, tailCovTargetEventsDirName))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("the receiver recorded no phases at all")
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(options.Store.Root, fixture.request.ResumeID, tailCovTargetEventsDirName, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var event struct {
			Phase string    `json:"phase"`
			At    time.Time `json:"at"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
		if !event.At.Equal(fixed) {
			t.Fatalf("event %s at = %s, want the injected clock %s", event.Phase, event.At, fixed)
		}
	}
}

func TestTailCovReceiveRejectsTamperedTargetEventHistory(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	if _, err := Receive(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	tailCovCorruptTargetEvents(t, options.Store, fixture.request.ResumeID)

	if _, err := Receive(context.Background(), options); err == nil || !strings.Contains(err.Error(), "unexpected park resume event artifact") {
		t.Fatalf("error = %v, want a tampered event history failure", err)
	}
}

func TestTailCovReceiveRejectsMemberResultIdentityDrift(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	options.ReceiveMember = func(context.Context, worktrees.SessionMemberReceiveOptions) (worktrees.SessionReceiveResult, error) {
		return worktrees.SessionReceiveResult{
			Repository: "acme/somewhere-else", WorktreeDir: fixture.paths[0], Commit: fixture.request.Members[0].Commit,
		}, nil
	}
	if _, err := Receive(context.Background(), options); err == nil || !strings.Contains(err.Error(), "conflicts with admitted repository or commit") {
		t.Fatalf("error = %v, want a prepared-member identity conflict", err)
	}
}

func TestTailCovReceiveRequiresAbsoluteMemberTargetPath(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	options.ReceiveMember = func(context.Context, worktrees.SessionMemberReceiveOptions) (worktrees.SessionReceiveResult, error) {
		return worktrees.SessionReceiveResult{
			Repository: fixture.request.Members[0].Repository, WorktreeDir: filepath.Join("relative", "target"),
			Commit: fixture.request.Members[0].Commit,
		}, nil
	}
	if _, err := Receive(context.Background(), options); err == nil || !strings.Contains(err.Error(), "successor context member") {
		t.Fatalf("error = %v, want a successor-context identity failure", err)
	}
}

func TestTailCovReceiveFailsWhenReceivedEventCannotBeRecorded(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	fixed := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	calls := 0
	// The clock's value is evaluated as the append's argument, so corrupting
	// the history here models a store that fails exactly when the very first
	// phase has to be recorded durably.
	options.Now = func() time.Time {
		calls++
		if calls == 1 {
			tailCovCorruptTargetEvents(t, options.Store, fixture.request.ResumeID)
		}
		return fixed
	}
	if _, err := Receive(context.Background(), options); err == nil || !strings.Contains(err.Error(), "unexpected park resume event artifact") {
		t.Fatalf("error = %v, want a received-phase event write failure", err)
	}
	if calls != 1 {
		t.Fatalf("clock calls = %d, want the failure on the first phase append", calls)
	}
}

func TestTailCovReceiveFailsWhenReplayCompletionEventCannotBeRecorded(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	interrupted := false
	options.AfterReceipt = func() error {
		if !interrupted {
			interrupted = true
			return errors.New("interrupt before the completion event")
		}
		return nil
	}
	if _, err := Receive(context.Background(), options); err == nil {
		t.Fatal("first receive did not report the injected interruption")
	}
	// A durable receipt now exists with no completion phase recorded: the
	// replay must still refuse to report success when that phase cannot be
	// appended.
	options.AfterReceipt = nil
	fixed := time.Date(2031, time.June, 7, 8, 9, 10, 0, time.UTC)
	options.Now = func() time.Time {
		tailCovCorruptTargetEvents(t, options.Store, fixture.request.ResumeID)
		return fixed
	}
	if _, err := Receive(context.Background(), options); err == nil || !strings.Contains(err.Error(), "unexpected park resume event artifact") {
		t.Fatalf("error = %v, want a replay completion event write failure", err)
	}
}

func TestTailCovReceiveFailsWhenMembersReadyEventCannotBeRecorded(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	baseReceive := options.ReceiveMember
	options.ReceiveMember = func(ctx context.Context, member worktrees.SessionMemberReceiveOptions) (worktrees.SessionReceiveResult, error) {
		result, err := baseReceive(ctx, member)
		if err != nil {
			return result, err
		}
		tailCovCorruptTargetEvents(t, options.Store, fixture.request.ResumeID)
		return result, nil
	}
	if _, err := Receive(context.Background(), options); err == nil || !strings.Contains(err.Error(), "unexpected park resume event artifact") {
		t.Fatalf("error = %v, want a members-ready event write failure", err)
	}
}

func TestTailCovReceiveFailsWhenClaimsReadyEventCannotBeRecorded(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	basePrepare := options.PrepareMember
	options.PrepareMember = func(ctx context.Context, member worktrees.ParkedSessionWorkLogPrepareOptions) (worktrees.ParkedSessionWorkLogPrepareResult, error) {
		result, err := basePrepare(ctx, member)
		if err != nil {
			return result, err
		}
		tailCovCorruptTargetEvents(t, options.Store, fixture.request.ResumeID)
		return result, nil
	}
	if _, err := Receive(context.Background(), options); err == nil || !strings.Contains(err.Error(), "unexpected park resume event artifact") {
		t.Fatalf("error = %v, want a claims-ready event write failure", err)
	}
}

func TestTailCovReceiveRejectsPreparedMemberReferenceConflict(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	options.PrepareMember = func(context.Context, worktrees.ParkedSessionWorkLogPrepareOptions) (worktrees.ParkedSessionWorkLogPrepareResult, error) {
		return worktrees.ParkedSessionWorkLogPrepareResult{WorkLogReference: "worklog:elsewhere/run/" + strings.Repeat("f", 64)}, nil
	}
	if _, err := Receive(context.Background(), options); err == nil || !strings.Contains(err.Error(), "conflicting target Work Log reference") {
		t.Fatalf("error = %v, want a conflicting target Work Log reference", err)
	}
}

func TestTailCovReceiveSurfacesAfterClaimsReadyFailure(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	injected := errors.New("injected after-claims failure")
	options.AfterClaimsReady = func() error { return injected }
	if _, err := Receive(context.Background(), options); !errors.Is(err, injected) {
		t.Fatalf("error = %v, want the injected after-claims failure", err)
	}
}

func TestTailCovReceiveSurfacesUnexpectedInspectFailure(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	injected := errors.New("injected inspect failure")
	interrupted := false
	options.AfterMembersReady = func() error {
		if !interrupted {
			interrupted = true
			return errors.New("interrupt after members ready")
		}
		return nil
	}
	if _, err := Receive(context.Background(), options); err == nil {
		t.Fatal("first receive did not report the injected interruption")
	}
	options.InspectSuccessor = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
		return sessionlaunch.Result{}, injected
	}
	if _, err := Receive(context.Background(), options); !errors.Is(err, injected) {
		t.Fatalf("error = %v, want the injected inspect failure", err)
	}
}

func TestTailCovReceiveFallsBackToRealSuccessorLaunchers(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	options.InspectSuccessor = nil
	options.StartSuccessor = nil
	// An empty PATH proves the receiver reached the real launcher: it must
	// report the fixed tmux dependency as unavailable rather than pretend a
	// successor started.
	t.Setenv("PATH", t.TempDir())
	if _, err := Receive(context.Background(), options); err == nil || !strings.Contains(err.Error(), "tmux executable is unavailable") {
		t.Fatalf("error = %v, want the real launcher dependency failure", err)
	}
}

func TestTailCovReceiveRejectsConflictingSuccessorIdentity(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	baseStart := options.StartSuccessor
	options.StartSuccessor = func(ctx context.Context, launch sessionlaunch.Options) (sessionlaunch.Result, error) {
		result, err := baseStart(ctx, launch)
		if err != nil {
			return result, err
		}
		result.HandoffID = "resume-somebody-else"
		return result, nil
	}
	if _, err := Receive(context.Background(), options); err == nil || !strings.Contains(err.Error(), "conflicts with admitted bundle identity") {
		t.Fatalf("error = %v, want a successor identity conflict", err)
	}
}

func TestTailCovReceiveRejectsSuccessorReceiptThatFailsValidation(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	baseStart := options.StartSuccessor
	options.StartSuccessor = func(ctx context.Context, launch sessionlaunch.Options) (sessionlaunch.Result, error) {
		result, err := baseStart(ctx, launch)
		if err != nil {
			return result, err
		}
		result.AttemptID = "not-an-attempt-id"
		return result, nil
	}
	if _, err := Receive(context.Background(), options); err == nil || !strings.Contains(err.Error(), "successor identity is incomplete") {
		t.Fatalf("error = %v, want a durable receipt validation failure", err)
	}
}

func TestTailCovReceiveSurfacesAfterSuccessorStartedFailure(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	injected := errors.New("injected after-successor failure")
	options.AfterSuccessorStarted = func() error { return injected }
	if _, err := Receive(context.Background(), options); !errors.Is(err, injected) {
		t.Fatalf("error = %v, want the injected after-successor failure", err)
	}
}

func TestTailCovReceiveFailsWhenDurableReceiptCannotBeWritten(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	baseComplete := options.CompleteMember
	options.CompleteMember = func(member worktrees.ParkedTargetCompletionOptions) (worktrees.LocalWorkLogEvent, error) {
		// Occupy the receipt name with a directory: publication finds the name
		// taken but cannot reload it as the durable receipt it just wrote.
		if err := os.Mkdir(filepath.Join(options.Store.Root, fixture.request.ResumeID, "receipt.json"), 0o700); err != nil {
			t.Fatal(err)
		}
		return baseComplete(member)
	}
	if _, err := Receive(context.Background(), options); err == nil || !strings.Contains(err.Error(), "load durable park resume receipt after publication") {
		t.Fatalf("error = %v, want a durable receipt publication failure", err)
	}
}

func TestTailCovReceiveFailsWhenCompletionEventCannotBeRecorded(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	options.AfterReceipt = func() error {
		tailCovCorruptTargetEvents(t, options.Store, fixture.request.ResumeID)
		return nil
	}
	if _, err := Receive(context.Background(), options); err == nil || !strings.Contains(err.Error(), "unexpected park resume event artifact") {
		t.Fatalf("error = %v, want a completion event write failure", err)
	}
}

func TestTailCovReceiveFailsWhenSuccessorStartedEventCannotBeRecorded(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	baseStart := options.StartSuccessor
	options.StartSuccessor = func(ctx context.Context, launch sessionlaunch.Options) (sessionlaunch.Result, error) {
		result, err := baseStart(ctx, launch)
		if err != nil {
			return result, err
		}
		// The claims event has already been published inside BeforeRelease;
		// breaking the history now must only fail the successor-started append.
		tailCovCorruptTargetEvents(t, options.Store, fixture.request.ResumeID)
		return result, nil
	}
	if _, err := Receive(context.Background(), options); err == nil || !strings.Contains(err.Error(), "unexpected park resume event artifact") {
		t.Fatalf("error = %v, want a successor-started event write failure", err)
	}
}

func TestTailCovReceiveMembersReadyReplayDefaultsToRealVerifier(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	interrupted := false
	options.AfterMembersReady = func() error {
		if !interrupted {
			interrupted = true
			return errors.New("interrupt after members ready")
		}
		return nil
	}
	if _, err := Receive(context.Background(), options); err == nil {
		t.Fatal("first receive did not report the injected interruption")
	}
	// No injected verifier on the replay: the receiver must default to the real
	// worktree verifier instead of re-preparing the member through the receive
	// path.
	options.VerifyMember = nil
	sentinel := errors.New("receive path must not run on a members-ready replay")
	options.ReceiveMember = func(context.Context, worktrees.SessionMemberReceiveOptions) (worktrees.SessionReceiveResult, error) {
		return worktrees.SessionReceiveResult{}, sentinel
	}
	_, err := Receive(context.Background(), options)
	if err == nil {
		t.Fatal("real verifier unexpectedly accepted a worktree that was never prepared")
	}
	if errors.Is(err, sentinel) {
		t.Fatalf("members-ready replay re-received the member instead of verifying it: %v", err)
	}
}

func TestTailCovTargetStoreRootJoinsParkDirectory(t *testing.T) {
	home := t.TempDir()
	if got, want := TargetStoreRoot(home), filepath.Join(home, sessionpark.TargetDirName); got != want {
		t.Fatalf("TargetStoreRoot(%q) = %q, want %q", home, got, want)
	}
}
