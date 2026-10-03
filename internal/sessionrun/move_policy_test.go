package sessionrun

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessioncourier"
	"github.com/sneat-dev/wb/internal/sessioncustody"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionreceive"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// cwDepsMoveFixture is the admitted request every resume test starts from.
func cwDepsMoveFixture(t *testing.T, store sessionmove.Store) (session.Record, sessionmove.Request, []byte, sessionmove.Digest) {
	t.Helper()
	source := session.Record{PID: 11, WBSessionID: "wbs-source", Machine: "laptop", Runtime: "codex", StartedAt: time.Now().UTC()}
	request := completeMoveTestRequest(sessionmove.Request{
		SchemaVersion: sessionmove.RequestSchemaVersion, HandoffID: "handoff-cwdeps",
		SuccessorWBSessionID: "wbs-target", PredecessorWBSessionID: source.WBSessionID,
		SourceMachine: source.Machine, TargetMachine: "hetzner-vm1", RepositoryRemote: "/tmp/acme/app.git",
		Branch: "feature/cwdeps", SourceWorkCommit: strings.Repeat("a", 40), BundleCommit: strings.Repeat("b", 40),
		HandoverPath: ".wb/handoffs/handoff-cwdeps.md", HandoverDigest: sessionmove.DigestBytes([]byte("handover")),
		SourceRuntime: "codex", SourceModel: "gpt-5", CreatedAt: time.Now().UTC(),
	})
	raw := mustEncodeMoveTestRequest(t, request)
	digest := sessionmove.DigestBytes(raw)
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	return source, request, raw, digest
}

func cwDepsMoveConfig(target sessionmove.TargetConfig) sessionmove.Config {
	return sessionmove.Config{Targets: map[string]sessionmove.TargetConfig{target.Machine: target}}
}

func cwDepsSSHTarget() sessionmove.TargetConfig {
	return sessionmove.TargetConfig{Machine: "hetzner-vm1", DefaultCourier: sessionmove.CourierSSH,
		SSH: &sessionmove.SSHConfig{Host: "hetzner-vm1", WBPath: "/home/ai/go/bin/wb"}}
}

// TestCwDepsSessionMoveResumeRefusalBranches walks the pre-delivery guards of
// runSessionMoveResume one at a time.
func TestCwDepsSessionMoveResumeRefusalBranches(t *testing.T) {
	base := func(store sessionmove.Store, source session.Record, ok bool) MoveDependencies {
		return MoveDependencies{
			DefaultConfigPath: func() string { return "/tmp/cw-deps-wb.yaml" },
			ResolveSource:     func(string) (session.Record, bool, error) { return source, ok, nil },
			Store:             func(string) (sessionmove.Store, error) { return store, nil },
		}
	}
	// A source-resolution failure is surfaced as-is.
	deps := base(sessionmove.NewStore(t.TempDir()), session.Record{}, false)
	deps.ResolveSource = func(string) (session.Record, bool, error) {
		return session.Record{}, false, errors.New("session dir unreadable")
	}
	if _, err := executeResume(deps, "handoff-cwdeps", ""); err == nil ||
		!strings.Contains(err.Error(), "session dir unreadable") {
		t.Fatalf("resolveSource error = %v", err)
	}
	// No live predecessor session.
	deps = base(sessionmove.NewStore(t.TempDir()), session.Record{}, false)
	if _, err := executeResume(deps, "handoff-cwdeps", ""); err == nil ||
		!strings.Contains(err.Error(), "requires the live registered predecessor session") {
		t.Fatalf("no live session = %v", err)
	}
	source := session.Record{PID: 11, WBSessionID: "wbs-source", Machine: "laptop", Runtime: "codex"}
	// A store that cannot be opened.
	deps = base(sessionmove.NewStore(t.TempDir()), source, true)
	deps.Store = func(string) (sessionmove.Store, error) { return sessionmove.Store{}, errors.New("store unavailable") }
	if _, err := executeResume(deps, "handoff-cwdeps", ""); err == nil ||
		!strings.Contains(err.Error(), "store unavailable") {
		t.Fatalf("store error = %v", err)
	}
	// An unknown handoff ID names no request.
	if _, err := executeResume(base(sessionmove.NewStore(t.TempDir()), source, true), "handoff-cwdeps", ""); err == nil {
		t.Fatal("an unknown handoff must be refused")
	}
	// A handoff that belongs to another predecessor is refused.
	otherStore := sessionmove.NewStore(t.TempDir())
	_, request, _, _ := cwDepsMoveFixture(t, otherStore)
	mismatched := source
	mismatched.WBSessionID = "wbs-someone-else"
	if _, err := executeResume(base(otherStore, mismatched, true), request.HandoffID, ""); err == nil ||
		!strings.Contains(err.Error(), "belongs to predecessor session") {
		t.Fatalf("predecessor mismatch = %v", err)
	}
}

