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
		newDeliverer: func(sessionmove.TargetConfig, sessionmove.Courier) (sessioncourier.Deliverer, error) {
			return delivererFunc(func(_ context.Context, raw []byte) (sessionreceive.Result, error) {
				delivered = append([]byte(nil), raw...)
				request, err := sessionmove.DecodeRequest(raw)
				if err != nil {
					return sessionreceive.Result{}, err
				}
				return sessionreceive.Result{Request: request, Digest: sessionmove.DigestBytes(raw), Phase: sessionmove.PhaseSuccessorStarted,
					Successor: &sessionlaunch.Result{HandoffID: request.HandoffID, WBSessionID: request.SuccessorWBSessionID,
						PredecessorWBSessionID: request.PredecessorWBSessionID, TargetMachine: request.TargetMachine,
						PID: 123, TmuxName: "wb-session-" + request.SuccessorWBSessionID, Runtime: "claude-code",
						WorktreeDir: "/target/worktree", PinnedCommit: request.BundleCommit, StartedAt: time.Now().UTC()}}, nil
			}), nil
		},
		checkpoint: func(_ context.Context, options worktrees.SessionCheckpointOptions) (worktrees.SessionCheckpointResult, error) {
			captured = options
			request := sessionmove.Request{
				SchemaVersion: sessionmove.RequestSchemaVersion, HandoffID: "handoff-123", SuccessorWBSessionID: "wbs-successor",
				PredecessorWBSessionID: "wbs-source", SourceMachine: "laptop", TargetMachine: "hetzner-vm1",
				RepositoryRemote: "/tmp/acme/app.git", Branch: "feature/session", SourceWorkCommit: strings.Repeat("b", 40),
				BundleCommit: strings.Repeat("a", 40), HandoverPath: ".wb/handoffs/handoff-123.md",
				HandoverDigest: sessionmove.DigestBytes([]byte("handover")), SourceRuntime: "codex", SourceModel: "gpt-5",
				RequestedHarness: "claude-code", CreatedAt: time.Now().UTC(),
			}
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
	if rendered.Phase != string(sessionmove.PhaseSuccessorStarted) || rendered.Courier != sessionmove.CourierSSH || rendered.SourceActive != true ||
		rendered.Request.HandoffID != "handoff-123" {
		t.Fatalf("output = %#v", rendered)
	}
	if !bytes.Equal(delivered, mustEncodeMoveTestRequest(t, rendered.Request)) {
		t.Fatal("courier did not receive exact checkpoint bytes")
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
				newDeliverer: func(sessionmove.TargetConfig, sessionmove.Courier) (sessioncourier.Deliverer, error) {
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
	request := sessionmove.Request{SchemaVersion: sessionmove.RequestSchemaVersion, HandoffID: "handoff-resume",
		SuccessorWBSessionID: "wbs-target", PredecessorWBSessionID: source.WBSessionID, SourceMachine: source.Machine,
		TargetMachine: "hetzner-vm1", RepositoryRemote: "/tmp/acme/app.git", Branch: "feature/resume",
		SourceWorkCommit: strings.Repeat("a", 40), BundleCommit: strings.Repeat("b", 40),
		HandoverPath: ".wb/handoffs/handoff-resume.md", HandoverDigest: sessionmove.DigestBytes([]byte("handover")),
		SourceRuntime: "codex", SourceModel: "gpt-5", CreatedAt: time.Now().UTC()}
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
		newDeliverer: func(target sessionmove.TargetConfig, _ sessionmove.Courier) (sessioncourier.Deliverer, error) {
			routedHosts = append(routedHosts, target.SSH.Host)
			return delivererFunc(func(_ context.Context, got []byte) (sessionreceive.Result, error) {
				calls++
				delivered = append(delivered, append([]byte(nil), got...))
				if calls == 1 {
					return sessionreceive.Result{}, errors.New("connection lost after remote start")
				}
				return sessionreceive.Result{Request: request, Digest: digest, Phase: sessionmove.PhaseSuccessorStarted,
					Successor: &sessionlaunch.Result{HandoffID: request.HandoffID, WBSessionID: request.SuccessorWBSessionID,
						PredecessorWBSessionID: request.PredecessorWBSessionID, TargetMachine: request.TargetMachine,
						PID: 77, TmuxName: "wb-session-" + request.SuccessorWBSessionID, Runtime: "codex", Model: "gpt-5",
						WorktreeDir: "/target/worktree", PinnedCommit: request.BundleCommit, StartedAt: time.Now().UTC()}}, nil
			}), nil
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
		newDeliverer: func(sessionmove.TargetConfig, sessionmove.Courier) (sessioncourier.Deliverer, error) {
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
	request := sessionmove.Request{
		SchemaVersion: sessionmove.RequestSchemaVersion, HandoffID: "handoff-route-failure",
		SuccessorWBSessionID: "wbs-successor", PredecessorWBSessionID: "wbs-source",
		SourceMachine: "laptop", TargetMachine: "hetzner-vm1", RepositoryRemote: "/tmp/acme/app.git",
		Branch: "feature/session", SourceWorkCommit: strings.Repeat("a", 40), BundleCommit: strings.Repeat("b", 40),
		HandoverPath: ".wb/handoffs/handoff-route-failure.md", HandoverDigest: sessionmove.DigestBytes([]byte("handover")),
		SourceRuntime: "codex", CreatedAt: time.Now().UTC(),
	}
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
		newDeliverer: func(sessionmove.TargetConfig, sessionmove.Courier) (sessioncourier.Deliverer, error) {
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

func TestSessionMoveRefusesCourierSuccessWithoutSuccessorIdentity(t *testing.T) {
	store := sessionmove.NewStore(t.TempDir())
	request := sessionmove.Request{
		SchemaVersion: sessionmove.RequestSchemaVersion, HandoffID: "handoff-missing-successor",
		SuccessorWBSessionID: "wbs-successor", PredecessorWBSessionID: "wbs-source",
		SourceMachine: "laptop", TargetMachine: "hetzner-vm1", RepositoryRemote: "/tmp/acme/app.git",
		Branch: "feature/session", SourceWorkCommit: strings.Repeat("a", 40), BundleCommit: strings.Repeat("b", 40),
		HandoverPath: ".wb/handoffs/handoff-missing-successor.md", HandoverDigest: sessionmove.DigestBytes([]byte("handover")),
		SourceRuntime: "codex", CreatedAt: time.Now().UTC(),
	}
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
		newDeliverer: func(sessionmove.TargetConfig, sessionmove.Courier) (sessioncourier.Deliverer, error) {
			return delivererFunc(func(context.Context, []byte) (sessionreceive.Result, error) {
				return sessionreceive.Result{Request: request, Digest: digest, Phase: sessionmove.PhaseSuccessorStarted}, nil
			}), nil
		},
	}
	command := newSessionMoveCmdWithDeps(deps)
	command.SetArgs([]string{"--to", "hetzner-vm1", "--handover-file", "-"})
	command.SetIn(strings.NewReader("continue"))
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "no successor identity") ||
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
