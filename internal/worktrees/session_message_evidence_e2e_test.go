//go:build e2e

package worktrees

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func sourceMessageEvidenceFixture(t *testing.T) (ExternalSourceMessageOptions, string) {
	t.Helper()
	fixture := newExternalSourceFixture(t)
	lock := fixture.lock(t)
	handoffReceipt := fixture.authorizeSeal(t, lock)
	if _, err := SealExternalSessionWorkLog(ExternalSourceSealOptions{
		Store: fixture.store, ExecutionLock: lock, ProjectsRoot: fixture.base.projectsRoot,
		Request: fixture.base.request, RequestDigest: fixture.digest, Receipt: handoffReceipt, SourceSession: fixture.source,
	}); err != nil {
		t.Fatal(err)
	}
	message := messageForWorkLog(fixture.base.request, sessionmove.MessageKindText, "private message must stay out of public events")
	raw, err := sessionmove.EncodeMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionmove.DigestBytes(raw)
	return ExternalSourceMessageOptions{
		ProjectsRoot: fixture.base.projectsRoot, Request: fixture.base.request, RequestDigest: fixture.digest,
		Receipt: handoffReceipt, SourceSession: fixture.source, Message: message,
		Record: sessionmove.MessageRecord{SchemaVersion: sessionmove.MessageRecordSchemaVersion,
			Direction: sessionmove.MessageDirectionOutgoing, MessageID: message.MessageID, MessageDigest: digest,
			HandoffID: message.HandoffID, RecordedAt: message.SentAt},
		MessageReceipt: messageReceiptForWorkLog(message, digest, handoffReceipt),
	}, fixture.worktree
}

func targetMessageEvidenceFixture(t *testing.T) (ExternalTargetMessageOptions, string) {
	t.Helper()
	fixture := newExternalTargetFixture(t)
	prepared, err := PrepareExternalSessionWorkLog(context.Background(), fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	handoffReceipt := fixture.receipt(t, prepared)
	if _, err := RecordExternalTargetCompleted(ExternalTargetCompletionOptions{
		ProjectsRoot: fixture.base.projectsRoot, Request: fixture.base.request, RequestDigest: fixture.digest,
		Receipt: handoffReceipt, WorktreeDir: fixture.worktree,
	}); err != nil {
		t.Fatal(err)
	}
	message := messageForWorkLog(fixture.base.request, sessionmove.MessageKindRequestHandoff, "")
	raw, err := sessionmove.EncodeMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionmove.DigestBytes(raw)
	return ExternalTargetMessageOptions{
		ProjectsRoot: fixture.base.projectsRoot, Request: fixture.base.request, RequestDigest: fixture.digest,
		Receipt: handoffReceipt, Message: message,
		Record: sessionmove.MessageRecord{SchemaVersion: sessionmove.MessageRecordSchemaVersion,
			Direction: sessionmove.MessageDirectionIncoming, MessageID: message.MessageID, MessageDigest: digest,
			HandoffID: message.HandoffID, RecordedAt: message.SentAt.Add(time.Second)},
	}, fixture.worktree
}

//nolint:paralleltest // source fixture configures process-wide WB and Git environment
func TestE2EExternalSourceMessageEvidenceRefusesUnprovenRecords(t *testing.T) {
	options, worktree := sourceMessageEvidenceFixture(t)
	before, err := readLocalEvents(worktree)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, want string
		change     func(*ExternalSourceMessageOptions)
	}{
		{"handoff receipt", "receipt", func(o *ExternalSourceMessageOptions) { o.Receipt.HandoffID = "different-handoff" }},
		{"source session", "source", func(o *ExternalSourceMessageOptions) { o.SourceSession.WBSessionID = "other-source" }},
		{"message lineage", "lineage", func(o *ExternalSourceMessageOptions) { o.Message.HandoffID = "different-handoff" }},
		{"canonical encoding", "year outside of range", func(o *ExternalSourceMessageOptions) {
			o.Message.SentAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		}},
		{"outbox record", "exact durable outbox", func(o *ExternalSourceMessageOptions) {
			o.Record.MessageDigest = sessionmove.Digest(strings.Repeat("0", 64))
		}},
		{"paste receipt", "pane_id", func(o *ExternalSourceMessageOptions) { o.MessageReceipt.PaneID = "not-a-pane" }},
	} {
		//nolint:paralleltest // all cases inspect one native source fixture without mutating it
		t.Run(test.name, func(t *testing.T) {
			attempt := options
			test.change(&attempt)
			if _, err := RecordExternalSourceMessageSent(attempt); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("source refusal = %v, want %q", err, test.want)
			}
			after, err := readLocalEvents(worktree)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("source refusal changed local Work Log: err=%v", err)
			}
		})
	}
}

