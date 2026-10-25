package cmdagent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
)

func TestPrunePlansBeforeApplying(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	var requests []agentrun.PruneRequest
	deps.Prune = func(_ context.Context, request agentrun.PruneRequest) (agentrun.PrunePlan, error) {
		requests = append(requests, request)
		return agentrun.PrunePlan{
			OlderThanMS: request.OlderThan.Milliseconds(), Applied: request.Apply,
			Entries: []agentrun.PruneEntry{{AgentID: "agt-1", State: "completed", Action: "remove"}},
		}, nil
	}
	code, out, errout := runFake(t, deps, "agent", "prune", "--older-than", "720h")
	if code != exitOK {
		t.Fatalf("code = %d stderr=%s", code, errout)
	}
	if len(requests) != 1 || requests[0].Apply || requests[0].OlderThan != 720*time.Hour {
		t.Fatalf("requests = %#v", requests)
	}
	if !strings.Contains(out, "older than: 720h0m0s") || !strings.Contains(out, "remove") {
		t.Fatalf("output = %q", out)
	}
	code, _, _ = runFake(t, deps, "agent", "prune", "--older-than", "720h", "--apply")
	if code != exitOK || !requests[1].Apply {
		t.Fatalf("apply was not passed through: %#v", requests)
	}
	code, out, _ = runFake(t, deps, "agent", "prune", "--older-than", "720h", "--format", "json")
	if code != exitOK || !strings.Contains(out, `"older_than_ms"`) {
		t.Fatalf("json code = %d out=%q", code, out)
	}
}
func TestPruneRequiresABound(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	deps.Prune = func(_ context.Context, request agentrun.PruneRequest) (agentrun.PrunePlan, error) {
		if request.OlderThan <= 0 {
			return agentrun.PrunePlan{}, agents.NewRequestError("--older-than must be positive")
		}
		return agentrun.PrunePlan{}, nil
	}
	code, _, errout := runFake(t, deps, "agent", "prune")
	if code != exitUsage || !strings.Contains(errout, "--older-than") {
		t.Fatalf("code = %d stderr=%s", code, errout)
	}
}
