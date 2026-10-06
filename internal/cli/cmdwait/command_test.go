package cmdwait

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/waitregistry"
	"github.com/sneat-dev/wb/internal/waitrun"
	"github.com/spf13/cobra"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

type codedError struct {
	code    int
	message string
}

func (e *codedError) Error() string { return e.message }
func runtimeForTest() shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: "parsed", NonInteractive: true} }, ExitError: func(code int, msg string) error { return &codedError{code, msg} }}
}
func selectorFixture(value string) (string, string, error) {
	for n := 1; n <= 8; n++ {
		if value == fmt.Sprintf("acme/app#%d", n) || value == fmt.Sprintf("https://github.com/acme/app/pull/%d", n) {
			return "acme/app", fmt.Sprint(n), nil
		}
	}
	return "", "", errors.New("invalid selector")
}
func depsForTest() Dependencies {
	return Dependencies{ParseSelector: selectorFixture, Interactive: func(io.Writer, bool) bool { return false }, Discovery: func(cmd *cobra.Command, terms string) { cmd.Annotations = map[string]string{"terms": terms} }, RegisterWait: func(waitrun.Registration) func() { return func() {} }, Wait: func(_ context.Context, req waitrun.Request) waitrun.Output {
		return waitrun.Output{SchemaVersion: 1, ObservedAt: time.Unix(0, 0).UTC(), Status: waitrun.Settled, Observations: 1}
	}, Inspect: func(waitrun.ListRequest) (waitrun.ListResult, error) { return waitrun.ListResult{}, nil }}
}
func executePR(runtime shared.Runtime, deps Dependencies, args ...string) (string, string, error) {
	cmd := NewPR(runtime, deps)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}
