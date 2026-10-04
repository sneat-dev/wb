package cmddeps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	cliprogress "github.com/sneat-dev/wb/internal/cli/progress"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/depsrun"
	engineprogress "github.com/sneat-dev/wb/internal/progress"
	"github.com/spf13/cobra"
)

type guardedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (w *guardedBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Write(p)
}
func (w *guardedBuffer) String() string { w.mu.Lock(); defer w.mu.Unlock(); return w.buffer.String() }
func commandFor(t *testing.T, name string, runtime shared.Runtime, ops Dependencies) *cobra.Command {
	t.Helper()
	root := New(runtime, ops)
	child, _, err := root.Find([]string{name})
	if err != nil {
		t.Fatal(err)
	}
	root.RemoveCommand(child)
	child.SilenceErrors = true
	child.SilenceUsage = true
	child.SetOut(io.Discard)
	child.SetErr(io.Discard)
	return child
}
func executeArgs(cmd *cobra.Command, args ...string) error { cmd.SetArgs(args); return cmd.Execute() }
func boundaryOps() Dependencies {
	ops := testOperations()
	ops.Select = func(context.Context, depsrun.Selection) ([]deps.Repository, error) {
		return []deps.Repository{{Slug: "acme/app"}}, nil
	}
	ops.Graph = func(_ context.Context, r depsrun.GraphRequest) (depsrun.GraphResult, error) {
		r.Finish("completed")
		return depsrun.GraphResult{Paths: deps.GraphReportPaths{HTML: "report.html"}}, nil
	}
	ops.Drift = func(_ context.Context, r depsrun.DriftRequest) (deps.DriftReport, error) {
		r.Finish("completed")
		return deps.DriftReport{}, nil
	}
	ops.Peers = func(context.Context, deps.PeerOptions) (deps.PeerReport, error) { return deps.PeerReport{}, nil }
	ops.Set = func(_ context.Context, r depsrun.SetRequest) (depsrun.SetResult, error) {
		r.Finish("completed")
		return depsrun.SetResult{}, nil
	}
	ops.Seed = func(context.Context, depsrun.SeedRequest) (depsrun.SeedResult, error) {
		return depsrun.SeedResult{Events: []deps.ReleaseEvent{{Dependency: "acme", Version: "v1"}}}, nil
	}
	ops.Bump = func(context.Context, depsrun.BumpRequest) (depsrun.BumpResult, error) {
		return depsrun.BumpResult{Report: deps.BumpReport{Operation: "bump"}}, nil
	}
	ops.OpenBrowser = func(string) error { return nil }
	return ops
}

func TestSetPropagateGuardsFinishTheActualCampaignOnceAfterSelection(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"local", []string{"go", "acme@v1.0.0", "--propagate"}, "--propagate requires --fleet"},
		{"npm", []string{"npm", "@acme/core@1.0.0", "--fleet", "--propagate"}, "--propagate is supported only for the go ecosystem; it delegates to deps bump"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out guardedBuffer
			ops := boundaryOps()
			var campaign *cliprogress.Campaign
			selected := false
			ops.Campaign = func(writer io.Writer, _ bool, name string) *cliprogress.Campaign {
				if writer != &out {
					t.Fatal("stale campaign writer")
				}
				campaign = cliprogress.NewCampaign(writer, true, name)
				return campaign
			}
			ops.Select = func(_ context.Context, r depsrun.Selection) ([]deps.Repository, error) {
				selected = true
				r.Progress(engineprogress.Event{Phase: "select_repositories", State: engineprogress.Waiting})
				return nil, nil
			}
			ops.Bump = func(context.Context, depsrun.BumpRequest) (depsrun.BumpResult, error) {
				t.Fatal("guard accepted mutation")
				return depsrun.BumpResult{}, nil
			}
			cmd := commandFor(t, "set", testRuntime(), ops)
			cmd.SetErr(&out)
			if err := executeArgs(cmd, test.args...); err == nil || err.Error() != test.want {
				t.Fatalf("guard=%v", err)
			}
			if !selected {
				t.Fatal("guard reordered ahead of selection")
			}
			snapshot := out.String()
			if strings.Count(snapshot, "deps set: failed") != 1 || strings.Contains(snapshot, "completed") {
				t.Fatalf("finish output=%q", snapshot)
			}
			// Finish joins the genuine heartbeat; later reports and a repeated finish must be ignored.
			campaign.Report(engineprogress.Event{Phase: "late", State: engineprogress.Completed})
			campaign.Finish("completed")
			if out.String() != snapshot {
				t.Fatalf("post-return writer access: %q", out.String())
			}
		})
	}
}

