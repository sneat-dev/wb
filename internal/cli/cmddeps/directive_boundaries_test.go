package cmddeps

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/depsrun"
)

type directiveExit struct {
	code    int
	message string
}

func (e *directiveExit) Error() string { return e.message }

type directiveKey struct{}

func directiveRecordingRuntime(get func() shared.Flags) shared.Runtime {
	return shared.Runtime{Flags: get, ExitError: func(code int, message string) error { return &directiveExit{code, message} }}
}

func TestDirectiveCheckKeepsArgsDefaultsAndBestEffortOutput(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("write refused")
	ctx := context.WithValue(context.Background(), directiveKey{}, "check")
	runtime := directiveRecordingRuntime(func() shared.Flags { return shared.Flags{} })
	for _, stage := range []string{"path", "empty", "findings", "clean"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			command := newDirectiveCheck(runtime, DirectiveDependencies{Check: func(got context.Context, request depsrun.DirectiveCheckRequest, emit func(depsrun.DirectiveCheckRow)) (depsrun.DirectiveCheckResult, error) {
				if got != ctx || request.Directory != "." || request.Policy.GoVersion != defaultDirectiveGoVersion || request.Policy.Toolchain != defaultDirectiveToolchain || request.Options.Timeout != 2*time.Minute || request.Options.Retry != 1 || !request.Apply || request.CodeQLCeiling != defaultCodeQLCeiling {
					t.Fatalf("request=%+v ctx=%v", request, got)
				}
				if stage == "path" {
					return depsrun.DirectiveCheckResult{}, sentinel
				}
				if stage == "empty" {
					return depsrun.DirectiveCheckResult{}, nil
				}
				emit(depsrun.DirectiveCheckRow{Label: "one", Error: errors.New("module refused")})
				emit(depsrun.DirectiveCheckRow{Label: "two", Assessment: deps.DirectiveAssessment{Verdict: deps.DirectiveCompliant}, Detail: "current"})
				attention := 0
				if stage == "findings" {
					attention = 2
				}
				return depsrun.DirectiveCheckResult{ModuleCount: 2, Attention: attention}, nil
			}})
			command.SetContext(ctx)
			command.SetOut(cwDepsFailingWriter{})
			command.SetErr(io.Discard)
			command.SilenceErrors = true
			command.SilenceUsage = true
			command.SetArgs([]string{"", "--apply"})
			err := command.Execute()
			var exit *directiveExit
			if stage == "path" {
				if !errors.As(err, &exit) || exit.code != shared.ExitUsage || exit.message != sentinel.Error() {
					t.Fatalf("path=%v", err)
				}
			} else if stage == "findings" {
				if !errors.As(err, &exit) || exit.code != shared.ExitFindings || !strings.Contains(exit.message, "2 module(s)") {
					t.Fatalf("findings=%v", err)
				}
			} else if err != nil {
				t.Fatalf("ignored writer error escaped: %v", err)
			}
		})
	}
	command := newDirectiveCheck(runtime, DirectiveDependencies{Check: func(context.Context, depsrun.DirectiveCheckRequest, func(depsrun.DirectiveCheckRow)) (depsrun.DirectiveCheckResult, error) {
		t.Fatal("invalid args executed")
		return depsrun.DirectiveCheckResult{}, nil
	}})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"a", "b"})
	if err := command.Execute(); err == nil {
		t.Fatal("multiple args accepted")
	}
}
func TestDirectiveReportReadsCurrentFlagsAndPreservesExactSelectionPolicy(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), directiveKey{}, "report")
	flags := shared.Flags{ProjectsRoot: "stale"}
	refused := errors.New("selection refused")
	runtime := directiveRecordingRuntime(func() shared.Flags { return flags })
	calls := 0
	command := newDirectiveReport(runtime, DirectiveDependencies{Select: func(got context.Context, request depsrun.Selection) ([]deps.Repository, error) {
		calls++
		if got != ctx || request.ProjectsRoot != flags.ProjectsRoot || request.Filter != flags.Filter || len(request.ExtraOrgs) != 1 || !request.Fleet || request.Parallel != 1 || request.Retry != 0 || request.Timeout != 0 || request.Match != "acme/*" || request.Regex != "repo" {
			t.Fatalf("request=%+v/%v", request, got)
		}
		if calls == 1 {
			return nil, refused
		}
		return []deps.Repository{{Slug: "acme/repo"}}, nil
	}, Report: func(got context.Context, repos []deps.Repository, p deps.DirectivePolicy, o deps.Options, ceiling string) []depsrun.DirectiveRow {
		if got != ctx || repos[0].Slug != "acme/repo" || p.GoVersion != "1.25.0" || o.Timeout != time.Second || o.Retry != 1 || ceiling != "1.25.7" {
			t.Fatalf("report=%+v/%+v/%q", p, o, ceiling)
		}
		return []depsrun.DirectiveRow{{Repository: "acme/repo", Verdict: string(deps.DirectiveCannotComply), CodeQLAtRisk: true}, {Repository: "other", Verdict: string(deps.DirectiveError)}}
	}})
	command.SetContext(ctx)
	command.SilenceErrors = true
	command.SilenceUsage = true
	flags = shared.Flags{ProjectsRoot: "current", Filter: "current-filter", ExtraOrgs: []string{"extra"}}
	var first bytes.Buffer
	command.SetOut(&first)
	command.SetErr(io.Discard)
	args := []string{"--match", "acme/*", "--regex", "repo", "--go-version", "1.25.0", "--timeout", "1s", "--codeql-ceiling", "1.25.7", "--format", "unknown"}
	command.SetArgs(args)
	err := command.Execute()
	var exit *directiveExit
	if !errors.As(err, &exit) || exit.code != shared.ExitUsage || exit.message != refused.Error() || first.Len() != 0 {
		t.Fatalf("selection=%v output=%q", err, first.String())
	}
	flags.ProjectsRoot = "second-current"
	var second bytes.Buffer
	command.SetOut(&second)
	command.SetArgs(args)
	err = command.Execute()
	if !errors.As(err, &exit) || exit.code != shared.ExitFindings || !strings.Contains(exit.message, "1 module(s) cannot comply, 1 module(s) errored, 1 at risk") || !strings.Contains(second.String(), "acme/repo") || first.Len() != 0 {
		t.Fatalf("report=%v output=%q", err, second.String())
	}
}
func TestDirectiveReportJSONWriterErrorsPrecedeFindings(t *testing.T) {
	t.Parallel()
	runtime := directiveRecordingRuntime(func() shared.Flags { return shared.Flags{} })
	ops := DirectiveDependencies{Select: func(context.Context, depsrun.Selection) ([]deps.Repository, error) { return nil, nil }, Report: func(context.Context, []deps.Repository, deps.DirectivePolicy, deps.Options, string) []depsrun.DirectiveRow {
		return []depsrun.DirectiveRow{{Verdict: string(deps.DirectiveError)}}
	}}
	command := newDirectiveReport(runtime, ops)
	command.SetOut(cwDepsFailingWriter{})
	command.SetErr(io.Discard)
	command.SilenceErrors = true
	command.SetArgs([]string{"--format", "json"})
	if err := command.Execute(); err == nil || err.Error() != "cwDeps: write refused" {
		t.Fatalf("writer=%v", err)
	}
}
func TestDirectiveOperationsBindTheActualServiceMethods(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service := depsrun.New(depsrun.Dependencies{Abs: func(s string) (string, error) { return s, nil }, Identity: func(string, string) (string, string, error) { return "acme/local", "origin", nil }, DiscoverModules: func(string) []string { return nil }})
	ops := DirectiveOperations(service)
	selected, err := ops.Select(ctx, depsrun.Selection{RepositoryPath: "/private/repo", Parallel: 1})
	if err != nil || len(selected) != 1 || selected[0].Slug != "acme/local" {
		t.Fatalf("select=%+v/%v", selected, err)
	}
	result, err := ops.Check(ctx, depsrun.DirectiveCheckRequest{}, func(depsrun.DirectiveCheckRow) { t.Fatal("no modules") })
	if err != nil || result.ModuleCount != 0 {
		t.Fatalf("check=%+v/%v", result, err)
	}
	rows := ops.Report(ctx, []deps.Repository{{Slug: "remote"}}, deps.DirectivePolicy{}, deps.Options{}, "")
	if len(rows) != 1 || rows[0].Verdict != "no-module" {
		t.Fatalf("rows=%+v", rows)
	}
}
