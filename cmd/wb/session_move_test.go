package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessioncourier"
	"github.com/sneat-dev/wb/internal/sessioncustody"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionreceive"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type delivererFunc func(context.Context, []byte) (sessionreceive.Result, error)

func (f delivererFunc) Deliver(ctx context.Context, raw []byte) (sessionreceive.Result, error) {
	return f(ctx, raw)
}

func TestSessionMoveCommandCheckpointsThenDeliversThroughSSH(t *testing.T) {
	source := session.Record{
		PID: 123, WBSessionID: "wbs-source", Machine: "laptop", Runtime: "codex",
		Model: "gpt-5", StartedAt: time.Now().UTC(),
	}
	var captured worktrees.SessionCheckpointOptions
	store := sessionmove.NewStore(t.TempDir())
	var delivered []byte
	acknowledgements := 0
	deps := sessionMoveDependencies{
		defaultConfigPath: func() string { return "/unused/default.yaml" },
		loadConfig: func(path string) (sessionmove.Config, error) {
			if path != "/tmp/wb.yaml" {
				t.Fatalf("config path = %q", path)
			}
			return sessionmove.Config{Targets: map[string]sessionmove.TargetConfig{
				"hetzner-vm1": {
					Machine: "hetzner-vm1", DefaultCourier: sessionmove.CourierSSH,
					SSH: &sessionmove.SSHConfig{Host: "hetzner-vm1"},
				},
			}}, nil
		},
		resolveSource: func() (session.Record, bool, error) { return source, true, nil },
		store:         func(string) (sessionmove.Store, error) { return store, nil },
		newDeliverer: func(sessionmove.TargetConfig, sessionmove.Courier, sessioncourier.SynchestraOptions) (sessioncourier.Deliverer, error) {
			return delivererFunc(func(_ context.Context, raw []byte) (sessionreceive.Result, error) {
				delivered = append([]byte(nil), raw...)
				request, err := sessionmove.DecodeRequest(raw)
				if err != nil {
					return sessionreceive.Result{}, err
				}
				return completedMoveTestDelivery(t, request, raw, true), nil
			}), nil
		},
		checkpoint: func(_ context.Context, options worktrees.SessionCheckpointOptions) (worktrees.SessionCheckpointResult, error) {
			captured = options
			request := completeMoveTestRequest(sessionmove.Request{
				SchemaVersion: sessionmove.RequestSchemaVersion, HandoffID: "handoff-123", SuccessorWBSessionID: "wbs-successor",
				PredecessorWBSessionID: "wbs-source", SourceMachine: "laptop", TargetMachine: "hetzner-vm1",
				RepositoryRemote: "/tmp/acme/app.git", Branch: "feature/session", SourceWorkCommit: strings.Repeat("b", 40),
				BundleCommit: strings.Repeat("a", 40), HandoverPath: ".wb/handoffs/handoff-123.md",
				HandoverDigest: sessionmove.DigestBytes([]byte("handover")), SourceRuntime: "codex", SourceModel: "gpt-5",
				RequestedHarness: "claude-code", CreatedAt: time.Now().UTC(),
			})
			raw, err := sessionmove.EncodeRequest(request)
			if err != nil {
				return worktrees.SessionCheckpointResult{}, err
			}
			digest := sessionmove.DigestBytes(raw)
			if _, err := store.Admit(raw, digest); err != nil {
				return worktrees.SessionCheckpointResult{}, err
			}
			return worktrees.SessionCheckpointResult{Request: request, Digest: digest, RequestBytes: raw}, nil
		},
		acknowledge: func(_ context.Context, options sessioncustody.Options) (sessioncustody.Result, error) {
			acknowledgements++
			if options.SourceSession != source {
				t.Fatalf("acknowledgement source = %#v", options.SourceSession)
			}
			return completedMoveTestAcknowledgement(t, options), nil
		},
	}

	command := newSessionMoveCmdWithDeps(deps)
	command.SetArgs([]string{
		"--to", "hetzner-vm1", "--via", "ssh", "--config", "/tmp/wb.yaml",
		"--handover-file", "-", "--summary", "source summary",
		"--validation", "go test ./...", "--remaining", "receive on target",
		"--harness", "claude-code", "--format", "json", "/repo/worktree",
	})
	command.SetIn(strings.NewReader("agent-authored continuation\n"))
	var output bytes.Buffer
	command.SetOut(&output)
	if err := command.Execute(); err != nil {
		t.Fatalf("session move: %v", err)
	}
	if captured.ProjectsRoot != projectsRoot || captured.Worktree != "/repo/worktree" || captured.SourceSession.WBSessionID != "wbs-source" ||
		captured.TargetMachine != "hetzner-vm1" || captured.RequestedHarness != "claude-code" ||
		captured.Handover.Summary != "source summary" || captured.Handover.ValidationEvidence != "go test ./..." ||
		captured.Handover.RemainingWork != "receive on target" || string(captured.Handover.Body) != "agent-authored continuation\n" {
		t.Fatalf("checkpoint options = %#v", captured)
	}
	var rendered sessionMoveOutput
	if err := json.Unmarshal(output.Bytes(), &rendered); err != nil {
		t.Fatalf("decode output %q: %v", output.String(), err)
	}
	if rendered.Phase != string(sessionmove.PhaseCompleted) || rendered.Courier != sessionmove.CourierSSH || rendered.SourceActive ||
		rendered.Request.HandoffID != "handoff-123" || rendered.Receipt == nil || rendered.Address == nil || acknowledgements != 1 {
		t.Fatalf("output = %#v", rendered)
	}
	if !bytes.Equal(delivered, mustEncodeMoveTestRequest(t, rendered.Request)) {
		t.Fatal("courier did not receive exact checkpoint bytes")
	}
}