func TestRunDepsBumpEnsureRootFailureFinishesCampaignAsFailed(t *testing.T) {
	t.Parallel()
	failure := errors.New("home persistence refused")
	var progress guardedBuffer
	ops := boundaryOps()
	var campaign *cliprogress.Campaign
	ops.Campaign = func(w io.Writer, _ bool, name string) *cliprogress.Campaign {
		campaign = cliprogress.NewCampaign(w, true, name)
		return campaign
	}
	ops.Bump = func(context.Context, depsrun.BumpRequest) (depsrun.BumpResult, error) {
		return depsrun.BumpResult{}, failure
	}
	cmd := commandFor(t, "bump", testRuntime(), ops)
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&progress)
	if err := executeArgs(cmd, "go", "--fleet", "--changed", "acme@v1.0.0"); err != failure {
		t.Fatalf("failure identity=%v", err)
	}
	if stdout.Len() != 0 || !strings.Contains(progress.String(), "deps bump: failed") || strings.Contains(progress.String(), "deps bump: completed") {
		t.Fatalf("streams=%q/%q", stdout.String(), progress.String())
	}
	before := progress.String()
	campaign.Report(engineprogress.Event{Detail: "late"})
	if progress.String() != before {
		t.Fatal("late output")
	}
}

func TestCommandsKeepFailureAndWriterOrdering(t *testing.T) {
	t.Parallel()
	failure := errors.New("operation refused")
	for _, test := range []struct {
		name      string
		verb      string
		args      []string
		configure func(*Dependencies)
		want      string
	}{
		{"graph-selection", "graph", nil, func(o *Dependencies) {
			o.Select = func(context.Context, depsrun.Selection) ([]deps.Repository, error) { return nil, failure }
		}, ""},
		{"drift-selection", "drift", nil, func(o *Dependencies) {
			o.Select = func(context.Context, depsrun.Selection) ([]deps.Repository, error) { return nil, failure }
		}, ""},
		{"set-selection", "set", []string{"go", "acme@v1.0.0"}, func(o *Dependencies) {
			o.Select = func(context.Context, depsrun.Selection) ([]deps.Repository, error) { return nil, failure }
		}, ""},
		{"bump-selection", "bump", []string{"go", "--fleet"}, func(o *Dependencies) {
			o.Select = func(context.Context, depsrun.Selection) ([]deps.Repository, error) { return nil, failure }
		}, ""},
		{"graph-engine", "graph", nil, func(o *Dependencies) {
			o.Graph = func(context.Context, depsrun.GraphRequest) (depsrun.GraphResult, error) {
				return depsrun.GraphResult{}, failure
			}
		}, ""},
		{"drift-engine", "drift", nil, func(o *Dependencies) {
			o.Drift = func(context.Context, depsrun.DriftRequest) (deps.DriftReport, error) {
				return deps.DriftReport{}, failure
			}
		}, ""},
		{"peers-engine", "peers", []string{"@acme/lib"}, func(o *Dependencies) {
			o.Peers = func(context.Context, deps.PeerOptions) (deps.PeerReport, error) { return deps.PeerReport{}, failure }
		}, ""},
		{"set-persistence", "set", []string{"go", "acme@v1.0.0"}, func(o *Dependencies) {
			o.Set = func(context.Context, depsrun.SetRequest) (depsrun.SetResult, error) {
				return depsrun.SetResult{}, failure
			}
		}, ""},
		{"bump-seed", "bump", []string{"go", "--fleet"}, func(o *Dependencies) {
			o.Seed = func(context.Context, depsrun.SeedRequest) (depsrun.SeedResult, error) {
				return depsrun.SeedResult{}, failure
			}
		}, ""},
		{"graph-format", "graph", []string{"--format", "bogus"}, func(o *Dependencies) {}, "unknown"},
		{"drift-format", "drift", []string{"--format", "bogus"}, func(o *Dependencies) {}, "unknown"},
		{"set-format", "set", []string{"go", "acme@v1.0.0", "--format", "bogus"}, func(o *Dependencies) {}, "unknown"},
		{"bump-format", "bump", []string{"go", "--fleet", "--format", "bogus"}, func(o *Dependencies) {}, "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := boundaryOps()
			test.configure(&ops)
			cmd := commandFor(t, test.verb, testRuntime(), ops)
			err := executeArgs(cmd, test.args...)
			if test.want == "" {
				if err != failure {
					t.Fatalf("exact operation error=%v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("format error=%v", err)
			}
		})
	}
	for _, verb := range []string{"graph", "drift", "peers", "set", "bump"} {
		t.Run(verb+"-writer", func(t *testing.T) {
			ops := boundaryOps()
			cmd := commandFor(t, verb, testRuntime(), ops)
			args := []string{}
			switch verb {
			case "peers":
				args = []string{"@acme/lib"}
			case "set":
				args = []string{"go", "acme@v1.0.0"}
			case "bump":
				args = []string{"go", "--fleet"}
			}
			cmd.SetOut(cwDepsFailingWriter{})
			if err := executeArgs(cmd, args...); err == nil || !strings.Contains(err.Error(), "write refused") {
				t.Fatalf("writer=%v", err)
			}
		})
	}
	t.Run("graph-open-after-output", func(t *testing.T) {
		ops := boundaryOps()
		var out bytes.Buffer
		ops.OpenBrowser = func(path string) error {
			if path != "report.html" || out.Len() == 0 {
				t.Fatalf("browser before output/path=%q", path)
			}
			return failure
		}
		cmd := commandFor(t, "graph", testRuntime(), ops)
		cmd.SetOut(&out)
		if err := executeArgs(cmd, "--open"); !errors.Is(err, failure) || !strings.Contains(err.Error(), "reports were written") {
			t.Fatalf("open=%v", err)
		}
	})
}

func TestCurrentFlagsContextAndWritersAreReadOnEveryExecute(t *testing.T) {
	t.Parallel()
	type key struct{}
	flags := shared.Flags{ProjectsRoot: "old"}
	runtime := shared.Runtime{Flags: func() shared.Flags { return flags }, ExitError: testRuntime().ExitError}
	ops := boundaryOps()
	var selected, requests int
	ctx := context.WithValue(context.Background(), key{}, "current")
	ops.Select = func(got context.Context, r depsrun.Selection) ([]deps.Repository, error) {
		selected++
		if got != ctx || r.ProjectsRoot != flags.ProjectsRoot || r.RepositoryPath != "checkout" || r.Filter != flags.Filter || len(r.ExtraOrgs) != 1 {
			t.Fatalf("current selection=%+v context=%v", r, got)
		}
		return []deps.Repository{{Slug: "acme/app"}}, nil
	}
	ops.Graph = func(got context.Context, r depsrun.GraphRequest) (depsrun.GraphResult, error) {
		requests++
		if got != ctx || r.Options.GitHubDir != flags.ProjectsRoot {
			t.Fatalf("graph=%+v", r)
		}
		r.Finish("completed")
		return depsrun.GraphResult{}, nil
	}
	cmd := commandFor(t, "graph", runtime, ops)
	cmd.SetContext(ctx)
	flags = shared.Flags{ProjectsRoot: "current", Filter: "acme", ExtraOrgs: []string{"acme"}, NonInteractive: true}
	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		cmd.SetOut(&out)
		if err := executeArgs(cmd, "checkout", "--format", "json"); err != nil || out.Len() == 0 {
			t.Fatalf("execute %d=%v output=%q", i, err, out.String())
		}
		flags.ProjectsRoot = "next"
	}
	if selected != 2 || requests != 2 {
		t.Fatalf("calls=%d/%d", selected, requests)
	}
	if commandExecutionContext(nil) == nil || depsBumpParallelExplicit(nil) {
		t.Fatal("nil CLI helper defaults")
	}
	if scopes := depsrun.DerivedScopes(true, []string{"acme/*"}); len(scopes) != 1 {
		t.Fatalf("scopes=%v", scopes)
	}
	if scopes := depsrun.DerivedScopes(false, []string{"acme/*"}); scopes != nil {
		t.Fatalf("unused scopes=%v", scopes)
	}
}

