package cmdsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
	"github.com/sneat-dev/wb/internal/syncreport"
	"github.com/sneat-dev/wb/internal/syncrun"
	"github.com/sneat-dev/wb/internal/tui"
	"github.com/spf13/cobra"
)

func TestSyncCommandReadsCurrentFlagsAndPreservesAcceptedWorkerValues(t *testing.T) {
	t.Parallel()
	for _, workers := range []int{4, 0, -1} {
		t.Run(fmt.Sprint(workers), func(t *testing.T) {
			t.Parallel()
			flags := shared.Flags{ProjectsRoot: "before"}
			boom := errors.New("exit identity")
			calls := 0
			runtime := shared.Runtime{Flags: func() shared.Flags { return flags }, ExitError: func(code int, message string) error {
				if code != 7 || message != "sync did not complete; see diagnostics above" {
					t.Fatalf("exit=%d %q", code, message)
				}
				return boom
			}}
			var out, diagnostics bytes.Buffer
			cmd := New(runtime, func(ctx context.Context, options syncrun.Options, stdout, stderr io.Writer) int {
				calls++
				if !reflect.DeepEqual(options, syncrun.Options{ProjectsRoot: "after", Filter: "needle", Owners: []string{"local"}, Workers: workers, DryRun: true, Publish: true, PruneArchived: true}) || stdout != &out || stderr != &diagnostics || ctx != context.Background() {
					t.Fatalf("options=%+v streams=%v/%v", options, stdout, stderr)
				}
				return 7
			})
			flags = shared.Flags{ProjectsRoot: "after", Filter: "needle", NonInteractive: true}
			cmd.SetOut(&out)
			cmd.SetErr(&diagnostics)
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			cmd.SetArgs([]string{"--org", "local", "--parallel", fmt.Sprint(workers), "--dry-run", "--publish", "--prune-archived"})
			if err := cmd.Execute(); !errors.Is(err, boom) || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
			if flag := cmd.Flags().Lookup("workers"); !flag.Hidden || flag.Deprecated != "use --parallel instead" {
				t.Fatal(flag)
			}
		})
	}
	cmd := New(syncRuntime(&shared.Flags{NonInteractive: true}), func(context.Context, syncrun.Options, io.Writer, io.Writer) int { return 0 })
	cmd.SetArgs(nil)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

type syncRejectedWriter struct{ err error }

func (w syncRejectedWriter) Write([]byte) (int, error) { return 0, w.err }

func TestSyncReportAdaptersPreserveAnnotationsFailuresAndOutput(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"validate", "publish"} {
		for _, phase := range []string{"load", "validate", "publish", "text", "json", "writer-text", "writer-json", "invalid-repository"} {
			t.Run(verb+"/"+phase, func(t *testing.T) {
				t.Parallel()
				boom := errors.New("operation or writer refused")
				calls := []string{}
				report := syncreport.Report{ID: "batch"}
				deps := ReportOperations{Load: func(path string) (syncreport.Report, error) {
					calls = append(calls, "load")
					if path != "input" {
						t.Fatal(path)
					}
					if phase == "load" {
						return report, boom
					}
					return report, nil
				}, Validate: func(context.Context, syncreport.Report) error {
					calls = append(calls, "validate")
					if phase == "validate" {
						return boom
					}
					return nil
				}, Publish: func(_ context.Context, repo, root string, _ syncreport.Report) (gitrepo.SyncReportPublishResult, error) {
					calls = append(calls, "publish")
					if repo != "acme/workbench" || root != "/private" {
						t.Fatalf("%s %s", repo, root)
					}
					if phase == "publish" {
						return gitrepo.SyncReportPublishResult{}, boom
					}
					return gitrepo.SyncReportPublishResult{CommitSHA: "immutable", Paths: []string{"record.md"}}, nil
				}}
				annotations := map[string]string{}
				flags := shared.Flags{ProjectsRoot: "before"}
				cmd := NewReport(syncRuntime(&flags), deps, func(cmd *cobra.Command, terms string) { annotations[cmd.Name()] = terms })
				flags.ProjectsRoot = "/private"
				if len(annotations) != 3 || !strings.Contains(annotations["sync-report"], "findings") || !strings.Contains(annotations["validate"], "schema") || !strings.Contains(annotations["publish"], "URL") {
					t.Fatal(annotations)
				}
				args := []string{verb, "input"}
				if verb == "publish" {
					repo := "acme/workbench"
					if phase == "invalid-repository" {
						repo = "invalid"
					}
					args = append(args, "--repo", repo)
				}
				if strings.HasSuffix(phase, "json") {
					args = append(args, "--json")
				}
				var out bytes.Buffer
				cmd.SetOut(&out)
				if strings.HasPrefix(phase, "writer-") {
					cmd.SetOut(syncRejectedWriter{boom})
				}
				cmd.SetErr(io.Discard)
				cmd.SilenceErrors = true
				cmd.SilenceUsage = true
				cmd.SetArgs(args)
				err := cmd.Execute()
				expectedFailure := phase == "load" || phase == "validate" || (verb == "publish" && phase == "publish") || strings.HasPrefix(phase, "writer-")
				if expectedFailure {
					if !errors.Is(err, boom) {
						t.Fatalf("err=%v calls=%v", err, calls)
					}
				} else if verb == "publish" && phase == "invalid-repository" {
					if err == nil || len(calls) != 0 {
						t.Fatalf("err=%v calls=%v", err, calls)
					}
				} else {
					if err != nil || !strings.Contains(out.String(), "batch") {
						t.Fatalf("err=%v output=%q", err, out.String())
					}
				}
			})
		}
	}
}