func TestSessionMoveCommandUsesSynchestraWithSameReceiptAndLineageContract(t *testing.T) {
	source := session.Record{
		PID: 321, WBSessionID: "wbs-source", Machine: "laptop", Runtime: "codex",
		Model: "gpt-5", StartedAt: time.Now().UTC(),
	}
	store := sessionmove.NewStore(t.TempDir())
	var delivered []byte
	deps := sessionMoveDependencies{
		defaultConfigPath: func() string { return "/tmp/wb.yaml" },
		loadConfig: func(string) (sessionmove.Config, error) {
			return sessionmove.Config{Targets: map[string]sessionmove.TargetConfig{
				"hetzner-vm1": {
					Machine: "hetzner-vm1", DefaultCourier: sessionmove.CourierSynchestra,
					Synchestra: &sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
				},
			}}, nil
		},
		resolveSource: func() (session.Record, bool, error) { return source, true, nil },
		store:         func(string) (sessionmove.Store, error) { return store, nil },
		checkpoint: func(_ context.Context, _ worktrees.SessionCheckpointOptions) (worktrees.SessionCheckpointResult, error) {
			request := completeMoveTestRequest(sessionmove.Request{
				SchemaVersion: sessionmove.RequestSchemaVersion, HandoffID: "handoff-synchestra",
				SuccessorWBSessionID: "wbs-successor", PredecessorWBSessionID: source.WBSessionID,
				SourceMachine: source.Machine, TargetMachine: "hetzner-vm1", RepositoryRemote: "/tmp/acme/app.git",
				Branch: "feature/session", SourceWorkCommit: strings.Repeat("b", 40), BundleCommit: strings.Repeat("a", 40),
				HandoverPath: ".wb/handoffs/handoff-synchestra.md", HandoverDigest: sessionmove.DigestBytes([]byte("handover")),
				SourceRuntime: source.Runtime, SourceModel: source.Model, CreatedAt: time.Now().UTC(),
			})
			raw := mustEncodeMoveTestRequest(t, request)
			digest := sessionmove.DigestBytes(raw)
			if _, err := store.Admit(raw, digest); err != nil {
				return worktrees.SessionCheckpointResult{}, err
			}
			return worktrees.SessionCheckpointResult{Request: request, Digest: digest, RequestBytes: raw}, nil
		},
		newDeliverer: func(target sessionmove.TargetConfig, courier sessionmove.Courier, options sessioncourier.SynchestraOptions) (sessioncourier.Deliverer, error) {
			if courier != sessionmove.CourierSynchestra || target.Synchestra == nil || target.Synchestra.Runner != "hetzner-vm1" || options.Dispatch != nil || options.SaveDispatch == nil {
				t.Fatalf("Synchestra factory inputs: target=%#v courier=%q options=%#v", target, courier, options)
			}
			return delivererFunc(func(_ context.Context, raw []byte) (sessionreceive.Result, error) {
				delivered = append([]byte(nil), raw...)
				request, err := sessionmove.DecodeRequest(raw)
				if err != nil {
					return sessionreceive.Result{}, err
				}
				if err := options.SaveDispatch(sessionmove.SynchestraDispatch{
					HandoffID: request.HandoffID, RequestDigest: sessionmove.DigestBytes(raw), Runner: target.Synchestra.Runner,
					InvocationID: request.HandoffID, Handler: sessionmove.SynchestraSessionAcceptHandler, DispatchID: "dsp_synchestra",
				}); err != nil {
					return sessionreceive.Result{}, err
				}
				return completedMoveTestDelivery(t, request, raw, true), nil
			}), nil
		},
		acknowledge: func(_ context.Context, options sessioncustody.Options) (sessioncustody.Result, error) {
			return completedMoveTestAcknowledgement(t, options), nil
		},
	}
	command := newSessionMoveCmdWithDeps(deps)
	command.SetArgs([]string{
		"--to", "hetzner-vm1", "--via", "synchestra", "--handover-file", "-", "--format", "json",
	})
	command.SetIn(strings.NewReader("continue on the runner\n"))
	var output bytes.Buffer
	command.SetOut(&output)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var rendered sessionMoveOutput
	if err := json.Unmarshal(output.Bytes(), &rendered); err != nil {
		t.Fatal(err)
	}
	if rendered.Courier != sessionmove.CourierSynchestra || rendered.SourceActive || rendered.Receipt == nil || rendered.Address == nil ||
		rendered.Receipt.HandoffID != "handoff-synchestra" || rendered.Receipt.SuccessorWBSessionID != "wbs-successor" ||
		rendered.Address.PredecessorWBSessionID != source.WBSessionID || rendered.Address.Route.Courier != sessionmove.CourierSynchestra ||
		rendered.Address.Route.Synchestra == nil || rendered.Address.Route.Synchestra.Runner != "hetzner-vm1" {
		t.Fatalf("Synchestra move output = %#v", rendered)
	}
	if !bytes.Equal(delivered, mustEncodeMoveTestRequest(t, rendered.Request)) {
		t.Fatal("Synchestra did not receive the exact checkpoint bytes")
	}
	dispatch, err := store.LoadSynchestraDispatch("handoff-synchestra")
	if err != nil || dispatch.InvocationID != "handoff-synchestra" || dispatch.DispatchID != "dsp_synchestra" {
		t.Fatalf("durable Synchestra dispatch = %#v err=%v", dispatch, err)
	}
}

