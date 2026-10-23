//go:build e2e

package worktrees

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/worktreejournal"
)

func custodyNextWriteEvents(t *testing.T, worktree string, events []LocalWorkLogEvent) {
	t.Helper()
	var raw []byte
	for i := range events {
		events[i].Seq = i
		line, err := json.Marshal(events[i])
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, line...)
		raw = append(raw, '\n')
	}
	if err := os.WriteFile(filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, worktreejournal.EventsName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readLocalEvents(worktree)
	if err != nil || !reflect.DeepEqual(got, events) {
		t.Fatalf("native rewritten journal prerequisite: events=%+v err=%v", got, err)
	}
}

//nolint:paralleltest // inherited external target fixture configures process-wide native Git and agent environment
func TestE2ESessionCustodyNextExternalManifestAndJournalAuthority(t *testing.T) {
	for _, name := range []string{"multiple prompts", "missing prompt", "invalid prompt", "malformed events", "changed received", "missing received"} {
		//nolint:paralleltest // each case uses the inherited t.Setenv native fixture
		t.Run(name, func(t *testing.T) {
			f := newExternalTargetFixture(t)
			prepared, err := PrepareExternalSessionWorkLog(context.Background(), f.options)
			if err != nil {
				t.Fatal(err)
			}
			claim, _, unlock, err := loadExternalTargetClaim(f.base.projectsRoot, f.base.request, f.digest, f.worktree)
			if err != nil {
				t.Fatal(err)
			}
			unlock()
			if err := validateExternalTargetManifestAndJournal(f.worktree, f.base.request, f.digest, claim, externalReceiptModel(claim)); err != nil {
				t.Fatalf("valid control: %v", err)
			}
			promptDir := filepath.Join(f.worktree, journalRootDirectory, journalLocalDirectory, promptsDirectory)
			names, err := os.ReadDir(promptDir)
			if err != nil {
				t.Fatal(err)
			}
			var prompt string
			for _, e := range names {
				if promptFileName.MatchString(e.Name()) {
					prompt = filepath.Join(promptDir, e.Name())
				}
			}
			if prompt == "" {
				t.Fatal("fixture lacks native prompt")
			}
			body, err := os.ReadFile(prompt)
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			switch name {
			case "multiple prompts":
				if err := os.WriteFile(filepath.Join(promptDir, "0000-extra.md"), body, 0o600); err != nil {
					t.Fatal(err)
				}
				want = "multiple prompt"
			case "missing prompt":
				if err := os.Remove(prompt); err != nil {
					t.Fatal(err)
				}
				want = "exactly one"
			case "invalid prompt":
				if err := os.WriteFile(prompt, []byte("invalid header"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = "prompt"
			case "malformed events":
				if err := os.WriteFile(filepath.Join(f.worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, worktreejournal.EventsName), []byte("{invalid\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = "read external target Work Log journal"
			default:
				events, err := readLocalEvents(f.worktree)
				if err != nil {
					t.Fatal(err)
				}
				var out []LocalWorkLogEvent
				hit := false
				for _, e := range events {
					if e.ID == prepared.ReceivedEvent.ID {
						hit = true
						if name == "missing received" {
							continue
						}
						e.Message = "changed admitted received evidence"
					}
					out = append(out, e)
				}
				if !hit {
					t.Fatal("fixture lacks deterministic received event")
				}
				custodyNextWriteEvents(t, f.worktree, out)
				want = "received event conflicts"
				if name == "missing received" {
					want = "lacks deterministic received"
				}
			}
			beforeHead := gitTestOutput(t, f.worktree, "rev-parse", "HEAD")
			err = validateExternalTargetManifestAndJournal(f.worktree, f.base.request, f.digest, claim, externalReceiptModel(claim))
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("boundary=%v want%q", err, want)
			}
			if after := gitTestOutput(t, f.worktree, "rev-parse", "HEAD"); after != beforeHead {
				t.Fatal("read-only refusal moved HEAD")
			}
		})
	}
}

//nolint:paralleltest // native fixture uses process-wide Git/agent environment
func TestE2ESessionCustodyNextPublicationRacesAndNativeFailures(t *testing.T) {
	for _, same := range []bool{true, false} {
		//nolint:paralleltest // native fixture uses t.Setenv
		t.Run(map[bool]string{true: "same manifest wins", false: "different manifest wins"}[same], func(t *testing.T) {
			path := t.TempDir()
			gitTest(t, path, "init")
			manifest := Manifest{Version: 1, EffortID: "custody-next", EffortKind: "task", Repository: "acme/app", Branch: "topic", Provenance: "created", CreatedAt: time.Unix(100, 0).UTC()}
			winner := manifest
			if !same {
				winner.Branch = "different"
			}
			hit := false
			err := ensureExternalManifestBeforeWrite(path, manifest, func() {
				hit = true
				if err := WriteManifest(path, winner); err != nil {
					t.Fatal(err)
				}
			})
			if !hit || (same && err != nil) || (!same && (err == nil || !strings.Contains(err.Error(), "immutable"))) {
				t.Fatalf("native competing manifest same=%t hit=%t err=%v", same, hit, err)
			}
			got, err := ReadManifest(path)
			if err != nil || !reflect.DeepEqual(got, winner) {
				t.Fatalf("winner was overwritten: %+v %v", got, err)
			}
		})
	}
	f := newExternalTargetFixture(t)
	prepared, err := PrepareExternalSessionWorkLog(context.Background(), f.options)
	if err != nil {
		t.Fatal(err)
	}
	receipt := f.receipt(t, prepared)
	hit := false
	_, err = RecordExternalTargetCompleted(ExternalTargetCompletionOptions{ProjectsRoot: f.base.projectsRoot, Request: f.base.request, RequestDigest: f.digest, Receipt: receipt, WorktreeDir: f.worktree, beforeAppend: func() {
		hit = true
		if err := os.WriteFile(filepath.Join(f.worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, worktreejournal.EventsName), []byte("{publication refused\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}})
	if !hit || err == nil {
		t.Fatalf("completion native journal refusal hit=%t err=%v", hit, err)
	}
	raw, err := os.ReadFile(filepath.Join(f.worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, worktreejournal.EventsName))
	if err != nil || !bytes.Equal(raw, []byte("{publication refused\n")) {
		t.Fatalf("refusal altered corrupt journal: %q %v", raw, err)
	}
}

//nolint:paralleltest // native parked fixture configures process-wide Git and agent environment
func TestE2ESessionCustodyNextLocalAdmissionAndAttach(t *testing.T) {
	for _, name := range []string{"invalid checkout reference", "native Guard refusal", "open after admission", "invalid bundle time", "closed attach journal", "native append failure"} {
		//nolint:paralleltest // each native fixture calls t.Setenv
		t.Run(name, func(t *testing.T) {
			f, worktree, source, member, bundle := newParkedNextLocalFixture(t, "custody-next-"+strings.ReplaceAll(name, " ", "-"))
			custody := &ParkedLocalCustody{projectsRoot: f.projectsRoot, bundle: bundle, members: []parkedLocalMember{{member: member}}}
			t.Cleanup(custody.close)
			if name == "invalid checkout reference" {
				custody.members[0].member.WorktreeDir = filepath.Join(t.TempDir(), "missing")
				custody.members[0].member.WorkLogReference = "invalid"
				if err := custody.acquire(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "reference is unusable") {
					t.Fatalf("resolve refusal=%v", err)
				}
				return
			}
			if name == "native Guard refusal" {
				if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(t.TempDir(), "missing")+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := custody.acquire(context.Background(), 0); err == nil {
					t.Fatal("corrupt native Git metadata admitted")
				}
				return
			}
			if name == "open after admission" {
				hit := false
				custody.beforeWorktreeOpen = func(path string) {
					hit = true
					if err := os.Rename(path, path+".retained"); err != nil {
						t.Fatal(err)
					}
				}
				err := custody.acquire(context.Background(), 0)
				if !hit || !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("native open failure hit=%t err=%v", hit, err)
				}
				return
			}
			if err := custody.acquire(context.Background(), 0); err != nil {
				t.Fatal(err)
			}
			successor := source
			successor.WBSessionID = source.WBSessionID + "-successor"
			successor.PredecessorWBSessionID = source.WBSessionID
			successor.StartedAt = source.StartedAt.Add(time.Minute)
			attempt := "attempt-custody-next"
			index := uint64(1)
			want := ""
			observed := false
			var nativeAppendCause error
			switch name {
			case "invalid bundle time":
				custody.bundle.ParkedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
				want = "MarshalJSON"
			case "native append failure":
				custody.beforeAttachAppend = func(file *os.File) {
					observed = true
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
					_, _, nativeAppendCause = readLocalEventsForAppend(file)
					if nativeAppendCause == nil {
						t.Fatal("native read accepted the closed owned journal")
					}
				}
				want = "attach local parked successor to"
			case "closed attach journal":
				if err := custody.members[0].directory.Close(); err != nil {
					t.Fatal(err)
				}
				_, _, nativeAppendCause = readLocalEventsForAppend(custody.members[0].directory)
				if nativeAppendCause == nil {
					t.Fatal("native read accepted the closed owned journal")
				}
				want = "preflight parked local member"
			}
			before := parkedNextEvidence(t, f.projectsRoot, worktree)
			err := custody.Attach(context.Background(), successor, attempt, index)
			if err == nil || !strings.Contains(err.Error(), want) || (name == "native append failure" && (!observed || nativeAppendCause == nil || !errors.Is(err, nativeAppendCause))) {
				t.Fatalf("Attach=%v want%q observed=%t", err, want, observed)
			}
			if name == "closed attach journal" && (nativeAppendCause == nil || !errors.Is(err, nativeAppendCause)) {
				t.Fatalf("native closed-journal preflight cause was lost: %v; control=%v", err, nativeAppendCause)
			}
			assertParkedNextEvidence(t, f.projectsRoot, worktree, before)
		})
	}
}

//nolint:paralleltest // native parked fixture configures process-wide Git and agent environment
func TestE2ESessionCustodyNextRemoteAdmissionAndTip(t *testing.T) {
	for _, name := range []string{"invalid checkout", "native acquire failure", "origin drift", "absent remote branch"} {
		//nolint:paralleltest // each native fixture uses t.Setenv
		t.Run(name, func(t *testing.T) {
			f, worktree, _, member, bundle := newParkedNextLocalFixture(t, "custody-remote-"+strings.ReplaceAll(name, " ", "-"))
			want := ""
			switch name {
			case "invalid checkout":
				bundle.Worktrees[0].WorktreeDir = filepath.Join(t.TempDir(), "missing")
				bundle.Worktrees[0].WorkLogReference = "invalid"
				want = "reference is unusable"
			case "native acquire failure":
				if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(t.TempDir(), "missing")+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = "retain remote parked-session"
			case "origin drift":
				bundle.Worktrees[0].RepositoryRemote = "https://github.com/other/project.git"
				want = "identity changed since park"
			case "absent remote branch":
				canonical, err := openCanonicalRepository(member.CanonicalDir)
				if err != nil {
					t.Fatal(err)
				}
				defer canonical.close()
				tip, err := parkedRemoteBranchTip(context.Background(), canonical, member.RepositoryRemote, "absent-custody-next")
				if err != nil || tip != "" {
					t.Fatalf("native absent branch=%q %v", tip, err)
				}
				return
			}
			called := false
			err := WithParkedRemoteResumeCustody(context.Background(), f.projectsRoot, bundle, func() error { called = true; return nil })
			if called || err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("remote refusal callback=%t err=%v want%q", called, err, want)
			}
		})
	}
}

//nolint:paralleltest // native parked fixture configures process-wide Git and agent environment
func TestE2ESessionCustodyNextAggregateLockedFailureAndOrdering(t *testing.T) {
	f, worktree, source, member, _ := newParkedNextLocalFixture(t, "custody-aggregate")
	listed := ListResult{Repository: member.Repository, CanonicalDir: member.CanonicalDir, WorktreeDir: worktree, Branch: member.Branch}
	duplicate := listed
	duplicate.Repository = "zzzz/duplicate"
	called := false
	if err := CaptureParkedSessionAggregate(context.Background(), f.projectsRoot, []ListResult{duplicate, listed}, source, func([]sessionpark.Worktree) error { called = true; return nil }); err == nil || !strings.Contains(err.Error(), "duplicated") || called {
		t.Fatalf("duplicate ordering callback=%t err=%v", called, err)
	}
	assertParkedCaptureLockAvailable(t, worktree)
	hit := false
	err := captureParkedSessionAggregateBeforeCapture(context.Background(), f.projectsRoot, []ListResult{listed}, source, func([]sessionpark.Worktree) error { called = true; return nil }, func(m *parkedSessionCaptureMember) {
		hit = true
		if err := m.journal.Close(); err != nil {
			t.Fatal(err)
		}
	})
	if !hit || called || err == nil || !strings.Contains(err.Error(), "capture parked worktree") {
		t.Fatalf("actual held capture failure hit=%t callback=%t err=%v", hit, called, err)
	}
	assertParkedCaptureLockAvailable(t, worktree)
	journal, err := openJournalSubdirectory(worktree, worklogDirectory, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	unlock, err := lockLocalWorkLog(journal)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
	_, _, err = appendLocalEventUnderLock(worktree, journal, LocalWorkLogEvent{Version: 1, Type: LocalEventCheckpoint, Message: "non-owner progress retained", At: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	events, readErr := readLocalEvents(worktree)
	if readErr != nil || len(events) == 0 || events[len(events)-1].Type != LocalEventCheckpoint {
		t.Fatalf("native non-owner journal prerequisite: %+v, %v", events, readErr)
	}
	reference, owner, err := parkedSessionWorkLogSnapshotUnderLock(f.projectsRoot, worktree, source, journal)
	if err != nil || reference != member.WorkLogReference || owner != member.OwnerEventID {
		t.Fatalf("non-owner evidence changed exact source custody: %q %q %v", reference, owner, err)
	}
}

//nolint:paralleltest // inherited target fixture configures process-wide Git and agent environment
func TestE2ESessionCustodyNextParkedProjectionRepairRefusal(t *testing.T) {
	f := newParkedTargetCompletionFixture(t, "resume-custody-next-repair")
	record := session.Record{PID: f.successor.PID, WBSessionID: f.successor.WBSessionID, PredecessorWBSessionID: f.successor.PredecessorWBSessionID, Machine: f.successor.TargetMachine, Runtime: f.successor.Runtime, Model: f.successor.Model, TmuxName: f.successor.TmuxName, HandoffID: f.request.ResumeID, StartedAt: f.successor.StartedAt}
	hit := false
	options := ParkedSessionWorkLogPrepareOptions{ProjectsRoot: f.base.projectsRoot, Request: f.request, RequestDigest: f.digest, Member: f.member, ReceivedAt: f.request.CreatedAt, Session: record, AttemptID: f.successor.AttemptID, AttemptIndex: f.successor.AttemptIndex, WorktreeDir: f.worktree, PinnedCommit: f.member.Commit, beforeProjectionRepair: func() {
		hit = true
		if err := os.WriteFile(filepath.Join(f.worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, worktreejournal.EventsName), []byte("{repair cannot read\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}}
	_, err := PrepareParkedSessionWorkLog(context.Background(), options)
	if !hit || err == nil {
		t.Fatalf("native projection repair refusal hit=%t err=%v", hit, err)
	}
	raw, err := os.ReadFile(filepath.Join(f.worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, worktreejournal.EventsName))
	if err != nil || !bytes.Equal(raw, []byte("{repair cannot read\n")) {
		t.Fatalf("repair refusal mutated native journal: %q %v", raw, err)
	}
}
