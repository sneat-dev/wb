package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
)

func TestRecordParkedTargetCompletedRejectsCustodySupersededAtCompletionBarrier(t *testing.T) {
	base := newSessionReceiveFixture(t)
	member := sessionpark.RemoteMember{
		MemberID: "m-001-abcdef01", Repository: "acme/app", RepositoryRemote: base.remote,
		Branch: base.request.Branch, Commit: base.request.BundleCommit,
		SourceWorkLogReference: "worklog:session-park/source-run/" + strings.Repeat("b", 64),
	}
	request := sessionpark.RemoteRequest{
		SchemaVersion: sessionpark.RequestSchemaVersion, ResumeID: "resume-custody-race", ParkedSessionID: "park-custody-race",
		SuccessorWBSessionID: "wbs-park-successor", PredecessorWBSessionID: "wbs-park-source",
		SourceMachine: "laptop", TargetMachine: "target-vm", SourceRuntime: "codex", SourceModel: "gpt-5",
		Continuation: "continue privately", Members: []sessionpark.RemoteMember{member}, CreatedAt: time.Unix(100, 0).UTC(),
	}
	raw, err := sessionpark.EncodeEnvelope(sessionpark.Envelope{
		SchemaVersion: sessionpark.EnvelopeSchemaVersion, Kind: sessionpark.EnvelopeKind, Request: request,
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionmove.DigestBytes(raw)
	worktree := filepath.Join(base.home, "worktrees", "session-"+request.ResumeID, "acme", "app")
	if err := os.MkdirAll(filepath.Dir(worktree), 0o700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, base.canonical, "worktree", "add", "-b", sessionpark.MemberPin(request.ResumeID, member.MemberID), worktree, member.Commit)
	startedAt := time.Unix(200, 0).UTC()
	record := session.Record{
		PID: os.Getpid(), WBSessionID: request.SuccessorWBSessionID, PredecessorWBSessionID: request.PredecessorWBSessionID,
		Machine: request.TargetMachine, Runtime: request.SourceRuntime, Model: request.SourceModel,
		TmuxName: "wb-session-" + request.SuccessorWBSessionID, HandoffID: request.ResumeID, StartedAt: startedAt,
	}
	attemptID := "000001-" + strings.Repeat("1", 32)
	if _, err := PrepareParkedSessionWorkLog(context.Background(), ParkedSessionWorkLogPrepareOptions{
		ProjectsRoot: base.projectsRoot, Request: request, RequestDigest: digest, Member: member,
		ReceivedAt: request.CreatedAt, Session: record, AttemptID: attemptID, AttemptIndex: 1,
		WorktreeDir: worktree, PinnedCommit: member.Commit,
	}); err != nil {
		t.Fatal(err)
	}
	successor := sessionlaunch.Result{
		HandoffID: request.ResumeID, WBSessionID: request.SuccessorWBSessionID,
		PredecessorWBSessionID: request.PredecessorWBSessionID, TargetMachine: request.TargetMachine,
		PID: record.PID, AttemptID: attemptID, AttemptIndex: 1, TmuxName: record.TmuxName,
		Runtime: record.Runtime, Model: record.Model, WorktreeDir: worktree, PinnedCommit: member.Commit, StartedAt: startedAt,
	}
	// The test seam runs after the claim lock but before the journal barrier.
	// In the vulnerable implementation this same contender was injected after
	// the unlocked latest-owner read and completion still succeeded. The fixed
	// path admits the contender first, then revalidates the full barrier while
	// holding the journal lock and refuses stale completion authority.
	_, err = RecordParkedTargetCompleted(ParkedTargetCompletionOptions{
		ProjectsRoot: base.projectsRoot, Request: request, RequestDigest: digest, Member: member,
		WorktreeDir: worktree, Successor: successor,
		hooks: parkedTargetCompletionHooks{beforeCompletionBarrier: func() error {
			return RecordCustody(worktree, "", "competing successor", AgentIdentity{
				Runtime: "codex", AgentID: "competing-session", Model: "gpt-5", PID: os.Getpid(),
			})
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "winning live successor") {
		t.Fatalf("completion error = %v, want superseded-custody refusal", err)
	}
	events, readErr := readLocalEvents(worktree)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if eventByID(events, externalLocalEventID("park-target-completed-"+member.MemberID, digest, "")) != nil {
		t.Fatal("completion was recorded after a competing owner superseded custody")
	}
}