// TestCwDepsSessionMoveResumeRepairsARouteOrRefuses covers the branch where an
// accepted request has no durable route yet.
func TestCwDepsSessionMoveResumeRepairsARouteOrRefuses(t *testing.T) {
	store := sessionmove.NewStore(t.TempDir())
	source, request, _, _ := cwDepsMoveFixture(t, store)
	deps := MoveDependencies{
		DefaultConfigPath: func() string { return "/tmp/cw-deps-wb.yaml" },
		ResolveSource:     func(string) (session.Record, bool, error) { return source, true, nil },
		Store:             func(string) (sessionmove.Store, error) { return store, nil },
		LoadConfig:        func(string) (sessionmove.Config, error) { return sessionmove.Config{}, errors.New("config unreadable") },
	}
	// A config that cannot be read is surfaced.
	if _, err := executeResume(deps, request.HandoffID, ""); err == nil ||
		!strings.Contains(err.Error(), "config unreadable") {
		t.Fatalf("unreadable config = %v", err)
	}
	// A config without the requested target is a named refusal.
	deps.LoadConfig = func(string) (sessionmove.Config, error) {
		return cwDepsMoveConfig(sessionmove.TargetConfig{Machine: "somewhere-else", DefaultCourier: sessionmove.CourierSSH,
			SSH: &sessionmove.SSHConfig{Host: "somewhere-else"}}), nil
	}
	if _, err := executeResume(deps, request.HandoffID, ""); err == nil ||
		!strings.Contains(err.Error(), "is not configured") {
		t.Fatalf("unconfigured target = %v", err)
	}
	// A target whose configured courier is unusable is refused.
	deps.LoadConfig = func(string) (sessionmove.Config, error) {
		return cwDepsMoveConfig(sessionmove.TargetConfig{Machine: request.TargetMachine}), nil
	}
	if _, err := executeResume(deps, request.HandoffID, ""); err == nil ||
		!strings.Contains(err.Error(), "--via must be") {
		t.Fatalf("unusable courier = %v", err)
	}
	// A repaired route is saved and then drives the delivery.
	delivered := 0
	deps.LoadConfig = func(string) (sessionmove.Config, error) { return cwDepsMoveConfig(cwDepsSSHTarget()), nil }
	deps.NewDeliverer = func(sessionmove.TargetConfig, sessionmove.Courier, sessioncourier.SynchestraOptions) (sessioncourier.Deliverer, error) {
		return delivererFunc(func(_ context.Context, raw []byte) (sessionreceive.Result, error) {
			delivered++
			return completedMoveTestDelivery(t, request, raw, true), nil
		}), nil
	}
	deps.Acknowledge = func(_ context.Context, options sessioncustody.Options) (sessioncustody.Result, error) {
		return completedMoveTestAcknowledgement(t, options), nil
	}
	out, err := executeResume(deps, request.HandoffID, "")
	if err != nil {
		t.Fatalf("repaired route resume: %v\n%+v", err, out)
	}
	if delivered != 1 || out.Request.HandoffID != request.HandoffID {
		t.Fatalf("delivered=%d output=%+v", delivered, out)
	}
	// The persisted route is now immutable: a conflicting --via is refused.
	if _, err := executeResume(deps, request.HandoffID, "synchestra"); err == nil ||
		!strings.Contains(err.Error(), "cannot change immutable courier route") {
		t.Fatalf("changed courier = %v", err)
	}
}

