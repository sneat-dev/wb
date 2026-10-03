package cmdlayout

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/layout"
	"github.com/spf13/cobra"
)

type contextKey string

type codedError struct {
	code    int
	message string
}

func (e *codedError) Error() string { return e.message }
func testDependencies() Dependencies {
	return Dependencies{
		Audit: func(context.Context, string) (layout.Report, error) { return layout.Report{}, nil },
		Clean: func(context.Context, string, layout.CleanOptions) (layout.CleanReport, error) {
			return layout.CleanReport{}, nil
		},
		Migrate: func(context.Context, string, layout.MigrateOptions) (layout.MigrateReport, error) {
			return layout.MigrateReport{}, nil
		},
	}
}
func execute(command *cobra.Command, args ...string) (string, error) {
	var out bytes.Buffer
	command.SilenceUsage, command.SilenceErrors = true, true
	command.SetOut(&out)
	command.SetErr(io.Discard)
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), err
}
func TestFlagsReachOperationsAfterParsing(t *testing.T) {
	t.Parallel()
	for _, leaf := range []string{"audit", "clean", "migrate"} {
		t.Run(leaf, func(t *testing.T) {
			t.Parallel()
			deps := testDependencies()
			root := "before"
			calls := 0
			check := func(ctx context.Context, got string) {
				t.Helper()
				calls++
				if got != "after" || ctx.Value(contextKey("key")) != "value" {
					t.Fatalf("root/context=%q/%v", got, ctx)
				}
			}
			deps.Audit = func(ctx context.Context, root string) (layout.Report, error) {
				check(ctx, root)
				return layout.Report{}, nil
			}
			deps.Clean = func(ctx context.Context, root string, o layout.CleanOptions) (layout.CleanReport, error) {
				check(ctx, root)
				if !o.Apply || !o.AllowMissingCanonical {
					t.Fatalf("options=%+v", o)
				}
				return layout.CleanReport{}, nil
			}
			deps.Migrate = func(ctx context.Context, root string, o layout.MigrateOptions) (layout.MigrateReport, error) {
				check(ctx, root)
				if !o.Apply || !o.ClonesOnly || !o.IncludeActiveTasks || o.UndoID != "id" || fmt.Sprint(o.Repositories) != "[acme/app]" || fmt.Sprint(o.IncludeTasks) != "[one two]" {
					t.Fatalf("options=%+v", o)
				}
				return layout.MigrateReport{}, nil
			}
			cmd := New(testRuntime(func() string { return root }), deps)
			cmd.PersistentFlags().StringVar(&root, "projects-root", root, "")
			cmd.SetContext(context.WithValue(context.Background(), contextKey("key"), "value"))
			args := []string{leaf, "--projects-root=after", "--format=json"}
			if leaf == "clean" {
				args = append(args, "--apply", "--allow-missing-canonical")
			}
			if leaf == "migrate" {
				args = append(args, "acme/app", "--apply", "--clones-only", "--undo=id", "--include-active-tasks", "--include-task=one", "--include-task=two")
			}
			if _, err := execute(cmd, args...); err != nil || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}
func TestCommandsPreserveDelegatedErrorsAndCancellation(t *testing.T) {
	t.Parallel()
	for _, leaf := range []string{"audit", "clean", "migrate"} {
		t.Run(leaf, func(t *testing.T) {
			t.Parallel()
			deps := testDependencies()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			deps.Audit = func(ctx context.Context, _ string) (layout.Report, error) { return layout.Report{}, ctx.Err() }
			deps.Clean = func(ctx context.Context, _ string, _ layout.CleanOptions) (layout.CleanReport, error) {
				return layout.CleanReport{}, ctx.Err()
			}
			deps.Migrate = func(ctx context.Context, _ string, _ layout.MigrateOptions) (layout.MigrateReport, error) {
				return layout.MigrateReport{}, ctx.Err()
			}
			cmd := New(testRuntime(func() string { return "root" }), deps)
			cmd.SetContext(ctx)
			if _, err := execute(cmd, leaf); !errors.Is(err, context.Canceled) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
func TestInvalidArgumentsDoNotCallOperations(t *testing.T) {
	t.Parallel()
	for _, leaf := range []string{"audit", "clean", "migrate"} {
		t.Run(leaf, func(t *testing.T) {
			t.Parallel()
			deps := testDependencies()
			called := false
			deps.Audit = func(context.Context, string) (layout.Report, error) { called = true; return layout.Report{}, nil }
			deps.Clean = func(context.Context, string, layout.CleanOptions) (layout.CleanReport, error) {
				called = true
				return layout.CleanReport{}, nil
			}
			deps.Migrate = func(context.Context, string, layout.MigrateOptions) (layout.MigrateReport, error) {
				called = true
				return layout.MigrateReport{}, nil
			}
			args := []string{leaf, "extra"}
			if leaf == "migrate" {
				args = []string{leaf, "--format=toml"}
			}
			if _, err := execute(New(testRuntime(func() string { return "root" }), deps), args...); err == nil || called {
				t.Fatalf("called=%t err=%v", called, err)
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestOutputFailureAndPartialMigrationJoin(t *testing.T) {
	t.Parallel()
	for _, leaf := range []string{"audit", "clean", "migrate"} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", leaf, partial), func(t *testing.T) {
				t.Parallel()
				deps := testDependencies()
				want := errors.New("manifest failure")
				if partial {
					deps.Migrate = func(context.Context, string, layout.MigrateOptions) (layout.MigrateReport, error) {
						return layout.MigrateReport{SchemaVersion: 1}, want
					}
				}
				cmd := New(testRuntime(func() string { return "root" }), deps)
				cmd.SetOut(failingWriter{})
				cmd.SetErr(io.Discard)
				cmd.SilenceUsage = true
				cmd.SilenceErrors = true
				cmd.SetArgs([]string{leaf})
				err := cmd.Execute()
				if !errors.Is(err, io.ErrClosedPipe) {
					t.Fatalf("error=%v", err)
				}
				if partial && leaf == "migrate" && !errors.Is(err, want) {
					t.Fatalf("lost manifest error:%v", err)
				}
			})
		}
	}
}
func TestReportDirectoryFailureStopsEachCommand(t *testing.T) {
	t.Parallel()
	for _, leaf := range []string{"audit", "clean", "migrate"} {
		t.Run(leaf, func(t *testing.T) {
			t.Parallel()
			blocked := filepath.Join(t.TempDir(), "blocked")
			if err := os.WriteFile(blocked, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := execute(New(testRuntime(func() string { return "root" }), testDependencies()), leaf, "--report-dir="+filepath.Join(blocked, "child")); err == nil {
				t.Fatal("expected report failure")
			}
		})
	}
}
func TestAuditAndCleanFindings(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	deps.Audit = func(context.Context, string) (layout.Report, error) {
		return layout.Report{Summary: layout.Summary{TopLevel: 1}}, nil
	}
	deps.Clean = func(context.Context, string, layout.CleanOptions) (layout.CleanReport, error) {
		return layout.CleanReport{Actions: []layout.CleanAction{{Status: "error"}}}, nil
	}
	for _, leaf := range []string{"audit", "clean"} {
		out, err := execute(New(testRuntime(func() string { return "root" }), deps), leaf)
		var coded *codedError
		if !errors.As(err, &coded) || coded.code != 1 || strings.TrimSpace(out) == "" {
			t.Fatalf("%s output=%q err=%v", leaf, out, err)
		}
	}
}
func TestInstancesKeepTheirOwnFlagValues(t *testing.T) {
	t.Parallel()
	for _, apply := range []bool{true, false} {
		t.Run(fmt.Sprint(apply), func(t *testing.T) {
			t.Parallel()
			deps := testDependencies()
			deps.Clean = func(_ context.Context, _ string, o layout.CleanOptions) (layout.CleanReport, error) {
				if o.Apply != apply {
					t.Fatalf("apply=%t", o.Apply)
				}
				return layout.CleanReport{}, nil
			}
			args := []string{"clean"}
			if apply {
				args = append(args, "--apply")
			}
			if _, err := execute(New(testRuntime(func() string { return "root" }), deps), args...); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCommandsDelegateDefaultOptions(t *testing.T) {
	t.Parallel()
	for _, leaf := range []string{"clean", "migrate"} {
		t.Run(leaf, func(t *testing.T) {
			t.Parallel()
			deps := testDependencies()
			calls := 0
			deps.Clean = func(_ context.Context, _ string, options layout.CleanOptions) (layout.CleanReport, error) {
				calls++
				if options != (layout.CleanOptions{}) {
					t.Fatalf("default clean options = %+v", options)
				}
				return layout.CleanReport{}, nil
			}
			deps.Migrate = func(_ context.Context, _ string, options layout.MigrateOptions) (layout.MigrateReport, error) {
				calls++
				expected := layout.MigrateOptions{Repositories: options.Repositories}
				if len(options.Repositories) != 0 || !reflect.DeepEqual(options, expected) {
					t.Fatalf("default migrate options = %+v", options)
				}
				return layout.MigrateReport{}, nil
			}
			if _, err := execute(New(testRuntime(func() string { return "root" }), deps), leaf); err != nil || calls != 1 {
				t.Fatalf("calls = %d, error = %v", calls, err)
			}
		})
	}
}

func testRuntime(projectsRoot func() string) shared.Runtime {
	return shared.Runtime{
		Flags:     func() shared.Flags { return shared.Flags{ProjectsRoot: projectsRoot()} },
		ExitError: func(code int, message string) error { return &codedError{code, message} },
	}
}
