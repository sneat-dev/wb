//go:build e2e

package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

func externalOfferOptions(fixture *externalSourceFixture, lock *sessionmove.ExecutionLock) ExternalSourceOfferOptions {
	return ExternalSourceOfferOptions{
		Store: fixture.store, ExecutionLock: lock, ProjectsRoot: fixture.base.projectsRoot,
		Request: fixture.base.request, RequestDigest: fixture.digest, SourceSession: fixture.source,
	}
}

func sourceClaimPath(fixture *externalSourceFixture) string {
	return filepath.Join(fixture.base.home, "worklogs", fixture.claim.EffortID, "runs", fixture.claim.RunID,
		"claims", fixture.claim.ClaimID+".json")
}

//nolint:paralleltest // each source fixture configures process-wide Git and agent environment
func TestE2EExternalSourceOfferRefusesCorruptAuthorityBeforeLocalEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*testing.T, *externalSourceFixture, *ExternalSourceOfferOptions)
	}{
		{"request differs from admitted aggregate", "does not match exact admitted request", func(_ *testing.T, _ *externalSourceFixture, options *ExternalSourceOfferOptions) {
			options.Request.SourceOfferMessage = "different"
		}},
		{"source session differs", "does not match admitted predecessor identity", func(_ *testing.T, _ *externalSourceFixture, options *ExternalSourceOfferOptions) {
			options.SourceSession.Machine = "different"
		}},
		{"offer predates source", "predates the predecessor session", func(_ *testing.T, _ *externalSourceFixture, options *ExternalSourceOfferOptions) {
			options.SourceSession.StartedAt = options.Request.CreatedAt.Add(time.Second)
		}},
		{"private claim missing", "", func(t *testing.T, fixture *externalSourceFixture, _ *ExternalSourceOfferOptions) {
			if err := os.Remove(sourceClaimPath(fixture)); err != nil {
				t.Fatal(err)
			}
		}},
		{"projection missing", "", func(t *testing.T, fixture *externalSourceFixture, _ *ExternalSourceOfferOptions) {
			if err := os.Remove(filepath.Join(fixture.worktree, workLogProjectionDirectory, workLogProjectionName)); err != nil {
				t.Fatal(err)
			}
		}},
		{"private claim identity changed", "identity conflicts", func(t *testing.T, fixture *externalSourceFixture, _ *ExternalSourceOfferOptions) {
			claim := fixture.claim
			claim.ClaimID = "different"
			raw, err := json.Marshal(claim)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sourceClaimPath(fixture), raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"source branch changed", "corroborate active source Work Log", func(t *testing.T, fixture *externalSourceFixture, _ *ExternalSourceOfferOptions) {
			gitTest(t, fixture.worktree, "branch", "-m", "renamed-after-checkpoint")
		}},
		{"source dirty", "source worktree changed after its admitted handoff checkpoint", func(t *testing.T, fixture *externalSourceFixture, _ *ExternalSourceOfferOptions) {
			if err := os.WriteFile(filepath.Join(fixture.worktree, "dirty-after-checkpoint"), []byte("changed"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"predecessor no longer live", "admitted predecessor session is not live", func(_ *testing.T, _ *externalSourceFixture, options *ExternalSourceOfferOptions) {
			options.SourceSession.PID = 999999
		}},
	} {
		//nolint:paralleltest // each fixture calls t.Setenv and runs native Git
		t.Run(tc.name, func(t *testing.T) {
			fixture := newExternalSourceFixture(t)
			lock := fixture.lock(t)
			options := externalOfferOptions(fixture, lock)
			tc.change(t, fixture, &options)
			result, err := EnsureExternalSourceOfferEvidence(options)
			if err == nil || (tc.want != "" && !strings.Contains(err.Error(), tc.want)) || result.OfferEvent.ID != "" {
				t.Fatalf("source authority refusal = (%#v, %v), want %q before local offer", result, err, tc.want)
			}
		})
	}
}

//nolint:paralleltest // source fixture configures process-wide Git and agent environment
func TestE2EExternalSourceOfferReportsLegacyHandoverReadFailure(t *testing.T) {
	fixture := newExternalSourceFixture(t)
	request := fixture.base.request
	request.HandoffID = "handoff-legacy-missing-document"
	request.HandoverContent = ""
	request.HandoverPath = "private/missing-handover.md"
	raw, err := sessionmove.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionmove.DigestBytes(raw)
	if _, err := fixture.store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	lock, err := fixture.store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	result, err := EnsureExternalSourceOfferEvidence(ExternalSourceOfferOptions{
		Store: fixture.store, ExecutionLock: lock, ProjectsRoot: fixture.base.projectsRoot,
		Request: request, RequestDigest: digest, SourceSession: fixture.source,
	})
	if err == nil || result.OfferEvent.ID != "" {
		t.Fatalf("missing admitted legacy handover = (%#v, %v)", result, err)
	}
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "read admitted source handover") {
		t.Fatalf("missing handover reported as %v, want distinct read failure", err)
	}
}

//nolint:paralleltest // source fixture configures process-wide Git and agent environment
func TestE2EExternalSourceOfferDistinguishesHandoverDigestMismatch(t *testing.T) {
	fixture := newExternalSourceFixture(t)
	request := fixture.base.request
	request.HandoffID = "handoff-legacy-wrong-document"
	request.HandoverContent = ""
	request.HandoverPath = "README.md"
	raw, err := sessionmove.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionmove.DigestBytes(raw)
	if _, err := fixture.store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	lock, err := fixture.store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	result, err := EnsureExternalSourceOfferEvidence(ExternalSourceOfferOptions{
		Store: fixture.store, ExecutionLock: lock, ProjectsRoot: fixture.base.projectsRoot,
		Request: request, RequestDigest: digest, SourceSession: fixture.source,
	})
	if err == nil || !strings.Contains(err.Error(), "source handover document does not match admitted immutable bytes") || result.OfferEvent.ID != "" {
		t.Fatalf("mismatched readable handover = (%#v, %v)", result, err)
	}
}

//nolint:paralleltest // source fixture configures process-wide Git and agent environment
func TestE2EExternalSourceOfferRefusesForgedOwnerAfterDurableOffer(t *testing.T) {
	fixture := newExternalSourceFixture(t)
	lock := fixture.lock(t)
	options := externalOfferOptions(fixture, lock)
	forged := externalSourceOwnerEvent(options, fixture.claim)
	forged.Message = "forged owner event"
	if _, _, err := appendLocalEventWithoutCustody(fixture.worktree, forged); err != nil {
		t.Fatal(err)
	}
	result, err := EnsureExternalSourceOfferEvidence(options)
	if err == nil || result.OfferEvent.ID == "" || result.OwnerEvent.ID != "" {
		t.Fatalf("forged source owner = (%#v, %v)", result, err)
	}
}

//nolint:paralleltest // native fixtures configure process-wide Git and agent environment
func TestE2EExternalSourceOfferStopsAtCorruptStorageStage(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		inject     func(*testing.T, *externalSourceFixture, *ExternalSourceOfferOptions)
	}{
		{"aggregate opener", "load exact source offer aggregate", func(_ *testing.T, fixture *externalSourceFixture, options *ExternalSourceOfferOptions) {
			options.Store = sessionmove.NewStore(filepath.Join(fixture.base.home, "wrong-store"))
		}},
		{"home resolver", "", func(t *testing.T, fixture *externalSourceFixture, options *ExternalSourceOfferOptions) {
			loop := filepath.Join(fixture.base.root, "cyclic-projects-root")
			if err := os.Symlink(loop, loop); err != nil {
				t.Fatal(err)
			}
			options.ProjectsRoot = loop
		}},
		{"private run opener", "", func(t *testing.T, fixture *externalSourceFixture, options *ExternalSourceOfferOptions) {
			options.hooks.afterOfferedPhase = func() error {
				return os.RemoveAll(filepath.Join(fixture.base.home, "worklogs", fixture.claim.EffortID, "runs", fixture.claim.RunID))
			}
		}},
		{"local offer journal read", "read source Work Log offer repair authority", func(t *testing.T, fixture *externalSourceFixture, options *ExternalSourceOfferOptions) {
			options.hooks.afterOfferedPhase = func() error {
				if err := writeWorkLogProjection(fixture.worktree, workLogProjection{
					Version: 1, EffortID: fixture.claim.EffortID, RunID: fixture.claim.RunID, ClaimID: fixture.claim.ClaimID, Lifecycle: "terminal",
				}); err != nil {
					return err
				}
				path := filepath.Join(fixture.worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory)
				if err := os.MkdirAll(path, 0o700); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(path, localWorkLogEventsName), []byte("malformed record\n"), 0o600)
			}
		}},
		{"local owner journal read", "", func(t *testing.T, fixture *externalSourceFixture, options *ExternalSourceOfferOptions) {
			options.hooks.afterOffer = func() error {
				path := filepath.Join(fixture.worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, localWorkLogEventsName)
				return os.WriteFile(path, []byte("malformed record\n"), 0o600)
			}
		}},
	} {
		//nolint:paralleltest // each fixture calls t.Setenv and runs native Git
		t.Run(tc.name, func(t *testing.T) {
			fixture := newExternalSourceFixture(t)
			options := externalOfferOptions(fixture, fixture.lock(t))
			tc.inject(t, fixture, &options)
			result, err := EnsureExternalSourceOfferEvidence(options)
			if err == nil || (tc.want != "" && !strings.Contains(err.Error(), tc.want)) || result.OwnerEvent.ID != "" {
				t.Fatalf("corrupt %s offer = (%#v, %v), want %q refusal", tc.name, result, err, tc.want)
			}
		})
	}
}

