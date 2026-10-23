package cmddashboard

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
)

type dashboardContextKey struct{}
type refusedDashboardWriter struct{ err error }

func (w refusedDashboardWriter) Write([]byte) (int, error) { return 0, w.err }

func TestDashboardReadsLiveInvocationAfterNativeLocalLookup(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{ProjectsRoot: "before"}
	ctx := context.WithValue(context.Background(), dashboardContextKey{}, "actual")
	opened := false
	calls := 0
	command := New(dashboardRuntime(&flags), Operations{Open: func(string) error { opened = true; return nil }, LocalURL: func(got context.Context, root string) (string, string, error) {
		calls++
		if got != ctx || root != "current" {
			t.Fatalf("context=%v root=%q", got, root)
		}
		flags.NonInteractive = true
		return "http://localhost:8123/old?keep=value", "native provenance warning", nil
	}})
	flags.ProjectsRoot = "current"
	var out, diagnostics bytes.Buffer
	command.SetContext(ctx)
	command.SetOut(&out)
	command.SetErr(&diagnostics)
	command.SetArgs([]string{"--local"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || opened || out.String() != "dashboard: http://localhost:8123/cockpit/?keep=value\n" {
		t.Fatalf("calls=%d opened=%t output=%q", calls, opened, out.String())
	}
	if !strings.HasPrefix(diagnostics.String(), "wb: "+dashboardLocalDeprecation+"\nwb: native provenance warning\n") {
		t.Fatal(diagnostics.String())
	}
}

func TestDashboardFormatFailuresKeepUsageIdentityBeforeAnyNativeEffects(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"--local", "--format", "yaml"}, {"--local", "--format", "yaml", "--json"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			boom := errors.New("coded usage")
			calls := 0
			command := New(shared.Runtime{Flags: func() shared.Flags { t.Fatal("format refusal must precede flags"); return shared.Flags{} }, ExitError: func(code int, message string) error {
				if code != shared.ExitUsage || !strings.Contains(message, "yaml") {
					t.Fatalf("code=%d message=%q", code, message)
				}
				return boom
			}}, Operations{Open: func(string) error { calls++; return nil }, LocalURL: func(context.Context, string) (string, string, error) { calls++; return "", "", nil }})
			command.SilenceUsage = true
			command.SilenceErrors = true
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			command.SetArgs(args)
			if err := command.Execute(); !errors.Is(err, boom) || calls != 0 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestDashboardPreservesNativeRefusalsAndOutputWriterErrors(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"local", "open-hosted", "open-local", "writer-text", "writer-json", "ignored-diagnostics"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			boom := errors.New("native or output refused")
			var out, diagnostics bytes.Buffer
			operations := Operations{Open: func(string) error {
				if strings.HasPrefix(stage, "open-") {
					return boom
				}
				return nil
			}, LocalURL: func(context.Context, string) (string, string, error) {
				if stage == "local" {
					return "", "", boom
				}
				return "http://localhost:8123/", "warning", nil
			}}
			command := New(dashboardRuntime(&shared.Flags{ProjectsRoot: t.TempDir(), NonInteractive: strings.HasPrefix(stage, "writer-")}), operations)
			command.SilenceUsage = true
			command.SilenceErrors = true
			command.SetOut(&out)
			command.SetErr(&diagnostics)
			args := []string{"--local"}
			if stage == "open-hosted" {
				args = nil
			}
			if stage == "writer-json" {
				args = append(args, "--json")
			}
			if strings.HasPrefix(stage, "writer-") {
				command.SetOut(refusedDashboardWriter{boom})
			}
			if stage == "ignored-diagnostics" {
				command.SetErr(refusedDashboardWriter{boom})
			}
			command.SetArgs(args)
			err := command.Execute()
			if stage == "ignored-diagnostics" {
				if err != nil || !strings.Contains(out.String(), "opened dashboard") {
					t.Fatalf("err=%v out=%q", err, out.String())
				}
			} else if !errors.Is(err, boom) {
				t.Fatalf("stage=%s err=%v", stage, err)
			}
			if strings.HasPrefix(stage, "open-") && !strings.Contains(err.Error(), "dashboard") {
				t.Fatal(err)
			}
		})
	}
}