//nolint:paralleltest // target fixture configures process-wide WB and Git environment
func TestE2EExternalTargetMessageEvidenceRefusesUnprovenRecords(t *testing.T) {
	options, worktree := targetMessageEvidenceFixture(t)
	before, err := readLocalEvents(worktree)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, want string
		change     func(*ExternalTargetMessageOptions)
	}{
		{"handoff receipt", "receipt", func(o *ExternalTargetMessageOptions) { o.Receipt.HandoffID = "different-handoff" }},
		{"message lineage", "lineage", func(o *ExternalTargetMessageOptions) { o.Message.HandoffID = "different-handoff" }},
		{"canonical encoding", "year outside of range", func(o *ExternalTargetMessageOptions) {
			o.Message.SentAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		}},
		{"inbox record", "exact durable inbox", func(o *ExternalTargetMessageOptions) { o.Record.Direction = sessionmove.MessageDirectionOutgoing }},
	} {
		//nolint:paralleltest // all cases inspect one native target fixture without mutating it
		t.Run(test.name, func(t *testing.T) {
			attempt := options
			test.change(&attempt)
			if _, err := RecordExternalTargetMessageReceived(attempt); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("target refusal = %v, want %q", err, test.want)
			}
			after, err := readLocalEvents(worktree)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("target refusal changed local Work Log: err=%v", err)
			}
		})
	}
}