func externalTargetSessionForSource(fixture *externalSourceFixture, worktree string) ExternalSessionWorkLogPrepareOptions {
	request := fixture.base.request
	startedAt := request.CreatedAt.Add(time.Minute)
	record := session.Record{
		PID: os.Getpid(), WBSessionID: request.SuccessorWBSessionID, Machine: request.TargetMachine,
		Runtime: "codex", Model: "gpt-5", NativeHarnessID: "native-target",
		TmuxName:               "wb-session-" + request.SuccessorWBSessionID,
		PredecessorWBSessionID: request.PredecessorWBSessionID, HandoffID: request.HandoffID,
		StartedAt: startedAt,
	}
	return ExternalSessionWorkLogPrepareOptions{
		ProjectsRoot: fixture.base.projectsRoot, Request: request, RequestDigest: fixture.digest,
		ReceivedAt: request.CreatedAt.Add(30 * time.Second), Session: record,
		AttemptID: "000001-" + strings.Repeat("1", 32), AttemptIndex: 1,
		WorktreeDir: worktree, PinnedCommit: request.BundleCommit, HandoverBytes: fixture.base.handover,
	}
}

//nolint:paralleltest // source fixture configures process-wide Git and agent environment
func TestE2EExternalCustodySourceOfferToTargetClaimAndRetry(t *testing.T) {
	fixture := newExternalSourceFixture(t)
	lock := fixture.lock(t)
	sourceOptions := externalOfferOptions(fixture, lock)
	source, err := EnsureExternalSourceOfferEvidence(sourceOptions)
	if err != nil || source.OfferEvent.ID == "" || source.OwnerEvent.ID == "" {
		t.Fatalf("source offer = (%#v, %v)", source, err)
	}
	targetWorktree := fixture.base.targetWorktree()
	if err := os.MkdirAll(filepath.Dir(targetWorktree), 0o700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.base.canonical, "worktree", "add", "-b", "wb-session/"+fixture.base.request.HandoffID,
		targetWorktree, fixture.base.request.BundleCommit)
	targetOptions := externalTargetSessionForSource(fixture, targetWorktree)
	target, err := PrepareExternalSessionWorkLog(context.Background(), targetOptions)
	if err != nil || target.ClaimID == "" || target.ReceivedEvent.ID == "" || target.OwnerEvent.ID == "" {
		t.Fatalf("target publication = (%#v, %v)", target, err)
	}
	sourceRetry, err := EnsureExternalSourceOfferEvidence(sourceOptions)
	if err != nil || !sourceRetry.Replayed || sourceRetry.OfferEvent.ID != source.OfferEvent.ID || sourceRetry.OwnerEvent.ID != source.OwnerEvent.ID {
		t.Fatalf("source retry = (%#v, %v)", sourceRetry, err)
	}
	targetRetry, err := PrepareExternalSessionWorkLog(context.Background(), targetOptions)
	if err != nil || !targetRetry.Replayed || targetRetry.ClaimID != target.ClaimID ||
		targetRetry.ReceivedEvent.ID != target.ReceivedEvent.ID || targetRetry.OwnerEvent.ID != target.OwnerEvent.ID {
		t.Fatalf("target retry = (%#v, %v)", targetRetry, err)
	}
	projection, err := readWorkLogProjection(targetWorktree)
	if err != nil || projection.EffortID != "session-move" || projection.RunID != "session-move-run" ||
		projection.ClaimID != target.ClaimID || projection.Lifecycle != "active" {
		t.Fatalf("target projection = (%#v, %v)", projection, err)
	}
	reference, err := sessionmove.ParseWorkLogReference(target.WorkLogReference)
	if err != nil {
		t.Fatal(err)
	}
	claimBytes, err := os.ReadFile(filepath.Join(fixture.base.home, "worklogs", reference.EffortID, "runs", reference.RunID,
		"claims", reference.ClaimID+".json"))
	if err != nil {
		t.Fatalf("read target immutable claim: %v", err)
	}
	var claim workLogClaim
	if err := json.Unmarshal(claimBytes, &claim); err != nil {
		t.Fatalf("decode target immutable claim: %v", err)
	}
	if claim.EffortID != reference.EffortID || claim.RunID != reference.RunID || claim.ClaimID != target.ClaimID ||
		claim.Worktree != targetWorktree || claim.Branch != "wb-session/"+fixture.base.request.HandoffID ||
		claim.ExternalHandoff == nil || claim.ExternalHandoff.RequestDigest != string(fixture.digest) ||
		claim.ExternalHandoff.SourceWorkLogReference != fixture.base.request.WorkLogReference ||
		claim.ExternalHandoff.TargetWorkLogReference != target.WorkLogReference {
		t.Fatalf("target immutable claim lost admitted lineage: %#v", claim)
	}
	outboxBytes, err := os.ReadFile(filepath.Join(fixture.base.home, "worklogs", reference.EffortID, "outbox",
		reference.RunID+"-"+reference.ClaimID+"-claimed.json"))
	if err != nil {
		t.Fatalf("read target public outbox: %v", err)
	}
	var event workLogPublicEvent
	if err := json.Unmarshal(outboxBytes, &event); err != nil {
		t.Fatalf("decode target public outbox: %v", err)
	}
	if event.Type != "worktree.claimed" || event.EffortID != claim.EffortID || event.RunID != claim.RunID ||
		event.ClaimID != claim.ClaimID || !event.At.Equal(claim.RecordedAt) || event.Lifecycle != "active" ||
		event.ExternalHandoff == nil || *event.ExternalHandoff != *claim.ExternalHandoff {
		t.Fatalf("target outbox lost claim identity or source lineage: %#v", event)
	}
	receipt := fixture.authorizeSeal(t, lock)
	if receipt.TargetWorkLogReference != target.WorkLogReference {
		t.Fatalf("durable receipt target %q differs from published claim %q", receipt.TargetWorkLogReference, target.WorkLogReference)
	}
	sealed, err := SealExternalSessionWorkLog(ExternalSourceSealOptions{
		Store: fixture.store, ExecutionLock: lock, ProjectsRoot: fixture.base.projectsRoot,
		Request: fixture.base.request, RequestDigest: fixture.digest, Receipt: receipt, SourceSession: fixture.source,
	})
	if err != nil || sealed.TargetWorkLogReference != target.WorkLogReference || sealed.SealedAt.IsZero() {
		t.Fatalf("receipt-authorized source seal = (%#v, %v)", sealed, err)
	}
	sourceProjection, err := readWorkLogProjection(fixture.worktree)
	if err != nil || sourceProjection.Lifecycle != "terminal" {
		t.Fatalf("sealed source projection = (%#v, %v)", sourceProjection, err)
	}
}