// TestCwDepsSessionMoveResumeDeliveryFailures covers the two post-checkpoint
// failure shapes: an ambiguous delivery and a custody acknowledgement failure.
func TestCwDepsSessionMoveResumeDeliveryFailures(t *testing.T) {
	store := sessionmove.NewStore(t.TempDir())
	source, request, _, digest := cwDepsMoveFixture(t, store)
	route := sessionmove.Route{HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine,
		Courier: sessionmove.CourierLoopback}
	if _, _, err := store.SaveRoute(route); err != nil {
		t.Fatal(err)
	}
	deps := MoveDependencies{
		ResolveSource: func(string) (session.Record, bool, error) { return source, true, nil },
		Store:         func(string) (sessionmove.Store, error) { return store, nil },
		LocalMachine:  func() (string, error) { return "laptop", nil },
	}
	// A delivery error names the exact resume command.
	deps.LoopbackDeliverer = func(sessionmove.Store) sessioncourier.Deliverer {
		return delivererFunc(func(context.Context, []byte) (sessionreceive.Result, error) {
			return sessionreceive.Result{}, errors.New("loopback refused")
		})
	}
	if _, err := executeResume(deps, request.HandoffID, ""); err == nil ||
		!strings.Contains(err.Error(), "retry the exact route with `wb session move --resume "+request.HandoffID+"`") {
		t.Fatalf("delivery error = %v", err)
	}
	// An acknowledgement failure is reported as a durable checkpoint needing
	// the same resume.
	deps.LoopbackDeliverer = func(sessionmove.Store) sessioncourier.Deliverer {
		return delivererFunc(func(_ context.Context, raw []byte) (sessionreceive.Result, error) {
			return completedMoveTestDelivery(t, request, raw, true), nil
		})
	}
	deps.Acknowledge = func(context.Context, sessioncustody.Options) (sessioncustody.Result, error) {
		return sessioncustody.Result{}, errors.New("custody seal failed")
	}
	if _, err := executeResume(deps, request.HandoffID, ""); err == nil ||
		!strings.Contains(err.Error(), "durably checkpointed but WB could not complete receipt-gated source custody") {
		t.Fatalf("acknowledgement error = %v", err)
	}
	// A nil acknowledger is refused rather than treated as success.
	deps.Acknowledge = nil
	if _, err := executeResume(deps, request.HandoffID, ""); err == nil ||
		!strings.Contains(err.Error(), "acknowledger is unavailable") {
		t.Fatalf("nil acknowledger = %v", err)
	}
}

func TestCwDepsSessionMoveResumeReportsDeliveryWithoutANewDeliverer(t *testing.T) {
	store := sessionmove.NewStore(t.TempDir())
	source, request, _, digest := cwDepsMoveFixture(t, store)
	route := sessionmove.Route{HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine,
		Courier: sessionmove.CourierSSH, SSH: &sessionmove.SSHConfig{Host: "hetzner-vm1"}}
	if _, _, err := store.SaveRoute(route); err != nil {
		t.Fatal(err)
	}
	deps := MoveDependencies{
		ResolveSource: func(string) (session.Record, bool, error) { return source, true, nil },
		Store:         func(string) (sessionmove.Store, error) { return store, nil },
		NewDeliverer: func(sessionmove.TargetConfig, sessionmove.Courier, sessioncourier.SynchestraOptions) (sessioncourier.Deliverer, error) {
			return nil, errors.New("ssh courier unavailable")
		},
	}
	if _, err := executeResume(deps, request.HandoffID, ""); err == nil ||
		!strings.Contains(err.Error(), "ssh courier unavailable") {
		t.Fatalf("deliverer construction error = %v", err)
	}
}

