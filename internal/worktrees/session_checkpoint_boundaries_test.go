package worktrees

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
)

func TestSessionCheckpointPropagatesDeclaredIDConstructorFailures(t *testing.T) {
	t.Parallel()
	injected := errors.New("declared ID constructor failed")
	started := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	base := SessionCheckpointOptions{
		Worktree: "/missing/source", TargetMachine: "target",
		SourceSession: session.Record{PID: os.Getpid(), WBSessionID: "wbs-source", Machine: "source", Runtime: "codex", StartedAt: started},
		Handover:      SessionHandover{Summary: "continue"}, Now: started.Add(time.Second),
	}
	for _, tc := range []struct {
		name string
		edit func(*SessionCheckpointOptions, *sessionCheckpointPorts)
	}{
		{"handoff ID", func(_ *SessionCheckpointOptions, ports *sessionCheckpointPorts) {
			ports.newHandoffID = func() (string, error) { return "", injected }
		}},
		{"successor session ID", func(options *SessionCheckpointOptions, ports *sessionCheckpointPorts) {
			options.HandoffID = "handoff-fixed"
			ports.newSessionID = func() (string, error) { return "", injected }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options, ports := base, productionSessionCheckpointPorts()
			tc.edit(&options, &ports)
			result, err := createSessionCheckpointWithPorts(context.Background(), options, ports)
			if !errors.Is(err, injected) || result.Request.HandoffID != "" {
				t.Fatalf("declared constructor failure = (%#v, %v), want empty result and injected error", result, err)
			}
		})
	}
}

func TestSessionCheckpointRejectsInvalidInputsBeforePreflight(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	base := SessionCheckpointOptions{
		Worktree: "/missing/source", TargetMachine: "target",
		SourceSession: session.Record{PID: os.Getpid(), WBSessionID: "wbs-source", Machine: "source", Runtime: "codex", StartedAt: started},
		Handover:      SessionHandover{Summary: "continue"}, Now: started.Add(time.Second),
	}
	cases := []struct {
		name string
		edit func(*SessionCheckpointOptions)
		want string
	}{
		{"empty handover", func(o *SessionCheckpointOptions) { o.Handover = SessionHandover{} }, "handover must not be empty"},
		{"invalid source", func(o *SessionCheckpointOptions) { o.SourceSession.Machine = "" }, "missing machine"},
		{"empty target", func(o *SessionCheckpointOptions) { o.TargetMachine = " \t" }, "target machine is required"},
		{"multiline harness", func(o *SessionCheckpointOptions) { o.RequestedHarness = "codex\nother" }, "single-line"},
		{"multiline model", func(o *SessionCheckpointOptions) { o.RequestedModel = "model\rother" }, "single-line"},
		{"source starts later", func(o *SessionCheckpointOptions) { o.Now = started.Add(-time.Second) }, "cannot precede"},
		{"invalid source path", func(o *SessionCheckpointOptions) { o.Worktree = "/missing/source" }, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options := base
			tc.edit(&options)
			_, err := CreateSessionCheckpoint(context.Background(), options)
			if err == nil || (tc.want != "" && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("checkpoint refusal = %v, want %q", err, tc.want)
			}
		})
	}
}
