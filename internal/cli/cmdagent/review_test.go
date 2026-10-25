package cmdagent

import (
	"context"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
)

func TestReviewRefusesAnUnknownRunAsAFinding(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	var request agentrun.ReviewRequest
	deps.Review = func(_ context.Context, r agentrun.ReviewRequest) (agents.ReviewRecord, error) {
		request = r
		return agents.ReviewRecord{Status: agents.ReviewAccepted, Evaluator: r.Evaluator}, nil
	}
	code, out, errout := runFake(t, deps, "agent", "review", "agt-00000000000000000000000000000001", "--status", "accepted", "--note", "looks right")
	if code != exitOK {
		t.Fatalf("code = %d stderr=%s", code, errout)
	}
	if request.Status != agents.ReviewAccepted || request.Note != "looks right" || request.Evaluator == "" {
		t.Fatalf("review request = %#v", request)
	}
	if !strings.Contains(out, "accepted") {
		t.Fatalf("output = %q", out)
	}
	deps.Review = func(context.Context, agentrun.ReviewRequest) (agents.ReviewRecord, error) {
		return agents.ReviewRecord{}, &agents.UnknownAgentError{AgentID: "agt-1", Root: "/root"}
	}
	code, _, errout = runFake(t, deps, "agent", "review", "agt-00000000000000000000000000000001", "--status", "accepted")
	if code != exitFindings || !strings.Contains(errout, "no dispatched agent run") {
		t.Fatalf("unknown review code = %d stderr=%s", code, errout)
	}
}