func TestArgumentsAndFindingsKeepTheirExactBoundary(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		verb string
		args []string
		want string
	}{
		{"bump", []string{"cobol"}, "go and npm"}, {"bump", []string{"go"}, "requires --fleet"},
		{"set", []string{"go", "acme@v1", "--fleet", "checkout"}, "repository-path cannot"},
		{"set", []string{"go", "acme@v1", "--checks", "bad"}, "unknown check"},
		{"set", []string{"go", "acme@v1", "--dependency-order", "--propagate"}, "cannot be used together"},
		{"graph", []string{"--fleet", "checkout"}, "repository-path cannot"}, {"graph", []string{"--view", "bad"}, "unknown dependency graph view"},
		{"drift", []string{"--ecosystem", "cobol"}, "go and npm"},
		{"set", []string{"go", "acme@v1", "--no-verify", "--validation", "full"}, "cannot be used together"},
		{"set", []string{"go", "acme@v1", "--validation", "fast", "--dry-run", "--checks", "test"}, "cannot be used together"},
	} {
		t.Run(strings.Join(test.args, "/"), func(t *testing.T) {
			cmd := commandFor(t, test.verb, testRuntime(), boundaryOps())
			if err := executeArgs(cmd, test.args...); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("args=%v err=%v", test.args, err)
			}
		})
	}
	for _, verb := range []string{"drift", "peers"} {
		t.Run(verb+"-findings", func(t *testing.T) {
			sentinel := errors.New("typed findings")
			runtime := testRuntime()
			runtime.ExitError = func(code int, message string) error {
				if code != shared.ExitFindings || !strings.Contains(message, "see") {
					t.Fatalf("typed error=%d %q", code, message)
				}
				return sentinel
			}
			ops := boundaryOps()
			args := []string{"--format", "json"}
			if verb == "drift" {
				ops.Drift = func(_ context.Context, r depsrun.DriftRequest) (deps.DriftReport, error) {
					r.Finish("completed with findings")
					return deps.DriftReport{Summary: deps.DriftSummary{Error: 1}}, nil
				}
				args = append(args, "--ecosystem", "")
			} else {
				ops.Peers = func(context.Context, deps.PeerOptions) (deps.PeerReport, error) {
					return deps.PeerReport{Summary: deps.PeerSummary{Missing: 1}}, nil
				}
				args = append(args, "@acme/lib")
			}
			cmd := commandFor(t, verb, runtime, ops)
			var out bytes.Buffer
			cmd.SetOut(&out)
			if err := executeArgs(cmd, args...); err != sentinel || out.Len() == 0 {
				t.Fatalf("findings=%v output=%q", err, out.String())
			}
		})
	}
	t.Run("set-run-error-after-report", func(t *testing.T) {
		sentinel := errors.New("run refused")
		ops := boundaryOps()
		ops.Set = func(_ context.Context, r depsrun.SetRequest) (depsrun.SetResult, error) {
			r.Finish("failed")
			return depsrun.SetResult{Report: deps.Report{Operation: "actual"}, RunError: sentinel}, nil
		}
		cmd := commandFor(t, "set", testRuntime(), ops)
		var out bytes.Buffer
		cmd.SetOut(&out)
		if err := executeArgs(cmd, "go", "acme@v1.0.0"); err != sentinel || out.Len() == 0 {
			t.Fatalf("run=%v output=%q", err, out.String())
		}
	})
}

