package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessioncourier"
	"github.com/sneat-dev/wb/internal/sessioncustody"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionreceive"
	"github.com/sneat-dev/wb/internal/wbhome"
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

func TestCwDepsSessionMoveResumeRefusesExtraFlagsAndArguments(t *testing.T) {
	deps := sessionMoveDependencies{
		resolveSource: func() (session.Record, bool, error) { return session.Record{}, false, nil },
	}
	command := newSessionMoveCmdWithDeps(deps)
	command.SilenceUsage, command.SilenceErrors = true, true
	command.SetArgs([]string{"--resume", "handoff-cwdeps", "--summary", "not allowed"})
	if err := command.Execute(); err == nil ||
		!strings.Contains(err.Error(), "--resume accepts only an existing handoff ID") {
		t.Fatalf("resume with --summary = %v", err)
	}
	command = newSessionMoveCmdWithDeps(deps)
	command.SilenceUsage, command.SilenceErrors = true, true
	command.SetArgs([]string{"--resume", "handoff-cwdeps", "extra-argument"})
	if err := command.Execute(); err == nil ||
		!strings.Contains(err.Error(), "--resume accepts only an existing handoff ID") {
		t.Fatalf("resume with an argument = %v", err)
	}
}

// TestCwDepsSessionMoveResumeRefusalBranches walks the pre-delivery guards of
// runSessionMoveResume one at a time.
func TestCwDepsSessionMoveResumeRefusalBranches(t *testing.T) {
	base := func(store sessionmove.Store, source session.Record, ok bool) sessionMoveDependencies {
		return sessionMoveDependencies{
			defaultConfigPath: func() string { return "/tmp/cw-deps-wb.yaml" },
			resolveSource:     func() (session.Record, bool, error) { return source, ok, nil },
			store:             func(string) (sessionmove.Store, error) { return store, nil },
		}
	}
	// A source-resolution failure is surfaced as-is.
	deps := base(sessionmove.NewStore(t.TempDir()), session.Record{}, false)
	deps.resolveSource = func() (session.Record, bool, error) {
		return session.Record{}, false, errors.New("session dir unreadable")
	}
	if _, err := cwDepsExecSessionMove(deps, "--resume", "handoff-cwdeps"); err == nil ||
		!strings.Contains(err.Error(), "session dir unreadable") {
		t.Fatalf("resolveSource error = %v", err)
	}
	// No live predecessor session.
	deps = base(sessionmove.NewStore(t.TempDir()), session.Record{}, false)
	if _, err := cwDepsExecSessionMove(deps, "--resume", "handoff-cwdeps"); err == nil ||
		!strings.Contains(err.Error(), "requires the live registered predecessor session") {
		t.Fatalf("no live session = %v", err)
	}
	source := session.Record{PID: 11, WBSessionID: "wbs-source", Machine: "laptop", Runtime: "codex"}
	// A store that cannot be opened.
	deps = base(sessionmove.NewStore(t.TempDir()), source, true)
	deps.store = func(string) (sessionmove.Store, error) { return sessionmove.Store{}, errors.New("store unavailable") }
	if _, err := cwDepsExecSessionMove(deps, "--resume", "handoff-cwdeps"); err == nil ||
		!strings.Contains(err.Error(), "store unavailable") {
		t.Fatalf("store error = %v", err)
	}
	// An unknown handoff ID names no request.
	if _, err := cwDepsExecSessionMove(base(sessionmove.NewStore(t.TempDir()), source, true), "--resume", "handoff-cwdeps"); err == nil {
		t.Fatal("an unknown handoff must be refused")
	}
	// A handoff that belongs to another predecessor is refused.
	otherStore := sessionmove.NewStore(t.TempDir())
	_, request, _, _ := cwDepsMoveFixture(t, otherStore)
	mismatched := source
	mismatched.WBSessionID = "wbs-someone-else"
	if _, err := cwDepsExecSessionMove(base(otherStore, mismatched, true), "--resume", request.HandoffID); err == nil ||
		!strings.Contains(err.Error(), "belongs to predecessor session") {
		t.Fatalf("predecessor mismatch = %v", err)
	}
}

