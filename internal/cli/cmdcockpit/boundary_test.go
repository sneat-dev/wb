package cmdcockpit

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/cockpitrun"
	"github.com/spf13/cobra"
)

func TestUnsupportedFormatCallsNoOperation(t *testing.T) {
	t.Parallel()
	deps := Dependencies{Local: func(context.Context, cockpitrun.LocalRequest) (cockpitrun.LocalSession, error) {
		t.Fatal("invalid format reached local")
		return cockpitrun.LocalSession{}, nil
	}}
	_, _, err := runCockpit(t, &shared.Flags{}, deps, "--format=yaml")
	if testExitCode(err) != shared.ExitUsage {
		t.Fatalf("err=%v", err)
	}
	deps.Export = func(context.Context, cockpitrun.ExportRequest) cockpitrun.ExportResult {
		t.Fatal("invalid export format reached operation")
		return cockpitrun.ExportResult{}
	}
	_, _, err = runCockpit(t, &shared.Flags{}, deps, "export", "--format=text")
	if testExitCode(err) != shared.ExitUsage {
		t.Fatalf("export refusal=%v", err)
	}
}
func TestJSONWriterRefusalPreservesError(t *testing.T) {
	t.Parallel()
	command := New(testRuntime(&shared.Flags{}), cockpitTestDependencies(t, new([]string), new(int)))
	command.SetArgs([]string{"--json"})
	command.SilenceErrors, command.SilenceUsage = true, true
	command.SetOut(cockpitFailingWriter{})
	if err := command.Execute(); err == nil || err.Error() != "stdout closed" {
		t.Fatalf("err=%v", err)
	}
}
func TestInvocationFlagsAreReadAfterParentParsingAndPerInstance(t *testing.T) {
	t.Parallel()
	for _, root := range []string{"first", "second"} {
		flags := &shared.Flags{}
		var got cockpitrun.LocalRequest
		deps := cockpitTestDependencies(t, new([]string), new(int))
		deps.Local = func(_ context.Context, request cockpitrun.LocalRequest) (cockpitrun.LocalSession, error) {
			got = request
			return cockpitrun.LocalSession{Listen: "127.0.0.1:8766"}, nil
		}
		parent := &cobra.Command{Use: "wb"}
		parent.PersistentFlags().StringVar(&flags.ProjectsRoot, "projects-root", "", "root")
		parent.PersistentFlags().BoolVar(&flags.NonInteractive, "non-interactive", false, "interactive")
		parent.AddCommand(New(testRuntime(flags), deps))
		parent.SetArgs([]string{"--projects-root", root, "--non-interactive", "cockpit"})
		parent.SetOut(&bytes.Buffer{})
		parent.SetErr(&bytes.Buffer{})
		if err := parent.Execute(); err != nil || got.Root != root {
			t.Fatalf("request=%+v err=%v", got, err)
		}
	}
}
func TestExportFakeResultsKeepBytesAndWriterPrecedence(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("private writer")
	for _, failure := range []cockpitrun.ExportFailure{"", "export_failed"} {
		deps := Dependencies{Export: func(_ context.Context, request cockpitrun.ExportRequest) cockpitrun.ExportResult {
			if request.Root != "late" || !request.MetricsOnly {
				t.Fatalf("request=%+v", request)
			}
			return cockpitrun.ExportResult{Body: []byte("exact\n"), Failure: failure}
		}}
		flags := &shared.Flags{}
		command := New(testRuntime(flags), deps)
		flags.ProjectsRoot = "late"
		command.SetArgs([]string{"export", "--metrics-only"})
		command.SilenceUsage, command.SilenceErrors = true, true
		command.SetOut(boundaryWriter{sentinel})
		if err := command.Execute(); err == nil || err.Error() != "wb cockpit export: could not write to stdout" || errors.Is(err, sentinel) {
			t.Fatalf("safe failure=%v", err)
		}
	}
}

type boundaryWriter struct{ err error }

func (w boundaryWriter) Write([]byte) (int, error) { return 0, w.err }

func TestExportRendersOperationBytesDropsAndTypedFindings(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		result            cockpitrun.ExportResult
		stdout, stderr    string
		code              int
		diagnosticFailure bool
	}{
		"successful bytes":            {result: cockpitrun.ExportResult{Body: []byte("exact-operation-bytes\n")}, stdout: "exact-operation-bytes\n"},
		"numeric drops":               {result: cockpitrun.ExportResult{Body: []byte("envelope\n"), Drops: cockpitfleet.ExportDrops{Repositories: 1, Worktrees: 2, PullRequests: 3, Agents: 4}}, stdout: "envelope\n", stderr: "wb cockpit export: left out 10 entries the envelope's rules refuse (repositories 1, worktrees 2, pull requests 3, agents 4)\n"},
		"ignored diagnostic refusal":  {result: cockpitrun.ExportResult{Body: []byte("envelope\n"), Drops: cockpitfleet.ExportDrops{Agents: 1}}, stdout: "envelope\n", diagnosticFailure: true},
		"typed failure excludes body": {result: cockpitrun.ExportResult{Body: []byte("private-secret"), Failure: "export_failed"}, stdout: "{\"schema_version\":1,\"error\":\"export_failed\"}\n", code: shared.ExitFindings},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			deps := Dependencies{Export: func(_ context.Context, request cockpitrun.ExportRequest) cockpitrun.ExportResult {
				calls++
				return test.result
			}}
			command := New(testRuntime(&shared.Flags{}), deps)
			command.SetArgs([]string{"export"})
			command.SilenceUsage, command.SilenceErrors = true, true
			var stdout, stderr bytes.Buffer
			command.SetOut(&stdout)
			command.SetErr(&stderr)
			if test.diagnosticFailure {
				command.SetErr(boundaryWriter{errors.New("diagnostic refusal")})
			}
			err := command.Execute()
			if calls != 1 || testExitCode(err) != test.code || stdout.String() != test.stdout || stderr.String() != test.stderr {
				t.Fatalf("calls=%d err=%v stdout=%q stderr=%q", calls, err, stdout.String(), stderr.String())
			}
			if err != nil && err.Error() != "wb cockpit export: export_failed" {
				t.Fatalf("unexpected finding=%v", err)
			}
		})
	}
}