//nolint:paralleltest // native fixture configures process-wide Git and agent environment
func TestE2EExternalTargetClaimRejectsConflictingRetry(t *testing.T) {
	fixture := newExternalTargetFixture(t)
	first, err := PrepareExternalSessionWorkLog(context.Background(), fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	changed := fixture.options
	changed.ReceivedAt = changed.ReceivedAt.Add(time.Second)
	second, err := PrepareExternalSessionWorkLog(context.Background(), changed)
	if err == nil || !strings.Contains(err.Error(), "immutable external target Work Log claim conflicts") || !second.Replayed {
		t.Fatalf("conflicting immutable target retry = (%#v, %v)", second, err)
	}
	projection, err := readWorkLogProjection(fixture.worktree)
	if err != nil || projection.ClaimID != first.ClaimID || projection.Lifecycle != "active" {
		t.Fatalf("original projection changed = (%#v, %v)", projection, err)
	}
}

//nolint:paralleltest // native fixture configures process-wide Git and agent environment
func TestE2EExternalTargetRefusesCorruptRunIndex(t *testing.T) {
	fixture := newExternalTargetFixture(t)
	options := fixture.options
	options.hooks.afterClaim = func() error {
		reference, err := sessionmove.ExpectedTargetWorkLogReference(options.Request, options.RequestDigest)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(fixture.base.home, "worklogs", reference.EffortID, "runs", reference.RunID, "run.json"), []byte(`{"effort_id":"wrong"}`), 0o600)
	}
	result, err := PrepareExternalSessionWorkLog(context.Background(), options)
	if err == nil || result.ClaimID != "" {
		t.Fatalf("corrupt run-index refusal = (%#v, %v)", result, err)
	}
}