func TestCwDepsSessionMoveSynchestraOptionsLoadsAPersistedDispatch(t *testing.T) {
	store := sessionmove.NewStore(t.TempDir())
	_, request, _, digest := cwDepsMoveFixture(t, store)
	options, err := moveSynchestraOptions(store, request.HandoffID, sessionmove.CourierSSH)
	if err != nil || options.SaveDispatch != nil || options.Dispatch != nil {
		t.Fatalf("non-synchestra options = %+v, %v", options, err)
	}
	// Synchestra with no recorded dispatch still hands the store a saver.
	options, err = moveSynchestraOptions(store, request.HandoffID, sessionmove.CourierSynchestra)
	if err != nil || options.SaveDispatch == nil || options.Dispatch != nil {
		t.Fatalf("synchestra options = %+v, %v", options, err)
	}
	// A recorded dispatch is replayed so a resume never invokes a second time.
	route := sessionmove.Route{HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine,
		Courier: sessionmove.CourierSynchestra, Synchestra: &sessionmove.SynchestraConfig{Runner: "synchestra"}}
	if _, _, err := store.SaveRoute(route); err != nil {
		t.Fatal(err)
	}
	identity := sessionmove.SynchestraDispatch{HandoffID: request.HandoffID, RequestDigest: digest,
		Runner: "synchestra", InvocationID: request.HandoffID,
		Handler: sessionmove.SynchestraSessionAcceptHandler, DispatchID: "dispatch-1"}
	if _, _, err := store.SaveSynchestraDispatch(identity); err != nil {
		t.Fatalf("save dispatch: %v", err)
	}
	options, err = moveSynchestraOptions(store, request.HandoffID, sessionmove.CourierSynchestra)
	if err != nil || options.Dispatch == nil || options.Dispatch.DispatchID != "dispatch-1" {
		t.Fatalf("replayed dispatch = %+v, %v", options, err)
	}
}

func TestCwDepsSelectSessionMoveCourierBranches(t *testing.T) {
	if _, err := selectMoveCourier(sessionmove.TargetConfig{Machine: "m", DefaultCourier: sessionmove.CourierSSH}, ""); err == nil ||
		!strings.Contains(err.Error(), "no ssh courier configured") {
		t.Fatalf("ssh without config = %v", err)
	}
	if _, err := selectMoveCourier(sessionmove.TargetConfig{Machine: "m", DefaultCourier: sessionmove.CourierSynchestra}, ""); err == nil ||
		!strings.Contains(err.Error(), "no synchestra courier configured") {
		t.Fatalf("synchestra without config = %v", err)
	}
	if _, err := selectMoveCourier(sessionmove.TargetConfig{Machine: "m"}, ""); err == nil ||
		!strings.Contains(err.Error(), "--via must be") {
		t.Fatalf("no default courier = %v", err)
	}
	if _, err := selectMoveCourier(sessionmove.TargetConfig{Machine: "m"}, "loopback"); err == nil {
		t.Fatal("loopback is not a remote courier and must be refused here")
	}
	target := cwDepsSSHTarget()
	if courier, err := selectMoveCourier(target, ""); err != nil || courier != sessionmove.CourierSSH {
		t.Fatalf("default courier = %q, %v", courier, err)
	}
	both := target
	both.DefaultCourier = sessionmove.CourierSynchestra
	both.Synchestra = &sessionmove.SynchestraConfig{Runner: "synchestra"}
	if courier, err := selectMoveCourier(both, "  ssh  "); err != nil || courier != sessionmove.CourierSSH {
		t.Fatalf("requested courier overrides the default = %q, %v", courier, err)
	}
}

