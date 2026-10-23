package integration

import (
	"bytes"
	"github.com/sneat-dev/wb/internal/cli/cmdsession"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/secretscan"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
	"strings"
	"testing"
)

func moveCommand(projectsRoot string, deps sessionrun.MoveDependencies) *cobra.Command {
	if deps.LoadScanner == nil {
		deps.LoadScanner = func() (*secretscan.Scanner, []string, error) {
			empty := ""
			return secretscan.LoadDefault(secretscan.LoadOptions{EnvExtraRulesPath: &empty})
		}
	}
	service := sessionrun.NewMove(deps)
	return cmdsession.NewMove(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: projectsRoot} }}, cmdsession.Dependencies{Move: service.Move})
}

func parkCommand(projectsRoot string, supplied ...sessionrun.ParkDependencies) *cobra.Command {
	deps := sessionrun.DefaultParkDependencies()
	if len(supplied) != 0 {
		deps = supplied[0]
	}
	deps.LoadScanner = func() (*secretscan.Scanner, []string, error) {
		empty := ""
		return secretscan.LoadDefault(secretscan.LoadOptions{EnvExtraRulesPath: &empty})
	}
	return cmdsession.NewPark(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: projectsRoot} }}, cmdsession.Dependencies{Park: sessionrun.NewPark(deps).Park})
}
func resumeCommand(projectsRoot string, deps sessionrun.ResumeDependencies) *cobra.Command {
	if deps.Home == nil {
		deps.Home = wbhome.Root
	}
	return cmdsession.NewResume(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: projectsRoot} }}, cmdsession.Dependencies{Resume: sessionrun.NewResume(deps).Resume})
}

func sendCommand(deps sessionrun.MessageDependencies) *cobra.Command {
	return cmdsession.NewSend(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }}, cmdsession.Dependencies{Send: sessionrun.NewMessage(deps).Send})
}
func recallCommand(deps sessionrun.MessageDependencies) *cobra.Command {
	return cmdsession.NewRecall(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }}, cmdsession.Dependencies{Send: sessionrun.NewMessage(deps).Send})
}
func messageReceiver(deps sessionrun.ReceiveMessageDependencies) *cobra.Command {
	return cmdsession.NewReceiveMessage(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }}, cmdsession.Dependencies{ReceiveMessage: sessionrun.NewReceiveMessage(deps).ReceiveMessage})
}

func registerCommand(root string, deps sessionrun.RegisterDependencies) *cobra.Command {
	return cmdsession.NewRegister(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: root} }}, cmdsession.Dependencies{Register: sessionrun.NewRegister(deps).Register})
}

func executeCommand(t *testing.T, _ string, build func() *cobra.Command, args ...string) (string, string, error) {
	return executeInputCommand(t, "", "", build, args...)
}
func executeInputCommand(t *testing.T, _ string, input string, build func() *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	command := build()
	command.SilenceErrors = true
	command.SilenceUsage = true
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetIn(strings.NewReader(input))
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), errOut.String(), err
}