func TestSessionMoveCommandPreflightsSynchestraBeforeCheckpoint(t *testing.T) {
	preflightErr := errors.New("synchestra executable is unavailable")
	checkpointed := false
	deps := sessionMoveDependencies{
		defaultConfigPath: func() string { return "/tmp/wb.yaml" },
		loadConfig: func(string) (sessionmove.Config, error) {
			return sessionmove.Config{Targets: map[string]sessionmove.TargetConfig{
				"hetzner-vm1": {
					Machine: "hetzner-vm1", DefaultCourier: sessionmove.CourierSynchestra,
					Synchestra: &sessionmove.SynchestraConfig{Runner: "hetzner-vm1"},
				},
			}}, nil
		},
		resolveSource: func() (session.Record, bool, error) {
			return session.Record{PID: 321, WBSessionID: "wbs-source", Runtime: "codex"}, true, nil
		},
		newDeliverer: func(target sessionmove.TargetConfig, courier sessionmove.Courier, options sessioncourier.SynchestraOptions) (sessioncourier.Deliverer, error) {
			if courier != sessionmove.CourierSynchestra || target.Synchestra == nil || options.SaveDispatch == nil || options.Dispatch != nil {
				t.Fatalf("Synchestra preflight inputs: target=%#v courier=%q options=%#v", target, courier, options)
			}
			return nil, preflightErr
		},
		checkpoint: func(context.Context, worktrees.SessionCheckpointOptions) (worktrees.SessionCheckpointResult, error) {
			checkpointed = true
			return worktrees.SessionCheckpointResult{}, errors.New("must not checkpoint")
		},
	}
	command := newSessionMoveCmdWithDeps(deps)
	command.SetArgs([]string{"--to", "hetzner-vm1", "--via", "synchestra", "--handover-file", "-"})
	command.SetIn(strings.NewReader("continue on the runner\n"))
	if err := command.Execute(); !errors.Is(err, preflightErr) {
		t.Fatalf("error = %v, want %v", err, preflightErr)
	}
	if checkpointed {
		t.Fatal("Synchestra preflight failure reached checkpoint mutation")
	}
}