func TestCwDepsSessionMoveRouteCarriesOnlyTheSelectedCourier(t *testing.T) {
	request := sessionmove.Request{HandoffID: "h", TargetMachine: "m"}
	digest := sessionmove.Digest("digest")
	ssh := sessionmove.SSHConfig{Host: "m"}
	synchestra := sessionmove.SynchestraConfig{Runner: "synchestra"}
	route := moveRoute(request, digest, sessionmove.CourierSSH, sessionmove.TargetConfig{SSH: &ssh, Synchestra: &synchestra})
	if route.SSH == nil || route.Synchestra != nil || route.Courier != sessionmove.CourierSSH {
		t.Fatalf("ssh route = %+v", route)
	}
	route = moveRoute(request, digest, sessionmove.CourierSynchestra, sessionmove.TargetConfig{SSH: &ssh, Synchestra: &synchestra})
	if route.Synchestra == nil || route.SSH != nil || route.Courier != sessionmove.CourierSynchestra {
		t.Fatalf("synchestra route = %+v", route)
	}
	route = moveRoute(request, digest, sessionmove.CourierLoopback, sessionmove.TargetConfig{SSH: &ssh})
	if route.SSH != nil || route.Synchestra != nil {
		t.Fatalf("loopback route must carry no remote transport: %+v", route)
	}
}

func TestCwDepsAcknowledgeSessionMoveRefusalBranches(t *testing.T) {
	store := sessionmove.NewStore(t.TempDir())
	source, request, raw, digest := cwDepsMoveFixture(t, store)
	ctx := context.Background()
	deps := MoveDependencies{}

	// An incomplete delivery carries no durable receipt.
	if _, err := acknowledgeMove("", ctx, deps, store, source, sessionmove.CourierLoopback, request, digest,
		sessionreceive.Result{Phase: sessionmove.PhaseReceived}); err == nil ||
		!strings.Contains(err.Error(), "no durable completion receipt") {
		t.Fatalf("incomplete delivery = %v", err)
	}
	// A courier response for a different request is refused.
	other := request
	other.HandoffID = "handoff-other"
	delivery := completedMoveTestDelivery(t, request, raw, true)
	delivery.Request = other
	if _, err := acknowledgeMove("", ctx, deps, store, source, sessionmove.CourierLoopback, request, digest, delivery); err == nil ||
		!strings.Contains(err.Error(), "does not match the exact delivered request") {
		t.Fatalf("mismatched delivery = %v", err)
	}
	// A receipt that does not validate for the request is refused.
	delivery = completedMoveTestDelivery(t, request, raw, true)
	broken := *delivery.Receipt
	broken.RequestDigest = "not-the-digest"
	delivery.Receipt = &broken
	if _, err := acknowledgeMove("", ctx, deps, store, source, sessionmove.CourierLoopback, request, digest, delivery); err == nil ||
		!strings.Contains(err.Error(), "validate target completion receipt") {
		t.Fatalf("invalid receipt = %v", err)
	}
	// With no acknowledger configured the move is refused rather than sealed.
	if _, err := acknowledgeMove("", ctx, deps, store, source, sessionmove.CourierLoopback, request, digest,
		completedMoveTestDelivery(t, request, raw, true)); err == nil ||
		!strings.Contains(err.Error(), "acknowledger is unavailable") {
		t.Fatalf("nil acknowledger = %v", err)
	}
	// The two resume hints name the exact handoff.
	if got := resumableDeliveryError("h1", errors.New("boom")).Error(); !strings.Contains(got, "wb session move --resume h1") {
		t.Errorf("resumableDeliveryError = %q", got)
	}
	if got := resumablePostCheckpointError("h2", "persist", errors.New("boom")).Error(); !strings.Contains(got, "could not persist") ||
		!strings.Contains(got, "wb session move --resume h2") {
		t.Errorf("resumablePostCheckpointError = %q", got)
	}
}

