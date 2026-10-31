package integration

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/cmdsession"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionrun"
)

func TestResumeCLIForwardsExplicitUnstartedLaunchAssertionWithoutLaunching(t *testing.T) {
	digest := sessionmove.Digest("sha256:" + strings.Repeat("a", 64))
	called := false
	command := cmdsession.NewResume(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: "/owned/projects"} }}, cmdsession.Dependencies{Resume: func(_ context.Context, request sessionrun.ResumeRequest) (sessionrun.ResumeResult, error) {
		called = true
		if request.ParkedSessionID != "park-owned" || request.UnstartedLaunchAcknowledgment != digest || !request.Current || request.Launch || request.Target != "" {
			t.Fatalf("CLI changed assertion or route: %#v", request)
		}
		return sessionrun.ResumeResult{ParkedSessionID: "park-owned", Status: "awaiting_context_ack"}, nil
	}})
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"park-owned", "--current", "--ack-unstarted-launch", string(digest), "--format", "json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !called || !strings.Contains(output.String(), "awaiting_context_ack") {
		t.Fatalf("assertion was not forwarded to normal current preparation: %s", output.String())
	}
}