// TestCwDepsSessionMoveResumeRepairsARouteOrRefuses covers the branch where an
// accepted request has no durable route yet.
func TestCwDepsSessionMoveResumeRepairsARouteOrRefuses(t *testing.T) {
	store := sessionmove.NewStore(t.TempDir())
	source, request, _, _ := cwDepsMoveFixture(t, store)
	deps := sessionMoveDependencies{
		defaultConfigPath: func() string { return "/tmp/cw-deps-wb.yaml" },
		resolveSource:     func() (session.Record, bool, error) { return source, true, nil },
		store:             func(string) (sessionmove.Store, error) { return store, nil },
		loadConfig:        func(string) (sessionmove.Config, error) { return sessionmove.Config{}, errors.New("config unreadable") },
	}
	// A config that cannot be read is surfaced.
	if _, err := cwDepsExecSessionMove(deps, "--resume", request.HandoffID); err == nil ||
		!strings.Contains(err.Error(), "config unreadable") {
		t.Fatalf("unreadable config = %v", err)
	}
	// A config without the requested target is a named refusal.
	deps.loadConfig = func(string) (sessionmove.Config, error) {
		return cwDepsMoveConfig(sessionmove.TargetConfig{Machine: "somewhere-else", DefaultCourier: sessionmove.CourierSSH,
			SSH: &sessionmove.SSHConfig{Host: "somewhere-else"}}), nil
	}
	if _, err := cwDepsExecSessionMove(deps, "--resume", request.HandoffID); err == nil ||
		!strings.Contains(err.Error(), "is not configured") {
		t.Fatalf("unconfigured target = %v", err)
	}
	// A target whose configured courier is unusable is refused.
	deps.loadConfig = func(string) (sessionmove.Config, error) {
		return cwDepsMoveConfig(sessionmove.TargetConfig{Machine: request.TargetMachine}), nil
	}
	if _, err := cwDepsExecSessionMove(deps, "--resume", request.HandoffID); err == nil ||
		!strings.Contains(err.Error(), "--via must be") {
		t.Fatalf("unusable courier = %v", err)
	}
	// A repaired route is saved and then drives the delivery.
	delivered := 0
	deps.loadConfig = func(string) (sessionmove.Config, error) { return cwDepsMoveConfig(cwDepsSSHTarget()), nil }
	deps.newDeliverer = func(sessionmove.TargetConfig, sessionmove.Courier, sessioncourier.SynchestraOptions) (sessioncourier.Deliverer, error) {
		return delivererFunc(func(_ context.Context, raw []byte) (sessionreceive.Result, error) {
			delivered++
			return completedMoveTestDelivery(t, request, raw, true), nil
		}), nil
	}
	deps.acknowledge = func(_ context.Context, options sessioncustody.Options) (sessioncustody.Result, error) {
		return completedMoveTestAcknowledgement(t, options), nil
	}
	out, err := cwDepsExecSessionMove(deps, "--resume", request.HandoffID, "--format", "json")
	if err != nil {
		t.Fatalf("repaired route resume: %v\n%s", err, out)
	}
	if delivered != 1 || !strings.Contains(out, request.HandoffID) {
		t.Fatalf("delivered=%d output=%s", delivered, out)
	}
	// The persisted route is now immutable: a conflicting --via is refused.
	if _, err := cwDepsExecSessionMove(deps, "--resume", request.HandoffID, "--via", "synchestra"); err == nil ||
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
	deps := sessionMoveDependencies{
		resolveSource: func() (session.Record, bool, error) { return source, true, nil },
		store:         func(string) (sessionmove.Store, error) { return store, nil },
		localMachine:  func() (string, error) { return "laptop", nil },
	}
	// A delivery error names the exact resume command.
	deps.loopbackDeliverer = func(sessionmove.Store) sessioncourier.Deliverer {
		return delivererFunc(func(context.Context, []byte) (sessionreceive.Result, error) {
			return sessionreceive.Result{}, errors.New("loopback refused")
		})
	}
	if _, err := cwDepsExecSessionMove(deps, "--resume", request.HandoffID); err == nil ||
		!strings.Contains(err.Error(), "retry the exact route with `wb session move --resume "+request.HandoffID+"`") {
		t.Fatalf("delivery error = %v", err)
	}
	// An acknowledgement failure is reported as a durable checkpoint needing
	// the same resume.
	deps.loopbackDeliverer = func(sessionmove.Store) sessioncourier.Deliverer {
		return delivererFunc(func(_ context.Context, raw []byte) (sessionreceive.Result, error) {
			return completedMoveTestDelivery(t, request, raw, true), nil
		})
	}
	deps.acknowledge = func(context.Context, sessioncustody.Options) (sessioncustody.Result, error) {
		return sessioncustody.Result{}, errors.New("custody seal failed")
	}
	if _, err := cwDepsExecSessionMove(deps, "--resume", request.HandoffID); err == nil ||
		!strings.Contains(err.Error(), "durably checkpointed but WB could not complete receipt-gated source custody") {
		t.Fatalf("acknowledgement error = %v", err)
	}
	// A nil acknowledger is refused rather than treated as success.
	deps.acknowledge = nil
	if _, err := cwDepsExecSessionMove(deps, "--resume", request.HandoffID); err == nil ||
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
	deps := sessionMoveDependencies{
		resolveSource: func() (session.Record, bool, error) { return source, true, nil },
		store:         func(string) (sessionmove.Store, error) { return store, nil },
		newDeliverer: func(sessionmove.TargetConfig, sessionmove.Courier, sessioncourier.SynchestraOptions) (sessioncourier.Deliverer, error) {
			return nil, errors.New("ssh courier unavailable")
		},
	}
	if _, err := cwDepsExecSessionMove(deps, "--resume", request.HandoffID); err == nil ||
		!strings.Contains(err.Error(), "ssh courier unavailable") {
		t.Fatalf("deliverer construction error = %v", err)
	}
}