func TestSessionMoveCommandRefusesMissingSessionAndEmptyHandoverBeforeCheckpoint(t *testing.T) {
	tests := []struct {
		name     string
		resolve  func() (session.Record, bool, error)
		handover string
		want     string
	}{
		{
			name:     "missing registered session",
			resolve:  func() (session.Record, bool, error) { return session.Record{}, false, nil },
			handover: "continue\n",
			want:     "live registered source session",
		},
		{
			name: "empty handover",
			resolve: func() (session.Record, bool, error) {
				return session.Record{PID: 123, WBSessionID: "wbs-source", Machine: "laptop", Runtime: "codex", StartedAt: time.Now()}, true, nil
			},
			want: "handover must not be empty",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			deps := sessionMoveDependencies{
				defaultConfigPath: func() string { return "/tmp/wb.yaml" },
				loadConfig: func(string) (sessionmove.Config, error) {
					return sessionmove.Config{Targets: map[string]sessionmove.TargetConfig{
						"target": {Machine: "target", DefaultCourier: sessionmove.CourierSSH, SSH: &sessionmove.SSHConfig{Host: "target"}},
					}}, nil
				},
				resolveSource: test.resolve,
				newDeliverer: func(sessionmove.TargetConfig, sessionmove.Courier, sessioncourier.SynchestraOptions) (sessioncourier.Deliverer, error) {
					return delivererFunc(func(context.Context, []byte) (sessionreceive.Result, error) { return sessionreceive.Result{}, nil }), nil
				},
				checkpoint: func(context.Context, worktrees.SessionCheckpointOptions) (worktrees.SessionCheckpointResult, error) {
					called = true
					return worktrees.SessionCheckpointResult{}, errors.New("must not run")
				},
			}
			command := newSessionMoveCmdWithDeps(deps)
			command.SetArgs([]string{"--to", "target", "--handover-file", "-"})
			command.SetIn(strings.NewReader(test.handover))
			if err := command.Execute(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
			if called {
				t.Fatal("checkpoint called after refusal")
			}
		})
	}
}