//nolint:paralleltest // source and target share a real Git fixture and WB home
func TestE2EExternalMessageWorkLogFollowsDurableSendReceiveAndPaste(t *testing.T) {
	source := newExternalSourceFixture(t)
	lock := source.lock(t)
	handoffReceipt := source.authorizeSeal(t, lock)
	targetWorktree := source.base.targetWorktree()
	if err := os.MkdirAll(filepath.Dir(targetWorktree), 0o700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, source.base.canonical, "worktree", "add", "-b", "wb-session/"+source.base.request.HandoffID,
		targetWorktree, source.base.request.BundleCommit)
	targetSession := session.Record{
		PID: handoffReceipt.PID, WBSessionID: handoffReceipt.SuccessorWBSessionID,
		PredecessorWBSessionID: handoffReceipt.PredecessorWBSessionID, Machine: handoffReceipt.TargetMachine,
		Runtime: handoffReceipt.Runtime, Model: handoffReceipt.Model, NativeHarnessID: handoffReceipt.NativeHarnessID,
		TmuxName: handoffReceipt.TmuxName, HandoffID: handoffReceipt.HandoffID, StartedAt: handoffReceipt.StartedAt,
	}
	prepared, err := PrepareExternalSessionWorkLog(context.Background(), ExternalSessionWorkLogPrepareOptions{
		ProjectsRoot: source.base.projectsRoot, Request: source.base.request, RequestDigest: source.digest,
		ReceivedAt: source.base.request.CreatedAt.Add(30 * time.Second), Session: targetSession,
		AttemptID: handoffReceipt.AttemptID, AttemptIndex: handoffReceipt.AttemptIndex,
		WorktreeDir: targetWorktree, PinnedCommit: handoffReceipt.PinnedCommit, HandoverBytes: source.base.handover,
	})
	if err != nil || prepared.WorkLogReference != handoffReceipt.TargetWorkLogReference {
		t.Fatalf("target preparation = %#v, %v", prepared, err)
	}
	if _, err := RecordExternalTargetCompleted(ExternalTargetCompletionOptions{
		ProjectsRoot: source.base.projectsRoot, Request: source.base.request, RequestDigest: source.digest,
		Receipt: handoffReceipt, WorktreeDir: targetWorktree,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := SealExternalSessionWorkLog(ExternalSourceSealOptions{
		Store: source.store, ExecutionLock: lock, ProjectsRoot: source.base.projectsRoot,
		Request: source.base.request, RequestDigest: source.digest, Receipt: handoffReceipt, SourceSession: source.source,
	}); err != nil {
		t.Fatal(err)
	}

	message := messageForWorkLog(source.base.request, sessionmove.MessageKindText, "private send/receive journey body")
	raw, err := sessionmove.EncodeMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	outgoing, err := source.store.AdmitOutgoingMessageUnderLock(lock, message.HandoffID, source.digest, raw, message.SentAt)
	if err != nil {
		t.Fatal(err)
	}
	incoming, err := source.store.AdmitIncomingMessageUnderLock(lock, message.HandoffID, source.digest, raw, message.SentAt.Add(time.Second))
	if err != nil || outgoing.Digest != incoming.Digest {
		t.Fatalf("durable send/receive = (%#v, %#v, %v)", outgoing, incoming, err)
	}
	intent := sessionmove.MessagePasteIntent{
		SchemaVersion: sessionmove.MessagePasteIntentSchemaVersion, MessageID: message.MessageID,
		MessageDigest: incoming.Digest, HandoffID: message.HandoffID, RecipientWBSessionID: message.RecipientWBSessionID,
		TmuxName: handoffReceipt.TmuxName, PaneID: "%7", PID: handoffReceipt.PID,
		IntendedAt: incoming.Record.RecordedAt.Add(time.Second),
	}
	if _, _, err := source.store.SaveIncomingPasteIntentUnderLock(lock, message.HandoffID, source.digest, intent); err != nil {
		t.Fatal(err)
	}
	messageReceipt := messageReceiptForWorkLog(message, incoming.Digest, handoffReceipt)
	messageReceipt.RecordedAt = incoming.Record.RecordedAt
	messageReceipt.PastedAt = intent.IntendedAt.Add(time.Second)
	if _, _, err := source.store.SaveIncomingMessageReceiptUnderLock(lock, message.HandoffID, source.digest, messageReceipt); err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.store.SaveOutgoingMessageReceiptUnderLock(lock, message.HandoffID, source.digest, messageReceipt); err != nil {
		t.Fatal(err)
	}
	targetOptions := ExternalTargetMessageOptions{
		ProjectsRoot: source.base.projectsRoot, Request: source.base.request, RequestDigest: source.digest,
		Receipt: handoffReceipt, Message: incoming.Message, Record: incoming.Record,
	}
	sourceOptions := ExternalSourceMessageOptions{
		ProjectsRoot: source.base.projectsRoot, Request: source.base.request, RequestDigest: source.digest,
		Receipt: handoffReceipt, SourceSession: source.source, Message: outgoing.Message,
		Record: outgoing.Record, MessageReceipt: messageReceipt,
	}
	targetEvent, err := RecordExternalTargetMessageReceived(targetOptions)
	if err != nil {
		t.Fatal(err)
	}
	sourceEvent, err := RecordExternalSourceMessageSent(sourceOptions)
	if err != nil {
		t.Fatal(err)
	}
	targetReplay, targetErr := RecordExternalTargetMessageReceived(targetOptions)
	sourceReplay, sourceErr := RecordExternalSourceMessageSent(sourceOptions)
	if targetErr != nil || sourceErr != nil || !sameLocalEvent(targetEvent, targetReplay) || !sameLocalEvent(sourceEvent, sourceReplay) {
		t.Fatalf("message Work Log retries changed exact events: target=%v source=%v", targetErr, sourceErr)
	}
	for _, event := range []LocalWorkLogEvent{targetEvent, sourceEvent} {
		encoded, err := json.Marshal(event)
		if err != nil || bytes.Contains(encoded, []byte(message.Body)) || event.Extra["message_digest"] != string(outgoing.Digest) ||
			event.Extra["acknowledgement_scope"] != "durable_record_and_tmux_paste_only" {
			t.Fatalf("public event leaked private body or lost durable lineage: %s, %v", encoded, err)
		}
	}
	if targetEvent.Extra["endpoint"] != "target" || sourceEvent.Extra["endpoint"] != "source" ||
		sourceEvent.Extra["source_work_log_reference"] != source.base.request.WorkLogReference ||
		targetEvent.Extra["target_work_log_reference"] != prepared.WorkLogReference {
		t.Fatalf("cross-boundary Work Log references differ: target=%#v source=%#v", targetEvent.Extra, sourceEvent.Extra)
	}
}

func sourceMessageRunPath(t *testing.T, options ExternalSourceMessageOptions) (string, string) {
	t.Helper()
	reference, err := sessionmove.ParseWorkLogReference(options.Request.WorkLogReference)
	if err != nil {
		t.Fatal(err)
	}
	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, "worklogs", reference.EffortID, "runs", reference.RunID), reference.ClaimID
}

func invalidMessageProjectsRoot(t *testing.T) string {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "ordinary-file")
	if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(parent, "projects")
}