func TestCwDepsSessionMoveSynchestraOptionsLoadsAPersistedDispatch(t *testing.T) {
	store := sessionmove.NewStore(t.TempDir())
	_, request, _, digest := cwDepsMoveFixture(t, store)
	options, err := sessionMoveSynchestraOptions(store, request.HandoffID, sessionmove.CourierSSH)
	if err != nil || options.SaveDispatch != nil || options.Dispatch != nil {
		t.Fatalf("non-synchestra options = %+v, %v", options, err)
	}
	// Synchestra with no recorded dispatch still hands the store a saver.
	options, err = sessionMoveSynchestraOptions(store, request.HandoffID, sessionmove.CourierSynchestra)
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
	options, err = sessionMoveSynchestraOptions(store, request.HandoffID, sessionmove.CourierSynchestra)
	if err != nil || options.Dispatch == nil || options.Dispatch.DispatchID != "dispatch-1" {
		t.Fatalf("replayed dispatch = %+v, %v", options, err)
	}
}

func TestCwDepsSelectSessionMoveCourierBranches(t *testing.T) {
	if _, err := selectSessionMoveCourier(sessionmove.TargetConfig{Machine: "m", DefaultCourier: sessionmove.CourierSSH}, ""); err == nil ||
		!strings.Contains(err.Error(), "no ssh courier configured") {
		t.Fatalf("ssh without config = %v", err)
	}
	if _, err := selectSessionMoveCourier(sessionmove.TargetConfig{Machine: "m", DefaultCourier: sessionmove.CourierSynchestra}, ""); err == nil ||
		!strings.Contains(err.Error(), "no synchestra courier configured") {
		t.Fatalf("synchestra without config = %v", err)
	}
	if _, err := selectSessionMoveCourier(sessionmove.TargetConfig{Machine: "m"}, ""); err == nil ||
		!strings.Contains(err.Error(), "--via must be") {
		t.Fatalf("no default courier = %v", err)
	}
	if _, err := selectSessionMoveCourier(sessionmove.TargetConfig{Machine: "m"}, "loopback"); err == nil {
		t.Fatal("loopback is not a remote courier and must be refused here")
	}
	target := cwDepsSSHTarget()
	if courier, err := selectSessionMoveCourier(target, ""); err != nil || courier != sessionmove.CourierSSH {
		t.Fatalf("default courier = %q, %v", courier, err)
	}
	both := target
	both.DefaultCourier = sessionmove.CourierSynchestra
	both.Synchestra = &sessionmove.SynchestraConfig{Runner: "synchestra"}
	if courier, err := selectSessionMoveCourier(both, "  ssh  "); err != nil || courier != sessionmove.CourierSSH {
		t.Fatalf("requested courier overrides the default = %q, %v", courier, err)
	}
}

