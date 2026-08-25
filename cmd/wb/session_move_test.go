package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestSessionMoveCommandCreatesSourceOfferWithoutDelivering(t *testing.T) {
	source := session.Record{
		PID: 123, WBSessionID: "wbs-source", Machine: "laptop", Runtime: "codex",
		Model: "gpt-5", StartedAt: time.Now().UTC(),
	}
	var captured worktrees.SessionCheckpointOptions
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
		checkpoint: func(_ context.Context, options worktrees.SessionCheckpointOptions) (worktrees.SessionCheckpointResult, error) {
			captured = options
			request := sessionmove.Request{
				HandoffID: "handoff-123", SuccessorWBSessionID: "wbs-successor",
				PredecessorWBSessionID: "wbs-source", TargetMachine: "hetzner-vm1",
				BundleCommit: strings.Repeat("a", 40), HandoverPath: ".wb/handoffs/handoff-123.md",
			}
			return worktrees.SessionCheckpointResult{Request: request, Digest: sessionmove.Digest("sha256:" + strings.Repeat("b", 64))}, nil
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
	if rendered.Phase != "offered" || rendered.Courier != sessionmove.CourierSSH || rendered.SourceActive != true ||
		rendered.Request.HandoffID != "handoff-123" {
		t.Fatalf("output = %#v", rendered)
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
