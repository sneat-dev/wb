package cmdmigrate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/migrate"
	"github.com/sneat-dev/wb/internal/migraterun"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/spf13/cobra"
)

type codedError struct {
	code    int
	message string
}

func (e *codedError) Error() string { return e.message }
func testRuntime() shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: "projects"} }, ExitError: func(code int, message string) error { return &codedError{code, message} }}
}
func execute(t *testing.T, runtime shared.Runtime, deps Dependencies, args ...string) (string, string, error) {
	t.Helper()
	cmd := New(runtime, deps)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}
func assertCode(t *testing.T, err error, code int, message string) {
	t.Helper()
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != code || coded.message != message {
		t.Fatalf("error=%#v want %d %q", err, code, message)
	}
}
func TestCommandUsageAndOptionRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args    []string
		message string
		plain   bool
	}{
		{nil, "requires at least 1 arg", true},
		{[]string{"spec"}, "migrate requires at least one source root", true},
		{[]string{"spec", "root", "--commit"}, "require --hierarchical", true},
		{[]string{"spec", "root", "--parallel=1"}, "require --hierarchical", true},
		{[]string{"spec", "root", "--github-dir="}, "require --hierarchical", true},
		{[]string{"spec", "root", "--ref=main"}, "require --hierarchical", true},
		{[]string{"spec", "root", "--verify=full"}, "require --hierarchical", true},
		{[]string{"spec", "root", "--apply", "--check"}, "--apply and --check cannot be used together", false},
		{[]string{"spec", "--hierarchical", "--cleanup", "--apply"}, "--cleanup cannot be combined", false},
		{[]string{"spec", "root", "--hierarchical", "--check"}, "--check is not supported", false},
		{[]string{"spec", "root", "--hierarchical", "--no-verify", "--verify=full"}, "--no-verify and --verify", false},
		{[]string{"spec", "root", "--hierarchical", "--module-ref=broken"}, "invalid --module-ref", false},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			out, diagnostic, err := execute(t, testRuntime(), Dependencies{}, tc.args...)
			if err == nil || out != "" {
				t.Fatal(out, err)
			}
			if tc.plain {
				if !strings.Contains(err.Error(), tc.message) || diagnostic != "" {
					t.Fatal(err, diagnostic)
				}
				var coded *codedError
				if errors.As(err, &coded) {
					t.Fatal("plain error became coded")
				}
			} else {
				if !strings.Contains(diagnostic, tc.message) {
					t.Fatal(diagnostic)
				}
				message := localFailure
				if strings.Contains(strings.Join(tc.args, " "), "hierarchical") {
					message = campaignFailure
				}
				assertCode(t, err, 2, message)
			}
		})
	}
}
func TestModuleRefsPreserveGrammarAndOriginalValues(t *testing.T) {
	t.Parallel()
	refs, err := parseModuleRefs([]string{"github.com/acme/lib=v1.2.3", "github.com/acme/other=main", " spaced = ref=rest "})
	if err != nil || len(refs) != 3 || refs["github.com/acme/lib"] != "v1.2.3" || refs["github.com/acme/other"] != "main" || refs[" spaced "] != " ref=rest " {
		t.Fatal(refs, err)
	}
	for _, value := range []string{"noref", "=v1", "mod=", "", "  =v1", "mod= "} {
		if _, err := parseModuleRefs([]string{value}); err == nil {
			t.Fatal(value)
		}
	}
	if _, err := parseModuleRefs([]string{"a=v1", "a=v2"}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatal(err)
	}
}
func TestLocalOptionsCheckAndLateFormat(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		args           []string
		changes, apply bool
		code           int
	}{
		{"plan", nil, false, false, 0}, {"check no changes", []string{"--check"}, false, false, 0}, {"check changes", []string{"--check"}, true, false, 1}, {"apply", []string{"--apply"}, true, true, 0}, {"late unknown format", []string{"--format=toml"}, false, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			called := false
			deps := Dependencies{Local: func(ctx context.Context, req migraterun.LocalRequest) (migraterun.LocalResult, error) {
				called = true
				if req.SpecPath != "spec" || !reflect.DeepEqual(req.Roots, []string{"first", "second"}) || req.Apply != tc.apply || req.ReportDir != "reports" || ctx == nil {
					t.Fatal(req)
				}
				return migraterun.LocalResult{Report: migrate.Report{Status: "planned"}, HasChanges: tc.changes}, nil
			}}
			out, diagnostic, err := execute(t, testRuntime(), deps, append([]string{"spec", "first", "second", "--report-dir=reports"}, tc.args...)...)
			if !called {
				t.Fatal("operation not called")
			}
			if tc.code == 0 {
				if err != nil || out == "" || diagnostic != "" {
					t.Fatal(out, diagnostic, err)
				}
			} else {
				assertCode(t, err, tc.code, localFailure)
				if tc.code == 2 && (!strings.Contains(diagnostic, "unknown --format") || out != "") {
					t.Fatal(out, diagnostic)
				}
			}
		})
	}
}
func TestOperationErrorsAndDiagnostics(t *testing.T) {
	t.Parallel()
	failure := errors.New("spec refused")
	for _, hierarchical := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "campaign"}[hierarchical], func(t *testing.T) {
			t.Parallel()
			deps := Dependencies{Local: func(context.Context, migraterun.LocalRequest) (migraterun.LocalResult, error) {
				return migraterun.LocalResult{}, failure
			}, Campaign: func(context.Context, migraterun.CampaignRequest) (migraterun.CampaignResult, error) {
				return migraterun.CampaignResult{}, failure
			}}
			args := []string{"spec", "root"}
			message := localFailure
			if hierarchical {
				args = append(args, "--hierarchical")
				message = campaignFailure
			}
			out, diagnostic, err := execute(t, testRuntime(), deps, args...)
			assertCode(t, err, 2, message)
			if out != "" || diagnostic != "spec refused\n" {
				t.Fatal(out, diagnostic)
			}
		})
	}
}
func TestCampaignFlagsProgressAndExecutionErrorOrdering(t *testing.T) {
	t.Parallel()
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "failed"}[failed], func(t *testing.T) {
			t.Parallel()
			failure := errors.New("campaign failed")
			var out, errOut bytes.Buffer
			runtime := testRuntime()
			runtime.Flags = func() shared.Flags { return shared.Flags{ProjectsRoot: "parsed", NonInteractive: true} }
			deps := Dependencies{Interactive: func(w io.Writer, nonInteractive bool) bool {
				if w != &errOut || !nonInteractive {
					t.Fatal(w, nonInteractive)
				}
				return true
			}, Campaign: func(ctx context.Context, req migraterun.CampaignRequest) (migraterun.CampaignResult, error) {
				if ctx == nil || req.GitHubDir != "parsed" || req.SpecPath != "spec" || !reflect.DeepEqual(req.Roots, []string{"source"}) || req.Ref != "branch" || req.ReportDir != "reports" || req.Verify != migrate.VerifyNone || req.Parallel != 4 || req.ModuleRefs["module"] != "branch" || !req.Apply || !req.Commit || !req.Push || !req.PR || !req.Merge || !req.Resume || req.Cleanup {
					t.Fatal(req)
				}
				reporter := req.PrepareProgress("migration")
				if reporter == nil {
					t.Fatal("missing reporter")
				}
				reporter(progress.Event{Phase: "apply", State: progress.Completed})
				req.BeforePersist(migraterun.CampaignCompletion{SpecID: "migration", Failed: failed})
				status := "completed"
				if failed {
					status = "failed"
				}
				if !strings.Contains(errOut.String(), "migrate migration: "+status) {
					t.Fatal("progress not finished before persist", errOut.String())
				}
				result := migraterun.CampaignResult{Report: migrate.CampaignReport{Status: "completed"}}
				if failed {
					result.RunError = failure
				}
				return result, nil
			}}
			cmd := New(runtime, deps)
			cmd.SetOut(&out)
			cmd.SetErr(&errOut)
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			cmd.SetArgs([]string{"spec", "source", "--hierarchical", "--ref=branch", "--report-dir=reports", "--module-ref=module=branch", "--no-verify", "--apply", "--commit", "--push", "--pr", "--merge", "--resume", "--parallel=4"})
			err := cmd.Execute()
			if out.Len() == 0 {
				t.Fatal("no report")
			}
			if failed {
				assertCode(t, err, 2, campaignFailure)
				if !strings.HasSuffix(errOut.String(), "campaign failed\n") {
					t.Fatal(errOut.String())
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestCampaignDefaultsExplicitGithubAndCleanupIgnoresFormat(t *testing.T) {
	t.Parallel()
	for _, cleanup := range []bool{false, true} {
		t.Run(map[bool]string{false: "defaults", true: "cleanup"}[cleanup], func(t *testing.T) {
			t.Parallel()
			deps := Dependencies{Campaign: func(_ context.Context, req migraterun.CampaignRequest) (migraterun.CampaignResult, error) {
				if req.GitHubDir != "explicit" || req.Verify != migrate.VerifyFull || req.Ref != "main" || req.Parallel != 1 || req.Cleanup != cleanup {
					t.Fatal(req)
				}
				if cleanup {
					return migraterun.CampaignResult{Cleanup: true, Removed: []string{"one", "two"}}, nil
				}
				return migraterun.CampaignResult{Report: migrate.CampaignReport{Status: "completed"}}, nil
			}}
			args := []string{"spec", "--hierarchical", "--github-dir=explicit"}
			if cleanup {
				args = append(args, "--cleanup", "--format=toml")
			} else {
				args = append(args, "source")
			}
			out, diagnostic, err := execute(t, testRuntime(), deps, args...)
			if err != nil || diagnostic != "" || out == "" {
				t.Fatal(out, diagnostic, err)
			}
			if cleanup && out != "one\ntwo\n" {
				t.Fatal(out)
			}
		})
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }
func TestCommandWriterErrorsRemainObservable(t *testing.T) {
	t.Parallel()
	failure := errors.New("writer refused")
	for _, kind := range []string{"diagnostic", "local", "campaign", "cleanup"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			deps := Dependencies{Local: func(context.Context, migraterun.LocalRequest) (migraterun.LocalResult, error) {
				if kind == "diagnostic" {
					return migraterun.LocalResult{}, errors.New("operation")
				}
				return migraterun.LocalResult{}, nil
			}, Campaign: func(context.Context, migraterun.CampaignRequest) (migraterun.CampaignResult, error) {
				return migraterun.CampaignResult{Cleanup: kind == "cleanup", Removed: []string{"one", "two"}, RunError: errors.New("later run failure")}, nil
			}}
			cmd := New(testRuntime(), deps)
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			cmd.SetOut(failingWriter{failure})
			cmd.SetErr(failingWriter{failure})
			args := []string{"spec", "source"}
			if kind == "campaign" || kind == "cleanup" {
				args = append(args, "--hierarchical")
			}
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != failure {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
func TestCampaignUnknownFormatWinsExecutionError(t *testing.T) {
	t.Parallel()
	out, diagnostic, err := execute(t, testRuntime(), Dependencies{Campaign: func(context.Context, migraterun.CampaignRequest) (migraterun.CampaignResult, error) {
		return migraterun.CampaignResult{RunError: errors.New("must not print")}, nil
	}}, "spec", "root", "--hierarchical", "--format=toml")
	assertCode(t, err, 2, campaignFailure)
	if out != "" || !strings.Contains(diagnostic, "unknown --format") || strings.Contains(diagnostic, "must not print") {
		t.Fatal(out, diagnostic)
	}
}
func TestInstancesReadFlagsAfterParentParsing(t *testing.T) {
	t.Parallel()
	for _, projects := range []string{"first", "second"} {
		t.Run(projects, func(t *testing.T) {
			t.Parallel()
			var rootFlag string
			runtime := testRuntime()
			runtime.Flags = func() shared.Flags { return shared.Flags{ProjectsRoot: rootFlag} }
			parent := &cobra.Command{Use: "wb"}
			parent.PersistentFlags().StringVar(&rootFlag, "projects-root", "initial", "")
			parent.AddCommand(New(runtime, Dependencies{Campaign: func(_ context.Context, req migraterun.CampaignRequest) (migraterun.CampaignResult, error) {
				if req.GitHubDir != projects {
					t.Fatal(req.GitHubDir)
				}
				return migraterun.CampaignResult{Cleanup: true}, nil
			}}))
			parent.SetOut(io.Discard)
			parent.SetErr(io.Discard)
			parent.SetArgs([]string{"migrate", "spec", "--hierarchical", "--cleanup", "--projects-root", projects})
			if err := parent.Execute(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRepeatedExecutionResolvesCurrentFlagsWithoutBindingFallback(t *testing.T) {
	t.Parallel()
	runtime := testRuntime()
	root := "first"
	runtime.Flags = func() shared.Flags { return shared.Flags{ProjectsRoot: root} }
	ctx := t.Context()
	cmd := New(runtime, Dependencies{Campaign: func(got context.Context, req migraterun.CampaignRequest) (migraterun.CampaignResult, error) {
		if got != ctx || req.GitHubDir != root {
			t.Fatalf("context=%v root=%q want=%q", got, req.GitHubDir, root)
		}
		return migraterun.CampaignResult{Cleanup: true}, nil
	}})
	cmd.SetContext(ctx)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	for _, current := range []string{"first", "second"} {
		root = current
		cmd.SetArgs([]string{"spec", "--hierarchical", "--cleanup"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	}
}