func targetOutboxPath(fixture *externalTargetFixture) string {
	reference, _ := sessionmove.ExpectedTargetWorkLogReference(fixture.options.Request, fixture.options.RequestDigest)
	return filepath.Join(fixture.base.home, "worklogs", reference.EffortID, "outbox")
}

//nolint:paralleltest // native fixtures configure process-wide Git and agent environment
func TestE2EExternalTargetStopsAtCorruptPublicationStage(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		inject     func(*testing.T, *externalTargetFixture, *ExternalSessionWorkLogPrepareOptions)
	}{
		{"manifest", "manifest", func(t *testing.T, fixture *externalTargetFixture, options *ExternalSessionWorkLogPrepareOptions) {
			prepared, err := prepareExternalTarget(context.Background(), *options)
			if err != nil {
				t.Fatal(err)
			}
			options.hooks.afterRunIndex = func() error {
				manifest := prepared.manifest
				manifest.Repository = "other/repo"
				return WriteManifest(fixture.worktree, manifest)
			}
		}},
		{"prompt", "prompt", func(t *testing.T, fixture *externalTargetFixture, options *ExternalSessionWorkLogPrepareOptions) {
			options.hooks.afterRunIndex = func() error {
				_, err := AppendPrompt(fixture.worktree, PromptHeader{At: options.ReceivedAt, Source: PromptSourceAgent, Runtime: "other", Model: "other", Slug: "wrong"}, []byte("wrong"))
				return err
			}
		}},
		{"received journal", "receipt evidence", func(t *testing.T, fixture *externalTargetFixture, options *ExternalSessionWorkLogPrepareOptions) {
			options.hooks.afterRunIndex = func() error {
				path := filepath.Join(fixture.worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory)
				if err := os.MkdirAll(path, 0o700); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(path, localWorkLogEventsName), []byte("malformed record\n"), 0o600)
			}
		}},
		{"outbox opener", "", func(t *testing.T, fixture *externalTargetFixture, options *ExternalSessionWorkLogPrepareOptions) {
			options.hooks.afterJournal = func() error {
				return os.WriteFile(targetOutboxPath(fixture), []byte("occupied"), 0o600)
			}
		}},
		{"immutable outbox", "publish external target Work Log outbox", func(t *testing.T, fixture *externalTargetFixture, options *ExternalSessionWorkLogPrepareOptions) {
			options.hooks.afterJournal = func() error {
				path := targetOutboxPath(fixture)
				if err := os.Mkdir(path, 0o700); err != nil {
					return err
				}
				reference, err := sessionmove.ExpectedTargetWorkLogReference(options.Request, options.RequestDigest)
				if err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(path, reference.RunID+"-"+reference.ClaimID+"-claimed.json"), []byte("conflict"), 0o600)
			}
		}},
		{"projection", "", func(t *testing.T, fixture *externalTargetFixture, options *ExternalSessionWorkLogPrepareOptions) {
			options.hooks.afterOutbox = func() error {
				return os.Symlink(fixture.base.home, filepath.Join(fixture.worktree, workLogProjectionDirectory))
			}
		}},
		{"local repair", "", func(t *testing.T, fixture *externalTargetFixture, options *ExternalSessionWorkLogPrepareOptions) {
			options.hooks.afterProjection = func() error {
				path := filepath.Join(fixture.worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, localWorkLogEventsName)
				return os.WriteFile(path, []byte("malformed record\n"), 0o600)
			}
		}},
	} {
		//nolint:paralleltest // each fixture calls t.Setenv and runs native Git
		t.Run(tc.name, func(t *testing.T) {
			fixture := newExternalTargetFixture(t)
			options := fixture.options
			tc.inject(t, fixture, &options)
			result, err := PrepareExternalSessionWorkLog(context.Background(), options)
			if err == nil || (tc.want != "" && !strings.Contains(err.Error(), tc.want)) || result.ClaimID != "" {
				t.Fatalf("corrupt %s publication = (%#v, %v), want %q refusal", tc.name, result, err, tc.want)
			}
		})
	}
}