//nolint:paralleltest // each case creates a native source checkout and private Work Log
func TestE2EExternalSourceMessageEvidenceRefusesBrokenAuthorityAndStorage(t *testing.T) {
	for _, test := range []struct {
		name, want string
		change     func(*testing.T, *ExternalSourceMessageOptions, string)
	}{
		{"home resolution", "not a directory", func(t *testing.T, o *ExternalSourceMessageOptions, _ string) {
			o.ProjectsRoot = invalidMessageProjectsRoot(t)
		}},
		{"private run missing", "no such file", func(t *testing.T, o *ExternalSourceMessageOptions, _ string) {
			run, _ := sourceMessageRunPath(t, *o)
			if err := os.RemoveAll(run); err != nil {
				t.Fatal(err)
			}
		}},
		{"immutable claim missing", "file does not exist", func(t *testing.T, o *ExternalSourceMessageOptions, _ string) {
			run, claimID := sourceMessageRunPath(t, *o)
			if err := os.Remove(filepath.Join(run, "claims", claimID+".json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"terminal projection missing", "file does not exist", func(t *testing.T, _ *ExternalSourceMessageOptions, worktree string) {
			if err := os.Remove(filepath.Join(worktree, workLogProjectionDirectory, workLogProjectionName)); err != nil {
				t.Fatal(err)
			}
		}},
		{"terminal projection changed", "identity conflicts", func(t *testing.T, o *ExternalSourceMessageOptions, worktree string) {
			reference, err := sessionmove.ParseWorkLogReference(o.Request.WorkLogReference)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeWorkLogProjection(worktree, workLogProjection{
				Version: 1, EffortID: reference.EffortID, RunID: reference.RunID,
				ClaimID: reference.ClaimID, Lifecycle: "active",
			}); err != nil {
				t.Fatal(err)
			}
		}},
		{"terminal unreadable", "invalid character", func(t *testing.T, o *ExternalSourceMessageOptions, _ string) {
			run, claimID := sourceMessageRunPath(t, *o)
			if err := os.WriteFile(filepath.Join(run, "terminals", claimID+".json"), []byte("broken json"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"terminal absent", "no immutable completed handoff", func(t *testing.T, o *ExternalSourceMessageOptions, _ string) {
			run, claimID := sourceMessageRunPath(t, *o)
			if err := os.Remove(filepath.Join(run, "terminals", claimID+".json")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		//nolint:paralleltest // each case mutates only its own native fixture
		t.Run(test.name, func(t *testing.T) {
			options, worktree := sourceMessageEvidenceFixture(t)
			before, err := readLocalEvents(worktree)
			if err != nil {
				t.Fatal(err)
			}
			test.change(t, &options, worktree)
			if _, err := RecordExternalSourceMessageSent(options); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("source authority refusal = %v, want %q", err, test.want)
			}
			after, err := readLocalEvents(worktree)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("source authority refusal changed local Work Log: err=%v", err)
			}
		})
	}
}

//nolint:paralleltest // each case creates a native target checkout and private Work Log
func TestE2EExternalTargetMessageEvidenceRefusesBrokenAuthorityAndStorage(t *testing.T) {
	for _, test := range []struct {
		name, want string
		change     func(*testing.T, *ExternalTargetMessageOptions, string)
	}{
		{"target path", "not a directory", func(t *testing.T, o *ExternalTargetMessageOptions, _ string) {
			o.ProjectsRoot = invalidMessageProjectsRoot(t)
		}},
		{"private claim missing", "no such file", func(t *testing.T, o *ExternalTargetMessageOptions, _ string) {
			o.ProjectsRoot = t.TempDir()
		}},
		{"attempt owner changed", "attempt", func(_ *testing.T, o *ExternalTargetMessageOptions, _ string) {
			o.Receipt.AttemptID = "000001-" + strings.Repeat("9", 32)
		}},
	} {
		//nolint:paralleltest // each case mutates only its own native fixture
		t.Run(test.name, func(t *testing.T) {
			options, worktree := targetMessageEvidenceFixture(t)
			before, err := readLocalEvents(worktree)
			if err != nil {
				t.Fatal(err)
			}
			test.change(t, &options, worktree)
			if _, err := RecordExternalTargetMessageReceived(options); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("target authority refusal = %v, want %q", err, test.want)
			}
			after, err := readLocalEvents(worktree)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("target authority refusal changed local Work Log: err=%v", err)
			}
		})
	}
}

func blockMessageWorkLogOutbox(t *testing.T, worktree string) func() {
	t.Helper()
	path := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, localWorkLogOutboxName)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	remove := func() {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if info, err := os.Lstat(path); err == nil && info.IsDir() {
			remove()
		}
	})
	return remove
}

//nolint:paralleltest // each case owns one native Git and Work Log fixture
func TestE2EExternalMessageEventsRepairAfterDurableJournalWrite(t *testing.T) {
	//nolint:paralleltest // source fixture mutates process-wide WB and Git environment
	t.Run("source", func(t *testing.T) {
		options, worktree := sourceMessageEvidenceFixture(t)
		before, err := readLocalEvents(worktree)
		if err != nil {
			t.Fatal(err)
		}
		removeBlock := blockMessageWorkLogOutbox(t, worktree)
		if _, err := RecordExternalSourceMessageSent(options); err == nil {
			t.Fatal("source message should report blocked outbox repair")
		}
		journal, err := readLocalEvents(worktree)
		if err != nil || len(journal) != len(before)+1 {
			t.Fatalf("source message journal after derivative failure = %d entries, %v", len(journal), err)
		}
		removeBlock()
		replayed, err := RecordExternalSourceMessageSent(options)
		if err != nil || !sameLocalEvent(replayed, journal[len(journal)-1]) {
			t.Fatalf("source message repair = %#v, %v", replayed, err)
		}
	})
	//nolint:paralleltest // target fixture mutates process-wide WB and Git environment
	t.Run("target", func(t *testing.T) {
		options, worktree := targetMessageEvidenceFixture(t)
		before, err := readLocalEvents(worktree)
		if err != nil {
			t.Fatal(err)
		}
		removeBlock := blockMessageWorkLogOutbox(t, worktree)
		if _, err := RecordExternalTargetMessageReceived(options); err == nil {
			t.Fatal("target message should report blocked outbox repair")
		}
		journal, err := readLocalEvents(worktree)
		if err != nil || len(journal) != len(before)+1 {
			t.Fatalf("target message journal after derivative failure = %d entries, %v", len(journal), err)
		}
		removeBlock()
		replayed, err := RecordExternalTargetMessageReceived(options)
		if err != nil || !sameLocalEvent(replayed, journal[len(journal)-1]) {
			t.Fatalf("target message repair = %#v, %v", replayed, err)
		}
	})
}
