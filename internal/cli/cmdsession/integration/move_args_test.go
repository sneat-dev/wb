package integration

import (
	"github.com/sneat-dev/wb/internal/cli/cmdsession"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"strings"
	"testing"
)

func TestCwDepsSessionMoveResumeRefusesExtraFlagsAndArguments(t *testing.T) {
	deps := sessionrun.MoveDependencies{
		ResolveSource: func(string) (session.Record, bool, error) { return session.Record{}, false, nil },
	}
	command := cmdsession.NewMove(shared.Runtime{}, cmdsession.Dependencies{Move: sessionrun.NewMove(deps).Move})
	command.SilenceUsage, command.SilenceErrors = true, true
	command.SetArgs([]string{"--resume", "handoff-cwdeps", "--summary", "not allowed"})
	if err := command.Execute(); err == nil ||
		!strings.Contains(err.Error(), "--resume accepts only an existing handoff ID") {
		t.Fatalf("resume with --summary = %v", err)
	}
	command = cmdsession.NewMove(shared.Runtime{}, cmdsession.Dependencies{Move: sessionrun.NewMove(deps).Move})
	command.SilenceUsage, command.SilenceErrors = true, true
	command.SetArgs([]string{"--resume", "handoff-cwdeps", "extra-argument"})
	if err := command.Execute(); err == nil ||
		!strings.Contains(err.Error(), "--resume accepts only an existing handoff ID") {
		t.Fatalf("resume with an argument = %v", err)
	}
}
