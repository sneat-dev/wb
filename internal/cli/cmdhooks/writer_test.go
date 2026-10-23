package cmdhooks

import (
	"errors"
	"io"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/spf13/cobra"
)

type countingWriter struct{ writes int }

func (w *countingWriter) Write(p []byte) (int, error) { w.writes++; return len(p), nil }

// Every published write is an observable boundary: failure must stop rendering
// and preserve the writer error, including after a partial report.
func TestRenderWriterFailuresPreserveIdentity(t *testing.T) {
	t.Parallel()
	rich := cwDepsHooksReportFixture()
	bad := rich
	bad.Findings = []hooks.Finding{{Code: "bad", Message: "repair", Path: "shim"}}
	cases := map[string]func(io.Writer) error{
		"healthy":  func(w io.Writer) error { return printHooksCheck(w, rich) },
		"findings": func(w io.Writer) error { return printHooksCheck(w, bad) },
		"builtins": func(w io.Writer) error { return printHooksCheck(w, hooks.CheckReport{ProfilesAuto: true}) },
		"metrics": func(w io.Writer) error {
			return printHookMetrics(w, hooks.MetricsSummary{RepositoryFilter: "app", Days: []hooks.DailyMetrics{{Date: "today"}}, Blocks: []hooks.BlockMetrics{{ID: "go"}}}, "events")
		},
		"measure": func(w io.Writer) error {
			return printHookProfileDelta(w, hooks.ProfileDelta{Blocks: []hooks.BlockMetrics{{ID: "go"}}, Unmeasured: []string{"untimed"}}, "events")
		},
	}
	for name, render := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			probe := &countingWriter{}
			if err := render(probe); err != nil {
				t.Fatal(err)
			}
			for n := 0; n < probe.writes; n++ {
				sentinel := errors.New("writer unavailable")
				w := &failWriter{remaining: n, err: sentinel}
				if err := render(w); err != sentinel {
					t.Fatalf("write %d: %v", n, err)
				}
			}
		})
	}
}

func TestCommandWriterFailuresPreserveIdentity(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"install", "repair-fleet", "install-fleet-error", "check", "check-json", "check-fleet", "check-fleet-error", "check-fleet-json", "metrics", "measure", "lifecycle-check-json", "lifecycle-resume-json"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			setup := func() (*cobra.Command, []string) {
				f := fakeCommands()
				f.git.Apply = func(hooks.ApplyOptions) (hooks.ApplyResult, error) {
					return hooks.ApplyResult{Actions: []string{"installed"}, Report: hooks.CheckReport{RepoRoot: "repo", MetricsPath: "events"}}, nil
				}
				f.git.Check = func(string, string, string, string) (hooks.CheckReport, error) {
					return cwDepsHooksReportFixture(), nil
				}
				f.git.LocalRepos = func(string, string) ([]discover.Repo, error) {
					return []discover.Repo{{Org: "acme", Name: "app", Path: "repo"}}, nil
				}
				args := []string{}
				switch scenario {
				case "install":
					args = []string{"install"}
				case "repair-fleet":
					args = []string{"repair", "--fleet"}
				case "install-fleet-error":
					args = []string{"install", "--fleet"}
					f.git.Apply = func(hooks.ApplyOptions) (hooks.ApplyResult, error) {
						return hooks.ApplyResult{}, errors.New("apply failed")
					}
				case "check":
					args = []string{"check"}
				case "check-json":
					args = []string{"check", "--json"}
				case "check-fleet":
					args = []string{"check", "--fleet"}
				case "check-fleet-error":
					args = []string{"check", "--fleet"}
					f.git.Check = func(string, string, string, string) (hooks.CheckReport, error) {
						return hooks.CheckReport{}, errors.New("check failed")
					}
				case "check-fleet-json":
					args = []string{"check", "--fleet", "--json"}
				case "metrics", "measure":
					args = []string{scenario}
				case "lifecycle-check-json":
					args = []string{"lifecycle", "check", "--json"}
				case "lifecycle-resume-json":
					args = []string{"lifecycle", "resume", "--json"}
				}
				cmd := New(f.runtime, f.git, f.life, f.agent)
				cmd.SilenceErrors = true
				cmd.SilenceUsage = true
				return cmd, args
			}
			probe := &countingWriter{}
			cmd, args := setup()
			cmd.SetOut(probe)
			cmd.SetErr(probe)
			cmd.SetArgs(args)
			_ = cmd.Execute()
			for n := 0; n < probe.writes; n++ {
				cmd, args := setup()
				sentinel := errors.New("writer failure")
				w := &failWriter{remaining: n, err: sentinel}
				cmd.SetOut(w)
				cmd.SetErr(w)
				cmd.SetArgs(args)
				if err := cmd.Execute(); err != sentinel {
					t.Fatalf("write %d: %v", n, err)
				}
			}
		})
	}
}