func TestSessionMoveResumeReusesExactRequestAndImmutableSSHRoute(t *testing.T) {
	store := sessionmove.NewStore(t.TempDir())
	source := session.Record{PID: 11, WBSessionID: "wbs-source", Machine: "laptop", Runtime: "codex", StartedAt: time.Now().UTC()}
	request := completeMoveTestRequest(sessionmove.Request{SchemaVersion: sessionmove.RequestSchemaVersion, HandoffID: "handoff-resume",
		SuccessorWBSessionID: "wbs-target", PredecessorWBSessionID: source.WBSessionID, SourceMachine: source.Machine,
		TargetMachine: "hetzner-vm1", RepositoryRemote: "/tmp/acme/app.git", Branch: "feature/resume",
		SourceWorkCommit: strings.Repeat("a", 40), BundleCommit: strings.Repeat("b", 40),
		HandoverPath: ".wb/handoffs/handoff-resume.md", HandoverDigest: sessionmove.DigestBytes([]byte("handover")),
		SourceRuntime: "codex", SourceModel: "gpt-5", CreatedAt: time.Now().UTC()})
	raw := mustEncodeMoveTestRequest(t, request)
	digest := sessionmove.DigestBytes(raw)
	calls, checkpoints := 0, 0
	var delivered [][]byte
	var routedHosts []string
	deps := sessionMoveDependencies{
		defaultConfigPath: func() string { return "/tmp/wb.yaml" },
		loadConfig: func(string) (sessionmove.Config, error) {
			return sessionmove.Config{Targets: map[string]sessionmove.TargetConfig{"hetzner-vm1": {
				Machine: "hetzner-vm1", DefaultCourier: sessionmove.CourierSSH,
				SSH: &sessionmove.SSHConfig{Host: "hetzner-vm1", WBPath: "/home/ai/go/bin/wb"},
			}}}, nil
		},
		resolveSource: func() (session.Record, bool, error) { return source, true, nil },
		store:         func(string) (sessionmove.Store, error) { return store, nil },
		checkpoint: func(context.Context, worktrees.SessionCheckpointOptions) (worktrees.SessionCheckpointResult, error) {
			checkpoints++
			if _, err := store.Admit(raw, digest); err != nil {
				return worktrees.SessionCheckpointResult{}, err
			}
			return worktrees.SessionCheckpointResult{Request: request, Digest: digest, RequestBytes: raw}, nil
		},
		newDeliverer: func(target sessionmove.TargetConfig, _ sessionmove.Courier, _ sessioncourier.SynchestraOptions) (sessioncourier.Deliverer, error) {
			routedHosts = append(routedHosts, target.SSH.Host)
			return delivererFunc(func(_ context.Context, got []byte) (sessionreceive.Result, error) {
				calls++
				delivered = append(delivered, append([]byte(nil), got...))
				if calls == 1 {
					return sessionreceive.Result{}, errors.New("connection lost after remote start")
				}
				return completedMoveTestDelivery(t, request, got, true), nil
			}), nil
		},
		acknowledge: func(_ context.Context, options sessioncustody.Options) (sessioncustody.Result, error) {
			return completedMoveTestAcknowledgement(t, options), nil
		},
	}
	first := newSessionMoveCmdWithDeps(deps)
	first.SetArgs([]string{"--to", "hetzner-vm1", "--via", "ssh", "--handover-file", "-"})
	first.SetIn(strings.NewReader("continue"))
	if err := first.Execute(); err == nil || !strings.Contains(err.Error(), "--resume handoff-resume") {
		t.Fatalf("first error = %v", err)
	}

	// A changed config must not redirect the accepted handoff. Resume loads the
	// immutable route and does not call loadConfig again.
	deps.loadConfig = func(string) (sessionmove.Config, error) {
		return sessionmove.Config{}, errors.New("changed config must be ignored")
	}
	second := newSessionMoveCmdWithDeps(deps)
	second.SetArgs([]string{"--resume", request.HandoffID, "--via", "ssh", "--format", "json"})
	var output bytes.Buffer
	second.SetOut(&output)
	if err := second.Execute(); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if checkpoints != 1 || calls != 2 || len(routedHosts) != 2 || routedHosts[0] != "hetzner-vm1" || routedHosts[1] != "hetzner-vm1" {
		t.Fatalf("checkpoints=%d calls=%d routes=%v", checkpoints, calls, routedHosts)
	}
	if !bytes.Equal(delivered[0], raw) || !bytes.Equal(delivered[1], raw) {
		t.Fatal("resume changed exact request bytes")
	}
}

