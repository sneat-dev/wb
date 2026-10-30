package cmdbranch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestCommandsBindParsedOptionsContextAndIndependentStreams(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"list", "count", "quarantine", "archive-target"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			flags := shared.Flags{ProjectsRoot: "before", Filter: "before"}
			called := false
			ctx := t.Context()
			var out, stderr bytes.Buffer
			deps := depsForTest()
			deps.List = func(got context.Context, o worktrees.BranchListOptions) (worktrees.BranchListOutcome, error) {
				called = true
				if got != ctx || o.ProjectsRoot != "after" || o.Filter != "late" || o.Progress != &stderr || o.Base != "release" || o.Scope != "all" || o.Only != "retired" || o.OlderThan != 2*time.Hour || o.Repository != "acme/app" || o.Org != "acme" || o.Name != "retired/*" || o.IncludeRetired != (kind == "list") || o.WithPRs != (kind == "list") || o.Branch != map[bool]string{true: "retired/old", false: ""}[kind == "list"] {
					t.Fatal(o, got)
				}
				return worktrees.BranchListOutcome{Diagnostics: []string{"inventory note"}}, nil
			}
			deps.Quarantine = func(got context.Context, o worktrees.BranchQuarantineOptions) (worktrees.BranchQuarantineOutcome, error) {
				called = true
				if got != ctx || o.ProjectsRoot != "after" || o.Repository != "acme/app" || o.Branch != "task/old" || o.SHA != "abc" || o.Reason != "operator reason" || o.Description != "preserved work" || !reflect.DeepEqual(o.Replacement, []string{"PR #113", "commit abc123"}) || o.Retention != "delete after verification" || !o.Annotate || o.Manifest != "" || !o.Apply || o.ReportDir != "reports" {
					t.Fatal(o, got)
				}
				return worktrees.BranchQuarantineOutcome{}, nil
			}
			deps.ArchiveTarget = func(got context.Context, repo string) (worktrees.RetiredArchivePlan, error) {
				called = true
				if got != ctx || repo != "acme/app" {
					t.Fatal(got, repo)
				}
				return worktrees.RetiredArchivePlan{}, nil
			}
			command := New(shared.Runtime{Flags: func() shared.Flags { return flags }}, deps)
			command.PersistentFlags().StringVar(&flags.ProjectsRoot, "projects-root", "before", "")
			command.PersistentFlags().StringVar(&flags.Filter, "filter", "before", "")
			args := []string{kind, "--projects-root=after", "--filter=late", "--repo=acme/app"}
			switch kind {
			case "list", "count":
				args = append(args, "--base=release", "--scope=all", "--only=retired", "--older-than=2h", "--org=acme", "--name=retired/*")
				if kind == "list" {
					args = append(args, "--branch=retired/old", "--include-retired", "--with-prs")
				}
			case "quarantine":
				args = append(args, "--branch=task/old", "--sha=abc", "--reason=operator reason", "--description=preserved work", "--replacement=PR #113", "--replacement=commit abc123", "--retention=delete after verification", "--annotate", "--apply", "--report-dir=reports")
			}
			command.SetContext(ctx)
			command.SetOut(&out)
			command.SetErr(&stderr)
			command.SetArgs(args)
			if err := command.Execute(); err != nil || !called {
				t.Fatal(err, called, out.String(), stderr.String())
			}
			if (kind == "list" || kind == "count") && stderr.String() != "diagnostic: inventory note\n" {
				t.Fatal(stderr.String())
			}
		})
	}
}

func TestCommandsReturnOperationErrorsBeforeRendering(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"list", "count", "quarantine", "archive-target"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			failure := errors.New("operation refused")
			deps := depsForTest()
			deps.List = func(context.Context, worktrees.BranchListOptions) (worktrees.BranchListOutcome, error) {
				return worktrees.BranchListOutcome{}, failure
			}
			deps.Quarantine = func(context.Context, worktrees.BranchQuarantineOptions) (worktrees.BranchQuarantineOutcome, error) {
				return worktrees.BranchQuarantineOutcome{}, failure
			}
			deps.ArchiveTarget = func(context.Context, string) (worktrees.RetiredArchivePlan, error) {
				return worktrees.RetiredArchivePlan{}, failure
			}
			command := New(runtimeForTest(), deps)
			command.SilenceUsage = true
			command.SilenceErrors = true
			var out, stderr bytes.Buffer
			command.SetOut(&out)
			command.SetErr(&stderr)
			command.SetArgs([]string{kind, "--repo=acme/app"})
			if err := command.Execute(); err != failure || out.Len() != 0 {
				t.Fatal(err, out.String())
			}
		})
	}
}