func TestSyncTUIWithoutWorkersCompletesWithFinalModelResults(t *testing.T) {
	t.Parallel()
	for _, workers := range []int{0, -1} {
		t.Run(fmt.Sprint(workers), func(t *testing.T) {
			t.Parallel()
			var diagnostics bytes.Buffer
			results := runSyncTUI(context.Background(), []discover.Repo{{Org: "acme", Name: "nonempty"}}, map[string]int{"acme": 1}, t.TempDir(), workers, true, false, &diagnostics, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignals(), tea.WithEnvironment([]string{"TERM=dumb"}))
			if len(results) != 0 || (workers == 0 && diagnostics.Len() != 0) || (workers < 0 && !strings.Contains(diagnostics.String(), "program experienced a panic")) {
				t.Fatalf("results=%v diagnostics=%q", results, diagnostics.String())
			}
		})
	}
}

func TestSyncTUIReturnsPartialFinalModelAfterJoiningTheCanceledBatch(t *testing.T) {
	t.Parallel()
	var diagnostics bytes.Buffer
	started := 0
	repos := []discover.Repo{{Org: "acme", Name: "first", TransferError: "first refused"}, {Org: "acme", Name: "second", TransferError: "second refused"}}
	results := runSyncTUI(context.Background(), repos, map[string]int{"acme": 2}, t.TempDir(), 1, true, false, &diagnostics, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignals(), tea.WithEnvironment([]string{"TERM=dumb"}), tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
		if _, ok := msg.(tui.RepoStarted); ok {
			started++
			if started == 2 {
				return tea.QuitMsg{}
			}
		}
		return msg
	}))
	if started != 2 || len(results) != 1 || results[0].Repo.Name != "first" || results[0].Err.Error() != "first refused" || diagnostics.Len() != 0 {
		t.Fatalf("started=%d results=%v diagnostics=%q", started, results, diagnostics.String())
	}
}

func TestSyncPresentationUsesTheActualNativeProgramAndSummary(t *testing.T) {
	t.Parallel()
	presentation := Presentation()
	// Native default program initialization may refuse an absent controlling terminal;
	// either refusal or SyncDone still joins the no-worker batch and returns no results.
	var diagnostics, summary bytes.Buffer
	results := presentation.Interactive(context.Background(), nil, nil, syncrun.Options{Workers: 0, DryRun: true}, &diagnostics)
	if len(results) != 0 {
		t.Fatal(results)
	}
	presentation.Summary(&summary, nil, false, false)
	if !strings.Contains(summary.String(), "Final outcomes") {
		t.Fatal(summary.String())
	}
}