func TestSessionMoveResumeRepairsDurableReceiptWithoutRedelivery(t *testing.T) {
	store := sessionmove.NewStore(t.TempDir())
	source := session.Record{PID: 11, WBSessionID: "wbs-source", Machine: "laptop", Runtime: "codex", StartedAt: time.Now().UTC()}
	request := completeMoveTestRequest(sessionmove.Request{
		SchemaVersion: sessionmove.RequestSchemaVersion, HandoffID: "handoff-local-receipt",
		SuccessorWBSessionID: "wbs-target", PredecessorWBSessionID: source.WBSessionID,
		SourceMachine: source.Machine, TargetMachine: "hetzner-vm1", RepositoryRemote: "/tmp/acme/app.git",
		Branch: "feature/resume", SourceWorkCommit: strings.Repeat("a", 40), BundleCommit: strings.Repeat("b", 40),
		HandoverPath: ".wb/handoffs/handoff-local-receipt.md", HandoverDigest: sessionmove.DigestBytes([]byte("handover")),
		SourceRuntime: "codex", CreatedAt: time.Now().UTC(),
	})
	raw := mustEncodeMoveTestRequest(t, request)
	digest := sessionmove.DigestBytes(raw)
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	route := sessionmove.Route{HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine,
		Courier: sessionmove.CourierSSH, SSH: &sessionmove.SSHConfig{Host: "hetzner-vm1"}}
	if _, _, err := store.SaveRoute(route); err != nil {
		t.Fatal(err)
	}
	receipt := *completedMoveTestDelivery(t, request, raw, false).Receipt
	lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveReceiptUnderLock(lock, request.HandoffID, digest, receipt); err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}

	deliveries := 0
	deps := sessionMoveDependencies{
		resolveSource: func() (session.Record, bool, error) { return source, true, nil },
		store:         func(string) (sessionmove.Store, error) { return store, nil },
		newDeliverer: func(sessionmove.TargetConfig, sessionmove.Courier, sessioncourier.SynchestraOptions) (sessioncourier.Deliverer, error) {
			deliveries++
			return nil, errors.New("durable local receipt must skip courier")
		},
		acknowledge: func(_ context.Context, options sessioncustody.Options) (sessioncustody.Result, error) {
			if options.Receipt != receipt {
				t.Fatalf("acknowledged receipt = %#v", options.Receipt)
			}
			return completedMoveTestAcknowledgement(t, options), nil
		},
	}
	command := newSessionMoveCmdWithDeps(deps)
	command.SetArgs([]string{"--resume", request.HandoffID, "--format", "json"})
	var output bytes.Buffer
	command.SetOut(&output)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if deliveries != 0 {
		t.Fatalf("courier deliveries = %d, want 0", deliveries)
	}
	var rendered sessionMoveOutput
	if err := json.Unmarshal(output.Bytes(), &rendered); err != nil {
		t.Fatal(err)
	}
	if rendered.SourceActive || rendered.Phase != string(sessionmove.PhaseCompleted) || rendered.Successor != nil || rendered.Receipt == nil {
		t.Fatalf("resume output = %#v", rendered)
	}
}

func TestSessionMoveRejectsUnsupportedHarnessBeforeCheckpoint(t *testing.T) {
	checkpointed := false
	deps := sessionMoveDependencies{
		defaultConfigPath: func() string { return "/tmp/wb.yaml" },
		loadConfig: func(string) (sessionmove.Config, error) {
			return sessionmove.Config{Targets: map[string]sessionmove.TargetConfig{
				"target": {Machine: "target", DefaultCourier: sessionmove.CourierSSH, SSH: &sessionmove.SSHConfig{Host: "target"}},
			}}, nil
		},
		resolveSource: func() (session.Record, bool, error) {
			return session.Record{PID: 1, WBSessionID: "wbs-source", Runtime: "codex"}, true, nil
		},
		newDeliverer: func(sessionmove.TargetConfig, sessionmove.Courier, sessioncourier.SynchestraOptions) (sessioncourier.Deliverer, error) {
			return delivererFunc(func(context.Context, []byte) (sessionreceive.Result, error) { return sessionreceive.Result{}, nil }), nil
		},
		checkpoint: func(context.Context, worktrees.SessionCheckpointOptions) (worktrees.SessionCheckpointResult, error) {
			checkpointed = true
			return worktrees.SessionCheckpointResult{}, nil
		},
	}
	command := newSessionMoveCmdWithDeps(deps)
	command.SetArgs([]string{"--to", "target", "--harness", "shell", "--handover-file", "-"})
	command.SetIn(strings.NewReader("continue"))
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error = %v", err)
	}
	if checkpointed {
		t.Fatal("unsupported harness reached checkpoint mutation")
	}
}