func TestWaitPRRejectsUnusableInvocations(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{"unknown condition": {"acme/app#1", "--until=whenever"}, "no targets": nil, "malformed selector": {"not-a-selector"}, "interval over slice": {"acme/app#1", "--slice=10s", "--interval=30s"}, "zero slice": {"acme/app#1", "--slice=0"}, "zero interval": {"acme/app#1", "--interval=0"}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			deps := depsForTest()
			deps.RegisterWait = func(waitrun.Registration) func() { t.Fatal("registered invalid wait"); return nil }
			_, _, err := executePR(runtimeForTest(), deps, args...)
			if err == nil {
				t.Fatal("unusable invocation accepted")
			}
		})
	}
}
func TestValidateWaitBoundsRejectsANonPositiveInterval(t *testing.T) {
	t.Parallel()
	if err := validateWaitBounds(time.Minute, 0); err == nil || !strings.Contains(err.Error(), "--interval must be positive") {
		t.Fatal(err)
	}
}
func TestWaitPRDeduplicatesRepeatedTargets(t *testing.T) {
	t.Parallel()
	refs, err := parseWaitTargets([]string{"acme/app#1", "acme/app#1", "https://github.com/acme/app/pull/1"}, selectorFixture)
	if err != nil || len(refs) != 1 || refs[0].Selector != "acme/app#1" {
		t.Fatal(refs, err)
	}
}
func TestDirectExecutionRetainsCodedValidationErrors(t *testing.T) {
	t.Parallel()
	for _, condition := range []string{"whenever", "closed"} {
		deps := depsForTest()
		cmd := NewPR(runtimeForTest(), deps)
		cmd.SetErr(io.Discard)
		if err := cmd.Flags().Set("until", condition); err != nil {
			t.Fatal(err)
		}
		args := []string{"acme/app#1"}
		if condition == "closed" {
			args = []string{"broken"}
		}
		err := cmd.RunE(cmd, args)
		var coded *codedError
		if !errors.As(err, &coded) || coded.code != shared.ExitUsage {
			t.Fatal(err)
		}
	}
}
func TestWaitPRPendingResumesOnlyTheUnsettledTargets(t *testing.T) {
	t.Parallel()
	out := waitrun.Output{Targets: []waitrun.Target{{Selector: "acme/app#1", Status: waitrun.Settled}, {Selector: "acme/app#2", Status: waitrun.Pending}}}
	args := waitResumeArgs(out, waitrun.ChecksSettled, time.Minute, time.Second, false)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "acme/app#1") || !strings.Contains(joined, "acme/app#2") {
		t.Fatal(joined)
	}
}
func TestWaitPRWarnsBeforeItEatsTheGitHubBudget(t *testing.T) {
	t.Parallel()
	if warning := waitBudgetWarning(8, 5*time.Second); !strings.Contains(warning, "34560") || !strings.Contains(warning, "early failures") || !strings.Contains(warning, "red details") || !strings.Contains(warning, "uncharged") {
		t.Fatal(warning)
	}
	if warning := waitBudgetWarning(7, defaultWaitInterval); !strings.Contains(warning, "2520") {
		t.Fatal(warning)
	}
	for _, tc := range []struct {
		targets  int
		interval time.Duration
	}{{0, time.Minute}, {-1, time.Minute}, {1, 0}, {5, time.Minute}, {5, 54 * time.Second}} {
		if got := waitBudgetWarning(tc.targets, tc.interval); got != "" {
			t.Fatal(tc, got)
		}
	}
	if waitBudgetWarning(5, 53*time.Second) == "" {
		t.Fatal("over-boundary did not warn")
	}
}
func TestPRDefaultsContextFreshFlagsAndResults(t *testing.T) {
	t.Parallel()
	for _, status := range []string{waitrun.Settled, waitrun.Pending} {
		for _, jsonOut := range []bool{false, true} {
			t.Run(fmt.Sprint(status, jsonOut), func(t *testing.T) {
				t.Parallel()
				deps := depsForTest()
				registered, released := false, false
				var request waitrun.Request
				ctx := t.Context()
				deps.RegisterWait = func(req waitrun.Registration) func() {
					registered = true
					if req.ProjectsRoot != "parsed" || req.Kind != "pr" || req.Until != "checks-settled" || req.Slice != defaultWaitSlice || len(req.Targets) != 1 {
						t.Fatal(req)
					}
					return func() { released = true }
				}
				deps.Interactive = func(_ io.Writer, nonInteractive bool) bool {
					if !nonInteractive {
						t.Fatal("flags were not read")
					}
					return false
				}
				deps.Wait = func(got context.Context, req waitrun.Request) waitrun.Output {
					if got != ctx || !registered {
						t.Fatal("context or ordering")
					}
					request = req
					req.Progress(waitrun.Update{Observations: 2, Settled: 0, Total: 1, Interval: req.Interval})
					return waitrun.Output{SchemaVersion: 1, ObservedAt: time.Unix(0, 0).UTC(), Status: status, Observations: 2, Targets: []waitrun.Target{{Selector: "acme/app#1", Status: status}}}
				}
				cmd := NewPR(runtimeForTest(), deps)
				cmd.SetContext(ctx)
				var out, errOut bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(&errOut)
				cmd.SilenceErrors = true
				cmd.SilenceUsage = true
				args := []string{"acme/app#1"}
				if jsonOut {
					args = append(args, "--json")
				}
				cmd.SetArgs(args)
				err := cmd.Execute()
				if !released || request.Condition != waitrun.ChecksSettled || request.Interval != defaultWaitInterval || request.Slice != defaultWaitSlice {
					t.Fatal(released, request)
				}
				if !strings.Contains(errOut.String(), "wait pr: observation 2; 0/1 settled; next in 1m0s") {
					t.Fatal(errOut.String())
				}
				if status == waitrun.Pending {
					var coded *codedError
					if !errors.As(err, &coded) || coded.code != 1 || coded.message != "wait pr pending" {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if jsonOut {
					var got waitrun.Output
					if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.SchemaVersion != 1 || got.Status != status {
						t.Fatal(got, err)
					}
				} else if !strings.Contains(out.String(), "acme/app#1 "+status) {
					t.Fatal(out.String())
				}
			})
		}
	}
}

type recordingWriter struct {
	writes []string
	failAt int
	err    error
	events *[]string
}

func (w *recordingWriter) Write(raw []byte) (int, error) {
	w.writes = append(w.writes, string(raw))
	if w.events != nil {
		*w.events = append(*w.events, "write")
	}
	if len(w.writes) == w.failAt {
		return 0, w.err
	}
	return len(raw), nil
}
func TestWarningWriteFailureJoinsActualLiveBeforeRegistryRelease(t *testing.T) {
	t.Parallel()
	failure := errors.New("warning refused")
	events := []string{}
	writer := &recordingWriter{failAt: 2, err: failure, events: &events}
	deps := depsForTest()
	deps.RegisterWait = func(waitrun.Registration) func() { return func() { events = append(events, "release") } }
	deps.Wait = func(context.Context, waitrun.Request) waitrun.Output {
		t.Fatal("wait ran after warning failure")
		return waitrun.Output{}
	}
	cmd := NewPR(runtimeForTest(), deps)
	cmd.SetErr(writer)
	cmd.SetOut(io.Discard)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	args := []string{}
	for n := 1; n <= 7; n++ {
		args = append(args, fmt.Sprintf("acme/app#%d", n))
	}
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != failure {
		t.Fatal(err)
	}
	if len(writer.writes) != 4 || !strings.Contains(writer.writes[0], "wait pr: 7 target(s)") || !strings.Contains(writer.writes[1], "2520") || !strings.Contains(writer.writes[2], "wait pr: aborted") || !strings.HasSuffix(writer.writes[3], "\n") || !reflect.DeepEqual(events, []string{"write", "write", "write", "write", "release"}) {
		t.Fatal(writer.writes, events)
	} // Live.Finish's final newline occurs only after it joins its stopped channel.
}
func TestPRSuccessfulBudgetWarningAndOutputFailurePrecedeFindings(t *testing.T) {
	t.Parallel()
	for _, jsonOut := range []bool{false, true} {
		deps := depsForTest()
		deps.Wait = func(context.Context, waitrun.Request) waitrun.Output {
			return waitrun.Output{Status: waitrun.Pending, Targets: []waitrun.Target{{Selector: "acme/app#1", Status: waitrun.Pending}}}
		}
		failure := errors.New("output refused")
		cmd := NewPR(runtimeForTest(), deps)
		cmd.SetOut(&recordingWriter{failAt: 1, err: failure})
		cmd.SetErr(io.Discard)
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		args := []string{"acme/app#1", "--slice=1s", "--interval=1ms"}
		if jsonOut {
			args = append(args, "--json")
		}
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != failure {
			t.Fatal(err)
		}
	}
}
func TestListRowsJSONPruneAndErrors(t *testing.T) {
	t.Parallel()
	failure := errors.New("registry refused")
	for _, kind := range []string{"error", "empty", "rows", "json", "prune", "write", "rows write", "json write"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			deps := depsForTest()
			deps.Inspect = func(req waitrun.ListRequest) (waitrun.ListResult, error) {
				if req.ProjectsRoot != "parsed" || req.Prune != (kind == "prune") {
					t.Fatal(req)
				}
				if kind == "error" {
					return waitrun.ListResult{}, failure
				}
				result := waitrun.ListResult{Pruned: kind == "prune", Removed: 2}
				if kind == "rows" || kind == "json" || kind == "rows write" || kind == "json write" {
					result.Records = []waitregistry.Record{{Kind: "pr", Targets: []string{"acme/app#1"}, StartedAt: time.Unix(0, 0).UTC()}, {Kind: "agent", Stale: true}}
				}
				return result, nil
			}
			cmd := NewList(runtimeForTest(), deps)
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			var out bytes.Buffer
			cmd.SetOut(&out)
			if kind == "write" || kind == "prune" || strings.HasSuffix(kind, " write") {
				cmd.SetOut(&recordingWriter{failAt: 1, err: failure})
			}
			if kind == "json" || kind == "prune" || kind == "json write" {
				args := []string{"--json"}
				if kind == "prune" {
					args = append(args, "--prune")
				}
				cmd.SetArgs(args)
			}
			err := cmd.Execute()
			if kind == "error" || kind == "write" || kind == "prune" || strings.HasSuffix(kind, " write") {
				if err != failure {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "empty" && out.String() != "no outstanding waits\n" {
				t.Fatal(out.String())
			}
			if kind == "rows" && (!strings.Contains(out.String(), "waiting pr") || !strings.Contains(out.String(), "stale agent")) {
				t.Fatal(out.String())
			}
			if kind == "json" && !strings.Contains(out.String(), `"schema_version": 1`) {
				t.Fatal(out.String())
			}
		})
	}
	deps := depsForTest()
	deps.Inspect = func(waitrun.ListRequest) (waitrun.ListResult, error) {
		return waitrun.ListResult{Pruned: true, Removed: 3}, nil
	}
	cmd := NewList(runtimeForTest(), deps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--prune", "--json"})
	if err := cmd.Execute(); err != nil || out.String() != "pruned 3 stale wait(s)\n" {
		t.Fatal(out.String(), err)
	}
}
func TestRegistryCreatesFreshChildrenAndDiscovery(t *testing.T) {
	t.Parallel()
	children := Children{Checks: func() *cobra.Command { return &cobra.Command{Use: "checks"} }, Agent: func() *cobra.Command { return &cobra.Command{Use: "agent", Aliases: []string{"await"}} }, Operation: func() *cobra.Command { return &cobra.Command{Use: "operation"} }}
	first := New(runtimeForTest(), depsForTest(), children)
	second := New(runtimeForTest(), depsForTest(), children)
	if len(first.Commands()) != 5 || len(second.Commands()) != 5 {
		t.Fatal(first.Commands(), second.Commands())
	}
	for i, cmd := range first.Commands() {
		if cmd == second.Commands()[i] || cmd.Parent() != first || second.Commands()[i].Parent() != second {
			t.Fatal("shared command instance")
		}
		if (cmd.Name() == "pr" || cmd.Name() == "list") && cmd.Annotations["terms"] == "" {
			t.Fatal("discovery missing")
		}
	}
}