func TestCwDepsDefaultSessionMoveDependencies(t *testing.T) {
	home := t.TempDir()
	t.Setenv(wbhome.EnvOverride, home)
	deps := DefaultMoveDependencies()
	if deps.DefaultConfigPath == nil || deps.LoadConfig == nil || deps.LocalMachine == nil || deps.ResolveSource == nil ||
		deps.Checkpoint == nil || deps.Store == nil || deps.NewDeliverer == nil || deps.Acknowledge == nil {
		t.Fatal("a default dependency is missing")
	}
	if deps.DefaultConfigPath() == "" {
		t.Error("defaultConfigPath returned nothing")
	}
	// The store lives under the resolved WB home.
	store, err := deps.Store(home)
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if !strings.Contains(store.Root, sessionmove.DirName) {
		t.Errorf("store root = %q", store.Root)
	}
	// Courier construction validates the target's own configuration.
	if _, err := deps.NewDeliverer(sessionmove.TargetConfig{Machine: "m"}, sessionmove.CourierSSH, sessioncourier.SynchestraOptions{}); err == nil ||
		!strings.Contains(err.Error(), "no ssh courier configured") {
		t.Fatalf("ssh without config = %v", err)
	}
	if _, err := deps.NewDeliverer(sessionmove.TargetConfig{Machine: "m"}, sessionmove.CourierSynchestra, sessioncourier.SynchestraOptions{}); err == nil ||
		!strings.Contains(err.Error(), "no synchestra courier configured") {
		t.Fatalf("synchestra without config = %v", err)
	}
	if _, err := deps.NewDeliverer(sessionmove.TargetConfig{Machine: "m"}, sessionmove.Courier("telepathy"), sessioncourier.SynchestraOptions{}); err == nil ||
		!strings.Contains(err.Error(), "not implemented by this WB build") {
		t.Fatalf("unknown courier = %v", err)
	}
	ssh := sessionmove.SSHConfig{Host: "m", WBPath: "/home/ai/go/bin/wb"}
	if deliverer, err := deps.NewDeliverer(sessionmove.TargetConfig{Machine: "m", SSH: &ssh}, sessionmove.CourierSSH, sessioncourier.SynchestraOptions{}); err != nil || deliverer == nil {
		t.Fatalf("ssh deliverer = %v, %v", deliverer, err)
	}
	// An invalid config path is reported, never silently treated as no config.
	if _, err := deps.LoadConfig(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Error("loading an absent config must fail")
	}
	// localMachine reports the configured machine or an error, and never
	// invents one.
	if machine, err := deps.LocalMachine(); err != nil && machine != "" {
		t.Errorf("localMachine returned %q with error %v", machine, err)
	}
	// resolveSource either finds this process's session or reports none; it
	// must not panic with no registered session.
	if _, _, err := deps.ResolveSource(home); err != nil {
		t.Logf("resolveSource reported: %v", err)
	}
}

// cwDepsExecSessionMove builds one session move command with the given
// dependencies and returns its stdout plus the execution error.

func executeResume(deps MoveDependencies, id, via string) (MoveResult, error) {
	return NewMove(deps).Move(context.Background(), MoveRequest{ResumeID: id, Via: via, Input: strings.NewReader("handover body\n")}, nil)
}