func TestSessionMoveReportsExactResumeAfterRoutePersistenceFailure(t *testing.T) {
	request := completeMoveTestRequest(sessionmove.Request{
		SchemaVersion: sessionmove.RequestSchemaVersion, HandoffID: "handoff-route-failure",
		SuccessorWBSessionID: "wbs-successor", PredecessorWBSessionID: "wbs-source",
		SourceMachine: "laptop", TargetMachine: "hetzner-vm1", RepositoryRemote: "/tmp/acme/app.git",
		Branch: "feature/session", SourceWorkCommit: strings.Repeat("a", 40), BundleCommit: strings.Repeat("b", 40),
		HandoverPath: ".wb/handoffs/handoff-route-failure.md", HandoverDigest: sessionmove.DigestBytes([]byte("handover")),
		SourceRuntime: "codex", CreatedAt: time.Now().UTC(),
	})
	raw := mustEncodeMoveTestRequest(t, request)
	invalidStoreRoot := t.TempDir() + "/not-a-directory"
	if err := os.WriteFile(invalidStoreRoot, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	delivered := false
	deps := sessionMoveDependencies{
		defaultConfigPath: func() string { return "/tmp/wb.yaml" },
		loadConfig: func(string) (sessionmove.Config, error) {
			return sessionmove.Config{Targets: map[string]sessionmove.TargetConfig{"hetzner-vm1": {
				Machine: "hetzner-vm1", DefaultCourier: sessionmove.CourierSSH,
				SSH: &sessionmove.SSHConfig{Host: "hetzner-vm1"},
			}}}, nil
		},
		resolveSource: func() (session.Record, bool, error) {
			return session.Record{PID: 1, WBSessionID: "wbs-source", Machine: "laptop", Runtime: "codex"}, true, nil
		},
		checkpoint: func(context.Context, worktrees.SessionCheckpointOptions) (worktrees.SessionCheckpointResult, error) {
			return worktrees.SessionCheckpointResult{Request: request, Digest: sessionmove.DigestBytes(raw), RequestBytes: raw}, nil
		},
		store: func(string) (sessionmove.Store, error) { return sessionmove.NewStore(invalidStoreRoot), nil },
		newDeliverer: func(sessionmove.TargetConfig, sessionmove.Courier, sessioncourier.SynchestraOptions) (sessioncourier.Deliverer, error) {
			return delivererFunc(func(context.Context, []byte) (sessionreceive.Result, error) {
				delivered = true
				return sessionreceive.Result{}, nil
			}), nil
		},
	}
	command := newSessionMoveCmdWithDeps(deps)
	command.SetArgs([]string{"--to", "hetzner-vm1", "--handover-file", "-"})
	command.SetIn(strings.NewReader("continue"))
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), request.HandoffID) ||
		!strings.Contains(err.Error(), "wb session move --resume "+request.HandoffID) {
		t.Fatalf("error = %v, want exact resumable handoff guidance", err)
	}
	if delivered {
		t.Fatal("courier ran before immutable route was persisted")
	}
}

func TestSessionMoveReportsExactResumeAfterDurableCheckpointEvidenceFailure(t *testing.T) {
	const handoffID = "handoff-checkpoint-evidence-failure"
	delivered := false
	deps := sessionMoveDependencies{
		defaultConfigPath: func() string { return "/tmp/wb.yaml" },
		loadConfig: func(string) (sessionmove.Config, error) {
			return sessionmove.Config{Targets: map[string]sessionmove.TargetConfig{"hetzner-vm1": {
				Machine: "hetzner-vm1", DefaultCourier: sessionmove.CourierSSH,
				SSH: &sessionmove.SSHConfig{Host: "hetzner-vm1"},
			}}}, nil
		},
		resolveSource: func() (session.Record, bool, error) {
			return session.Record{PID: 1, WBSessionID: "wbs-source", Machine: "laptop", Runtime: "codex"}, true, nil
		},
		checkpoint: func(context.Context, worktrees.SessionCheckpointOptions) (worktrees.SessionCheckpointResult, error) {
			return worktrees.SessionCheckpointResult{Request: sessionmove.Request{HandoffID: handoffID}},
				errors.New("source owner evidence interrupted")
		},
		newDeliverer: func(sessionmove.TargetConfig, sessionmove.Courier, sessioncourier.SynchestraOptions) (sessioncourier.Deliverer, error) {
			return delivererFunc(func(context.Context, []byte) (sessionreceive.Result, error) {
				delivered = true
				return sessionreceive.Result{}, errors.New("must not deliver")
			}), nil
		},
	}
	command := newSessionMoveCmdWithDeps(deps)
	command.SetArgs([]string{"--to", "hetzner-vm1", "--handover-file", "-"})
	command.SetIn(strings.NewReader("continue"))
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "wb session move --resume "+handoffID) ||
		!strings.Contains(err.Error(), "finish source checkpoint evidence") {
		t.Fatalf("error = %v", err)
	}
	if delivered {
		t.Fatal("courier ran after incomplete source checkpoint evidence")
	}
}