func TestConcreteReportsKeepYAMLDerivedDatesAndEveryWriteError(t *testing.T) {
	t.Parallel()
	date := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	writers := []struct {
		name  string
		write func(*cobra.Command, string) error
	}{
		{"set", func(c *cobra.Command, f string) error {
			return writeDependencyReport(c, deps.Report{Operation: "actual"}, f)
		}},
		{"bump", func(c *cobra.Command, f string) error {
			return writeDependencyReport(c, deps.BumpReport{SeedEvents: []deps.ReleaseEvent{{Dependency: "acme", Version: "v1", CheckedAt: date}}}, f)
		}},
		{"drift", func(c *cobra.Command, f string) error {
			return writeDependencyReport(c, deps.DriftReport{ObservedAt: date}, f)
		}},
		{"peers", func(c *cobra.Command, f string) error {
			return writeDependencyReport(c, deps.PeerReport{ObservedAt: date}, f)
		}},
	}
	for _, w := range writers {
		t.Run(w.name, func(t *testing.T) {
			var out bytes.Buffer
			cmd := cwDepsNewOutCommand(&out)
			if err := w.write(cmd, "json"); err != nil || out.Len() == 0 {
				t.Fatalf("concrete JSON=%v output=%q", err, out.String())
			}
			if w.name != "set" && !strings.Contains(out.String(), "10000") {
				t.Fatalf("out-of-range YAML date not retained: %q", out.String())
			}
			for _, format := range []string{"markdown", "yaml", "json"} {
				if err := w.write(cwDepsNewOutCommand(cwDepsFailingWriter{}), format); err == nil || !strings.Contains(err.Error(), "write refused") {
					t.Fatalf("%s writer=%v", format, err)
				}
			}
		})
	}
}

