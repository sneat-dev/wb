package agentrun

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/agents"
	"testing"
	"time"
)

func TestAwaitTransitionsUseFixedCadenceAndLastObservation(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0)
	reads, waits := 0, 0
	a := Awaiter{Load: func(id string) (agents.Record, error) {
		reads++
		if id != "id" {
			t.Fatal(id)
		}
		return agents.Record{AgentID: id}, nil
	}, Render: func(r agents.Record) agents.Result {
		return agents.Result{AgentID: r.AgentID, State: agents.StateRunning}
	}, Now: func() time.Time { return now }, Wait: func(ctx context.Context, d time.Duration) error {
		waits++
		if ctx != t.Context() || d != 250*time.Millisecond {
			t.Fatal(ctx, d)
		}
		now = now.Add(d)
		return nil
	}}
	result, err := a.Await(t.Context(), "id", now.Add(200*time.Millisecond))
	if err != nil || result.AgentID != "id" || result.Terminal || reads != 2 || waits != 1 {
		t.Fatal(result, err, reads, waits)
	}
	a.Render = func(r agents.Record) agents.Result { return agents.Result{AgentID: r.AgentID, Terminal: reads >= 4} }
	result, err = a.Await(t.Context(), "id", time.Time{})
	if err != nil || !result.Terminal || reads != 4 {
		t.Fatal(result, err, reads)
	}
}
func TestAwaitPreservesReadAndContextFailuresAndTerminalPriority(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("load refused")
	a := Awaiter{Load: func(string) (agents.Record, error) { return agents.Record{}, sentinel }}
	if result, err := a.Await(t.Context(), "id", time.Time{}); err != sentinel || result.AgentID != "" {
		t.Fatal(result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	a = Awaiter{Load: func(string) (agents.Record, error) { return agents.Record{AgentID: "id"}, nil }, Render: func(r agents.Record) agents.Result { return agents.Result{AgentID: r.AgentID} }, Wait: func(c context.Context, d time.Duration) error { return c.Err() }}
	if result, err := a.Await(ctx, "id", time.Time{}); !errors.Is(err, context.Canceled) || result.AgentID != "id" {
		t.Fatal(result, err)
	}
	a.Render = func(r agents.Record) agents.Result { return agents.Result{AgentID: r.AgentID, Terminal: true} }
	if result, err := a.Await(ctx, "id", time.Time{}); err != nil || !result.Terminal {
		t.Fatal(result, err)
	}
}