func TestSessionMoveRefusesCourierSuccessWithoutCompletionReceipt(t *testing.T) {
	store := sessionmove.NewStore(t.TempDir())
	request := completeMoveTestRequest(sessionmove.Request{
		SchemaVersion: sessionmove.RequestSchemaVersion, HandoffID: "handoff-missing-successor",
		SuccessorWBSessionID: "wbs-successor", PredecessorWBSessionID: "wbs-source",
		SourceMachine: "laptop", TargetMachine: "hetzner-vm1", RepositoryRemote: "/tmp/acme/app.git",
		Branch: "feature/session", SourceWorkCommit: strings.Repeat("a", 40), BundleCommit: strings.Repeat("b", 40),
		HandoverPath: ".wb/handoffs/handoff-missing-successor.md", HandoverDigest: sessionmove.DigestBytes([]byte("handover")),
		SourceRuntime: "codex", CreatedAt: time.Now().UTC(),
	})
	raw := mustEncodeMoveTestRequest(t, request)
	digest := sessionmove.DigestBytes(raw)
	deps := sessionMoveDependencies{
		defaultConfigPath: func() string { return "/tmp/wb.yaml" },
		loadConfig: func(string) (sessionmove.Config, error) {
			return sessionmove.Config{Targets: map[string]sessionmove.TargetConfig{"hetzner-vm1": {
				Machine: "hetzner-vm1", DefaultCourier: sessionmove.CourierSSH,
				SSH: &sessionmove.SSHConfig{Host: "hetzner-vm1"},
			}}}, nil
		},
		resolveSource: func() (session.Record, bool, error) {
			return session.Record{PID: 1, WBSessionID: "wbs-source", Machine: "laptop", Runtime: "codex"}, true, nil
		},
		checkpoint: func(context.Context, worktrees.SessionCheckpointOptions) (worktrees.SessionCheckpointResult, error) {
			if _, err := store.Admit(raw, digest); err != nil {
				return worktrees.SessionCheckpointResult{}, err
			}
			return worktrees.SessionCheckpointResult{Request: request, Digest: digest, RequestBytes: raw}, nil
		},
		store: func(string) (sessionmove.Store, error) { return store, nil },
		newDeliverer: func(sessionmove.TargetConfig, sessionmove.Courier, sessioncourier.SynchestraOptions) (sessioncourier.Deliverer, error) {
			return delivererFunc(func(context.Context, []byte) (sessionreceive.Result, error) {
				return sessionreceive.Result{Request: request, Digest: digest, Phase: sessionmove.PhaseSuccessorStarted}, nil
			}), nil
		},
	}
	command := newSessionMoveCmdWithDeps(deps)
	command.SetArgs([]string{"--to", "hetzner-vm1", "--handover-file", "-"})
	command.SetIn(strings.NewReader("continue"))
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "no durable completion receipt") ||
		!strings.Contains(err.Error(), "--resume "+request.HandoffID) {
		t.Fatalf("error = %v", err)
	}
}

func mustEncodeMoveTestRequest(t *testing.T, request sessionmove.Request) []byte {
	t.Helper()
	raw, err := sessionmove.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	return raw
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