//nolint:paralleltest // native fixtures configure process-wide Git and agent environment
func TestE2EExternalTargetRefusesInvalidHomeAndPrivateRun(t *testing.T) {
	for _, stage := range []string{"home", "run"} {
		//nolint:paralleltest // each fixture calls t.Setenv and runs native Git
		t.Run(stage, func(t *testing.T) {
			fixture := newExternalTargetFixture(t)
			options := fixture.options
			if stage == "home" {
				loop := filepath.Join(fixture.base.root, "cyclic-projects-root")
				if err := os.Symlink(loop, loop); err != nil {
					t.Fatal(err)
				}
				options.ProjectsRoot = loop
			} else {
				if err := os.MkdirAll(fixture.base.home, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(fixture.base.home, "worklogs"), []byte("occupied"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := PrepareExternalSessionWorkLog(context.Background(), options)
			if err == nil || result.ClaimID != "" {
				t.Fatalf("invalid %s = (%#v, %v)", stage, result, err)
			}
		})
	}
}

//nolint:paralleltest // native fixture configures process-wide Git and agent environment
func TestE2EExternalTargetPublicationPortsFailClosedAndRepair(t *testing.T) {
	for _, stage := range []string{"receipt event", "owner event", "outbox close", "outbox write and close"} {
		//nolint:paralleltest // each fixture calls t.Setenv and runs native Git
		t.Run(stage, func(t *testing.T) {
			fixture := newExternalTargetFixture(t)
			options := fixture.options
			ports := defaultExternalTargetPublicationPorts()
			fault := errors.New("injected descriptor or journal failure")
			switch stage {
			case "receipt event", "owner event":
				original := ports.appendEvent
				calls := 0
				ports.appendEvent = func(worktree string, event LocalWorkLogEvent) (LocalWorkLogEvent, LocalWorkLogProjection, error) {
					calls++
					if stage == "receipt event" || calls == 2 {
						return LocalWorkLogEvent{}, LocalWorkLogProjection{}, fault
					}
					return original(worktree, event)
				}
			case "outbox close", "outbox write and close":
				ports.closeOutbox = func(file *os.File) error {
					if err := file.Close(); err != nil {
						return err
					}
					return fault
				}
				if stage == "outbox write and close" {
					options.hooks.afterJournal = func() error {
						path := targetOutboxPath(fixture)
						if err := os.Mkdir(path, 0o700); err != nil {
							return err
						}
						reference, err := sessionmove.ExpectedTargetWorkLogReference(options.Request, options.RequestDigest)
						if err != nil {
							return err
						}
						return os.WriteFile(filepath.Join(path, reference.RunID+"-"+reference.ClaimID+"-claimed.json"), []byte("conflict"), 0o600)
					}
				}
			}
			result, err := ports.prepare(context.Background(), options)
			if !errors.Is(err, fault) || result.ClaimID != "" {
				t.Fatalf("%s failure = (%#v, %v), want sentinel and no completed publication", stage, result, err)
			}
			if stage == "outbox write and close" && !strings.Contains(err.Error(), "publish external target Work Log outbox") {
				t.Fatalf("immutable write error was masked by close failure: %v", err)
			}
			if stage == "outbox close" && !strings.Contains(err.Error(), "close external target Work Log outbox") {
				t.Fatalf("outbox close failure was ignored: %v", err)
			}
			if _, projectionErr := readWorkLogProjection(fixture.worktree); !errors.Is(projectionErr, os.ErrNotExist) {
				t.Fatalf("%s unexpectedly published projection: %v", stage, projectionErr)
			}
			if stage != "outbox write and close" {
				repaired, repairErr := PrepareExternalSessionWorkLog(context.Background(), fixture.options)
				if repairErr != nil || repaired.ClaimID == "" {
					t.Fatalf("%s retry repair = (%#v, %v)", stage, repaired, repairErr)
				}
			}
		})
	}
}

//nolint:paralleltest // native fixture configures process-wide Git and agent environment
func TestE2EExternalSourceOfferPortsFailClosedAndRepair(t *testing.T) {
	for _, stage := range []string{"offered phase", "Git status", "offer event", "owner event"} {
		//nolint:paralleltest // each fixture calls t.Setenv and runs native Git
		t.Run(stage, func(t *testing.T) {
			fixture := newExternalSourceFixture(t)
			options := externalOfferOptions(fixture, fixture.lock(t))
			ports := defaultExternalSourceOfferPorts()
			fault := errors.New("injected custody transition failure")
			switch stage {
			case "offered phase":
				ports.appendOffered = func(ExternalSourceOfferOptions) error { return fault }
			case "Git status":
				ports.gitStatus = func(string) (string, error) { return "", fault }
			case "offer event", "owner event":
				original := ports.appendEvent
				calls := 0
				ports.appendEvent = func(worktree string, event LocalWorkLogEvent) (LocalWorkLogEvent, LocalWorkLogProjection, error) {
					calls++
					if stage == "offer event" || calls == 2 {
						return LocalWorkLogEvent{}, LocalWorkLogProjection{}, fault
					}
					return original(worktree, event)
				}
			}
			partial, err := ports.ensure(options)
			if !errors.Is(err, fault) || partial.OwnerEvent.ID != "" || (stage == "owner event") != (partial.OfferEvent.ID != "") {
				t.Fatalf("%s source partial = (%#v, %v)", stage, partial, err)
			}
			repaired, repairErr := EnsureExternalSourceOfferEvidence(options)
			if repairErr != nil || repaired.OfferEvent.ID == "" || repaired.OwnerEvent.ID == "" {
				t.Fatalf("%s retry repair = (%#v, %v)", stage, repaired, repairErr)
			}
		})
	}
}
