package cmdagent

import (
	"context"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
)

func TestDiffRoutesPatchThroughTheRuntime(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	var sawPatch bool
	deps.Diff = func(_ context.Context, request agentrun.DiffRequest) (string, error) {
		sawPatch = request.Patch
		return "diff --git a/a b/a", nil
	}
	code, out, errout := runFake(t, deps, "agent", "diff", "agt-00000000000000000000000000000001", "--patch")
	if code != exitOK || !sawPatch || !strings.Contains(out, "diff --git") {
		t.Fatalf("code = %d patch=%v out=%q stderr=%s", code, sawPatch, out, errout)
	}
	code, out, _ = runFake(t, deps, "agent", "diff", "agt-00000000000000000000000000000001", "--format", "json")
	if code != exitOK || !strings.Contains(out, `"diff"`) {
		t.Fatalf("json code = %d out=%q", code, out)
	}
	deps.Diff = func(context.Context, agentrun.DiffRequest) (string, error) {
		return "", &agents.UnknownAgentError{AgentID: "agt-1", Root: "/root"}
	}
	code, _, errout = runFake(t, deps, "agent", "diff", "agt-00000000000000000000000000000001")
	if code != exitFindings || !strings.Contains(errout, "no dispatched agent run") {
		t.Fatalf("unknown diff code = %d stderr=%s", code, errout)
	}
}