func TestOperationsBindTheActualServiceAndCampaignFactory(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("peer service refused")
	service := depsrun.New(depsrun.Dependencies{InspectPeers: func(context.Context, deps.PeerOptions) (deps.PeerReport, error) { return deps.PeerReport{}, sentinel }})
	ops := Operations(service, nil)
	if _, err := ops.Peers(context.Background(), deps.PeerOptions{}); err != sentinel {
		t.Fatalf("service binding=%v", err)
	}
	campaign := ops.Campaign(io.Discard, false, "deps")
	if campaign == nil || campaign.Reporter() != nil {
		t.Fatal("actual default nonterminal campaign")
	}
	campaign.Finish("completed")
}

func TestRemainingCommandPathsPreserveRequestsAndFinishAuthority(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		verb string
		args []string
	}{
		{"bump", []string{"go", "--fleet", "--validation", "bad"}},
		{"set", []string{"go", "not-a-target"}},
	} {
		t.Run(test.verb+"-refusal", func(t *testing.T) {
			if err := executeArgs(commandFor(t, test.verb, testRuntime(), boundaryOps()), test.args...); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
	t.Run("propagate", func(t *testing.T) {
		ops := boundaryOps()
		called := false
		ops.Bump = func(_ context.Context, r depsrun.BumpRequest) (depsrun.BumpResult, error) {
			called = true
			if len(r.Events) != 1 || r.Events[0].Source != "exact_set" || r.Options.Ecosystem != deps.EcosystemGo {
				t.Fatalf("propagate=%+v", r)
			}
			return depsrun.BumpResult{Report: deps.BumpReport{Operation: "actual"}}, nil
		}
		cmd := commandFor(t, "set", testRuntime(), ops)
		if err := executeArgs(cmd, "go", "acme@v1.0.0", "--fleet", "--propagate"); err != nil || !called {
			t.Fatalf("propagate=%v called=%v", err, called)
		}
	})
	for _, verb := range []string{"drift", "peers", "bump"} {
		t.Run(verb+"-success", func(t *testing.T) {
			cmd := commandFor(t, verb, testRuntime(), boundaryOps())
			args := []string{}
			switch verb {
			case "drift":
				args = []string{"checkout"}
			case "peers":
				args = []string{"@acme/lib"}
			case "bump":
				args = []string{"go", "--fleet"}
			}
			if err := executeArgs(cmd, args...); err != nil {
				t.Fatal(err)
			}
		})
	}
	if got := dependencyOptions(shared.Flags{}, depsSetOptions{}, nil); got.ValidationMode != deps.ValidationModeFull || !got.Verify {
		t.Fatalf("default lifecycle=%+v", got)
	}
}

func TestBumpRequestsRetainExplicitAndOmittedParallelFlagAuthority(t *testing.T) {
	t.Parallel()
	for _, explicit := range []bool{true, false} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			ops := boundaryOps()
			calls := 0
			ops.Bump = func(_ context.Context, r depsrun.BumpRequest) (depsrun.BumpResult, error) {
				calls++
				want := 1
				if explicit {
					want = 2
				}
				if r.ResumeParallelExplicit != explicit || r.Options.Parallel != want || r.Options.GitHubDir != "engine-root" || r.ProjectsRoot != "engine-root" {
					t.Fatalf("parallel/root request=%+v", r)
				}
				return depsrun.BumpResult{Report: deps.BumpReport{Operation: "actual"}}, nil
			}
			runtime := testRuntime()
			runtime.Flags = func() shared.Flags { return shared.Flags{ProjectsRoot: "engine-root", NonInteractive: true} }
			cmd := commandFor(t, "bump", runtime, ops)
			args := []string{"go", "--fleet", "--resume"}
			if explicit {
				args = append(args, "--parallel", "2")
			}
			if err := executeArgs(cmd, args...); err != nil || calls != 1 {
				t.Fatalf("execution=%v calls=%d", err, calls)
			}
		})
	}
}