func completeMoveTestRequest(request sessionmove.Request) sessionmove.Request {
	if request.WorkLogReference == "" {
		request.WorkLogReference = "worklog:effort/run-1/" + strings.Repeat("1", 64)
	}
	message, nextAction := sessionmove.NormalizeSourceOfferContent("session checkpoint ready", "continue from the handover")
	request.SourceOfferMessage = message
	request.SourceOfferNextAction = nextAction
	request.SourceOfferDigest = sessionmove.DigestSourceOffer(message, nextAction)
	return request
}
func mustEncodeMoveTestRequest(t *testing.T, request sessionmove.Request) []byte {
	t.Helper()
	raw, err := sessionmove.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func completedMoveTestDelivery(t *testing.T, request sessionmove.Request, raw []byte, includeSuccessor bool) sessionreceive.Result {
	t.Helper()
	digest := sessionmove.DigestBytes(raw)
	targetReference, err := sessionmove.ExpectedTargetWorkLogReference(request, digest)
	if err != nil {
		t.Fatal(err)
	}
	runtime := request.RequestedHarness
	if runtime == "" {
		runtime = request.SourceRuntime
	}
	model := ""
	if runtime == request.SourceRuntime {
		model = request.SourceModel
	}
	startedAt := request.CreatedAt.Add(time.Second).UTC()
	receipt := sessionmove.Receipt{
		SchemaVersion: sessionmove.ReceiptSchemaVersion, HandoffID: request.HandoffID, RequestDigest: digest,
		SuccessorWBSessionID: request.SuccessorWBSessionID, PredecessorWBSessionID: request.PredecessorWBSessionID,
		TargetMachine: request.TargetMachine, TmuxName: "wb-session-" + request.SuccessorWBSessionID,
		Runtime: runtime, Model: model, TargetWorkLogReference: targetReference.String(),
		AttemptID: "000001-" + strings.Repeat("a", 32), AttemptIndex: 1, PID: 123,
		PinnedCommit: request.BundleCommit, StartedAt: startedAt,
	}
	result := sessionreceive.Result{Request: request, Digest: digest, Phase: sessionmove.PhaseCompleted, Receipt: &receipt}
	if includeSuccessor {
		result.Successor = &sessionlaunch.Result{
			HandoffID: request.HandoffID, WBSessionID: request.SuccessorWBSessionID,
			PredecessorWBSessionID: request.PredecessorWBSessionID, TargetMachine: request.TargetMachine,
			PID: receipt.PID, AttemptID: receipt.AttemptID, AttemptIndex: receipt.AttemptIndex,
			TmuxName: receipt.TmuxName, Runtime: receipt.Runtime, Model: receipt.Model,
			TargetWorkLogRef: receipt.TargetWorkLogReference, WorktreeDir: "/target/worktree",
			PinnedCommit: request.BundleCommit, StartedAt: startedAt,
		}
	}
	return result
}
func completedMoveTestAcknowledgement(t *testing.T, options sessioncustody.Options) sessioncustody.Result {
	t.Helper()
	route, err := options.Store.LoadRoute(options.Request.HandoffID)
	if err != nil {
		t.Fatal(err)
	}
	receipt := options.Receipt
	address := sessionmove.SuccessorAddress{
		SchemaVersion:        sessionmove.SuccessorAddressSchemaVersion,
		SuccessorWBSessionID: receipt.SuccessorWBSessionID, PredecessorWBSessionID: receipt.PredecessorWBSessionID,
		HandoffID: receipt.HandoffID, RequestDigest: receipt.RequestDigest,
		SourceMachine: options.Request.SourceMachine, TargetMachine: receipt.TargetMachine,
		SourceWorkLogReference: options.Request.WorkLogReference, TargetWorkLogReference: receipt.TargetWorkLogReference,
		TmuxName: receipt.TmuxName, Runtime: receipt.Runtime, Model: receipt.Model, NativeHarnessID: receipt.NativeHarnessID,
		AttemptID: receipt.AttemptID, AttemptIndex: receipt.AttemptIndex, PID: receipt.PID,
		PinnedCommit: receipt.PinnedCommit, StartedAt: receipt.StartedAt, Route: route,
	}
	return sessioncustody.Result{
		Receipt: receipt, Address: address,
		WorkLog: worktrees.ExternalSourceSealResult{
			SourceWorkLogReference: options.Request.WorkLogReference,
			TargetWorkLogReference: receipt.TargetWorkLogReference,
			SealedAt:               receipt.StartedAt.Add(time.Second),
		},
	}
}

type delivererFunc func(context.Context, []byte) (sessionreceive.Result, error)

func (f delivererFunc) Deliver(ctx context.Context, raw []byte) (sessionreceive.Result, error) {
	return f(ctx, raw)
}