func TestCwDepsSessionMoveRouteCarriesOnlyTheSelectedCourier(t *testing.T) {
	request := sessionmove.Request{HandoffID: "h", TargetMachine: "m"}
	digest := sessionmove.Digest("digest")
	ssh := sessionmove.SSHConfig{Host: "m"}
	synchestra := sessionmove.SynchestraConfig{Runner: "synchestra"}
	route := sessionMoveRoute(request, digest, sessionmove.CourierSSH, sessionmove.TargetConfig{SSH: &ssh, Synchestra: &synchestra})
	if route.SSH == nil || route.Synchestra != nil || route.Courier != sessionmove.CourierSSH {
		t.Fatalf("ssh route = %+v", route)
	}
	route = sessionMoveRoute(request, digest, sessionmove.CourierSynchestra, sessionmove.TargetConfig{SSH: &ssh, Synchestra: &synchestra})
	if route.Synchestra == nil || route.SSH != nil || route.Courier != sessionmove.CourierSynchestra {
		t.Fatalf("synchestra route = %+v", route)
	}
	route = sessionMoveRoute(request, digest, sessionmove.CourierLoopback, sessionmove.TargetConfig{SSH: &ssh})
	if route.SSH != nil || route.Synchestra != nil {
		t.Fatalf("loopback route must carry no remote transport: %+v", route)
	}
}