func TestInvalidFormatsAndExtraArgumentsNeverCallOperations(t *testing.T) {
	t.Parallel()
	deps := Dependencies{List: func(context.Context, worktrees.BranchListOptions) (worktrees.BranchListOutcome, error) {
		t.Fatal("list called")
		return worktrees.BranchListOutcome{}, nil
	}, Quarantine: func(context.Context, worktrees.BranchQuarantineOptions) (worktrees.BranchQuarantineOutcome, error) {
		t.Fatal("quarantine called")
		return worktrees.BranchQuarantineOutcome{}, nil
	}, ArchiveTarget: func(context.Context, string) (worktrees.RetiredArchivePlan, error) {
		t.Fatal("archive called")
		return worktrees.RetiredArchivePlan{}, nil
	}}
	for _, kind := range []string{"list", "count", "quarantine", "archive-target"} {
		for _, arg := range []string{"extra", "--format=toml"} {
			if kind == "quarantine" && strings.HasPrefix(arg, "--format") {
				continue
			}
			cmd := New(runtimeForTest(), deps)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{kind, arg})
			if err := cmd.Execute(); err == nil {
				t.Fatal(kind, arg)
			}
		}
	}
}

func TestReportWriterErrorsAndUnrepresentableDatesRetainIdentity(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"list", "count", "archive-target"} {
		for _, format := range []string{"text", "json", "yaml"} {
			t.Run(kind+format, func(t *testing.T) {
				t.Parallel()
				failure := errors.New("report writer refused")
				cmd := New(runtimeForTest(), depsForTest())
				cmd.SilenceUsage = true
				cmd.SilenceErrors = true
				cmd.SetOut(failingWriter{err: failure})
				cmd.SetErr(io.Discard)
				cmd.SetArgs([]string{kind, "--format=" + format, "--repo=acme/app"})
				if err := cmd.Execute(); err != failure {
					t.Fatal(err)
				}
			})
		}
	}
	badDate := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, kind := range []string{"list", "count", "archive-target"} {
		for _, format := range []string{"json", "yaml"} {
			t.Run(fmt.Sprint(kind, format, "date"), func(t *testing.T) {
				t.Parallel()
				deps := depsForTest()
				deps.List = func(context.Context, worktrees.BranchListOptions) (worktrees.BranchListOutcome, error) {
					return worktrees.BranchListOutcome{GeneratedAt: badDate}, nil
				}
				deps.ArchiveTarget = func(context.Context, string) (worktrees.RetiredArchivePlan, error) {
					return worktrees.RetiredArchivePlan{GeneratedAt: badDate}, nil
				}
				cmd := New(runtimeForTest(), deps)
				cmd.SilenceUsage = true
				cmd.SilenceErrors = true
				cmd.SetErr(io.Discard)
				var out bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetArgs([]string{kind, "--format=" + format, "--repo=acme/app"})
				if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "MarshalJSON") || out.Len() != 0 {
					t.Fatal(err, out.String())
				}
			})
		}
	}
}

func TestQuarantineRowsAndReportPropagateEveryWriterFailure(t *testing.T) {
	t.Parallel()
	deps := depsForTest()
	deps.Quarantine = func(context.Context, worktrees.BranchQuarantineOptions) (worktrees.BranchQuarantineOutcome, error) {
		return worktrees.BranchQuarantineOutcome{Results: []worktrees.BranchQuarantineResult{{Outcome: "refused", Ref: "task/one", Error: "changed"}, {Outcome: "annotated", Ref: "retired/two", RetiredSHA: "metadata-commit"}}, ReportPath: "report.json"}, nil
	}
	for after := 0; after <= 3; after++ {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			t.Parallel()
			failure := errors.New("quarantine writer refused")
			cmd := New(runtimeForTest(), deps)
			var out bytes.Buffer
			cmd.SetOut(&out)
			if after < 3 {
				cmd.SetOut(&failAfterWriter{remaining: after, err: failure})
			}
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"quarantine"})
			err := cmd.Execute()
			if after < 3 && err != failure {
				t.Fatal(err)
			}
			if after == 3 && (err != nil || out.String() != "refused task/one: changed\nannotated retired/two -> metadata-commit\nreport: report.json\n") {
				t.Fatal(err, out.String())
			}
		})
	}
}

func TestLateTextWriterErrorsPrecedeDiagnosticsAndReportPaths(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"list", "count"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			failure := errors.New("late writer refused")
			deps := depsForTest()
			deps.List = func(context.Context, worktrees.BranchListOptions) (worktrees.BranchListOutcome, error) {
				return worktrees.BranchListOutcome{Entries: []worktrees.BranchEntry{{Repository: "acme/app", Branch: "task/old"}}, Totals: map[string]int{"contained": 1}, Diagnostics: []string{"later diagnostic"}}, nil
			}
			beforeFailure := 1
			if kind == "list" {
				beforeFailure = 4
			}
			cmd := New(runtimeForTest(), deps)
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			cmd.SetOut(&failAfterWriter{remaining: beforeFailure, err: failure})
			var stderr bytes.Buffer
			cmd.SetErr(&stderr)
			cmd.SetArgs([]string{kind})
			if err := cmd.Execute(); err != failure || stderr.Len() != 0 {
				t.Fatal(err, stderr.String())
			}
		})
	}
}