func TestCwDepsReadSessionHandoverBranches(t *testing.T) {
	dir := t.TempDir()
	if _, err := readSessionHandover(cwDepsNewOutCommand(&bytes.Buffer{}), ""); err == nil ||
		!strings.Contains(err.Error(), "--handover-file is required") {
		t.Fatalf("empty path = %v", err)
	}
	if _, err := readSessionHandover(cwDepsNewOutCommand(&bytes.Buffer{}), filepath.Join(dir, "absent.md")); err == nil ||
		!strings.Contains(err.Error(), "open handover file") {
		t.Fatalf("missing file = %v", err)
	}
	if _, err := readSessionHandover(cwDepsNewOutCommand(&bytes.Buffer{}), dir); err == nil ||
		!strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("directory = %v", err)
	}
	oversized := filepath.Join(dir, "huge.md")
	if err := os.WriteFile(oversized, bytes.Repeat([]byte("x"), maxSessionHandoverBytes+2), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSessionHandover(cwDepsNewOutCommand(&bytes.Buffer{}), oversized); err == nil ||
		!strings.Contains(err.Error(), "handover exceeds") {
		t.Fatalf("oversized file = %v", err)
	}
	blank := filepath.Join(dir, "blank.md")
	if err := os.WriteFile(blank, []byte("   \n\t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSessionHandover(cwDepsNewOutCommand(&bytes.Buffer{}), blank); err == nil ||
		!strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("blank file = %v", err)
	}
	valid := filepath.Join(dir, "valid.md")
	if err := os.WriteFile(valid, []byte("carry on\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, err := readSessionHandover(cwDepsNewOutCommand(&bytes.Buffer{}), valid)
	if err != nil || string(body) != "carry on\n" {
		t.Fatalf("valid file = %q, %v", body, err)
	}
	// "-" reads the command's stdin so a handover never has to touch disk.
	command := cwDepsNewOutCommand(&bytes.Buffer{})
	command.SetIn(strings.NewReader("from stdin\n"))
	body, err = readSessionHandover(command, "-")
	if err != nil || string(body) != "from stdin\n" {
		t.Fatalf("stdin handover = %q, %v", body, err)
	}
}

func TestCwDepsAcknowledgeSessionMoveRefusalBranches(t *testing.T) {
	store := sessionmove.NewStore(t.TempDir())
	source, request, raw, digest := cwDepsMoveFixture(t, store)
	ctx := context.Background()
	deps := sessionMoveDependencies{}

	// An incomplete delivery carries no durable receipt.
	if _, err := acknowledgeSessionMove(ctx, deps, store, source, sessionmove.CourierLoopback, request, digest,
		sessionreceive.Result{Phase: sessionmove.PhaseReceived}); err == nil ||
		!strings.Contains(err.Error(), "no durable completion receipt") {
		t.Fatalf("incomplete delivery = %v", err)
	}
	// A courier response for a different request is refused.
	other := request
	other.HandoffID = "handoff-other"
	delivery := completedMoveTestDelivery(t, request, raw, true)
	delivery.Request = other
	if _, err := acknowledgeSessionMove(ctx, deps, store, source, sessionmove.CourierLoopback, request, digest, delivery); err == nil ||
		!strings.Contains(err.Error(), "does not match the exact delivered request") {
		t.Fatalf("mismatched delivery = %v", err)
	}
	// A receipt that does not validate for the request is refused.
	delivery = completedMoveTestDelivery(t, request, raw, true)
	broken := *delivery.Receipt
	broken.RequestDigest = "not-the-digest"
	delivery.Receipt = &broken
	if _, err := acknowledgeSessionMove(ctx, deps, store, source, sessionmove.CourierLoopback, request, digest, delivery); err == nil ||
		!strings.Contains(err.Error(), "validate target completion receipt") {
		t.Fatalf("invalid receipt = %v", err)
	}
	// With no acknowledger configured the move is refused rather than sealed.
	if _, err := acknowledgeSessionMove(ctx, deps, store, source, sessionmove.CourierLoopback, request, digest,
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
	deps := defaultSessionMoveDependencies()
	if deps.defaultConfigPath == nil || deps.loadConfig == nil || deps.localMachine == nil || deps.resolveSource == nil ||
		deps.checkpoint == nil || deps.store == nil || deps.newDeliverer == nil || deps.acknowledge == nil {
		t.Fatal("a default dependency is missing")
	}
	if deps.defaultConfigPath() == "" {
		t.Error("defaultConfigPath returned nothing")
	}
	// The store lives under the resolved WB home.
	store, err := deps.store(home)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if !strings.Contains(store.Root, sessionmove.DirName) {
		t.Errorf("store root = %q", store.Root)
	}
	// Courier construction validates the target's own configuration.
	if _, err := deps.newDeliverer(sessionmove.TargetConfig{Machine: "m"}, sessionmove.CourierSSH, sessioncourier.SynchestraOptions{}); err == nil ||
		!strings.Contains(err.Error(), "no ssh courier configured") {
		t.Fatalf("ssh without config = %v", err)
	}
	if _, err := deps.newDeliverer(sessionmove.TargetConfig{Machine: "m"}, sessionmove.CourierSynchestra, sessioncourier.SynchestraOptions{}); err == nil ||
		!strings.Contains(err.Error(), "no synchestra courier configured") {
		t.Fatalf("synchestra without config = %v", err)
	}
	if _, err := deps.newDeliverer(sessionmove.TargetConfig{Machine: "m"}, sessionmove.Courier("telepathy"), sessioncourier.SynchestraOptions{}); err == nil ||
		!strings.Contains(err.Error(), "not implemented by this WB build") {
		t.Fatalf("unknown courier = %v", err)
	}
	ssh := sessionmove.SSHConfig{Host: "m", WBPath: "/home/ai/go/bin/wb"}
	if deliverer, err := deps.newDeliverer(sessionmove.TargetConfig{Machine: "m", SSH: &ssh}, sessionmove.CourierSSH, sessioncourier.SynchestraOptions{}); err != nil || deliverer == nil {
		t.Fatalf("ssh deliverer = %v, %v", deliverer, err)
	}
	// An invalid config path is reported, never silently treated as no config.
	if _, err := deps.loadConfig(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Error("loading an absent config must fail")
	}
	// localMachine reports the configured machine or an error, and never
	// invents one.
	if machine, err := deps.localMachine(); err != nil && machine != "" {
		t.Errorf("localMachine returned %q with error %v", machine, err)
	}
	// resolveSource either finds this process's session or reports none; it
	// must not panic with no registered session.
	if _, _, err := deps.resolveSource(); err != nil {
		t.Logf("resolveSource reported: %v", err)
	}
}

// cwDepsExecSessionMove builds one session move command with the given
// dependencies and returns its stdout plus the execution error.
func cwDepsExecSessionMove(deps sessionMoveDependencies, args ...string) (string, error) {
	command := newSessionMoveCmdWithDeps(deps)
	command.SilenceUsage, command.SilenceErrors = true, true
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetIn(strings.NewReader("handover body\n"))
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), err
}
