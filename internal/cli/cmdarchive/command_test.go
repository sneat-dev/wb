package cmdarchive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/archiveprune"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type contextKey struct{}

type codedError struct {
	code    int
	message string
}

func (e *codedError) Error() string { return e.message }
func testRuntime() shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: "projects", Filter: "acme"} }, ExitError: func(code int, message string) error { return &codedError{code, message} }}
}
func execute(t *testing.T, runtime shared.Runtime, clean func(context.Context, archiveprune.Options) (archiveprune.Outcome, error), args ...string) (string, error) {
	t.Helper()
	cmd := New(runtime, Dependencies{Clean: clean})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs(append([]string{"clean"}, args...))
	err := cmd.Execute()
	return out.String(), err
}
func sampleOutcome() archiveprune.Outcome {
	return archiveprune.Outcome{Apply: true, Results: []archiveprune.Result{
		{Repository: "acme/deleted", Applied: true, Reason: "archived and clean"},
		{Repository: "acme/would", Eligible: true, Reason: "archived and clean"},
		{Repository: "acme/skipped", Reason: "unpushed commits"},
		{Repository: "acme/broken", Eligible: true, Error: "permission denied"},
		{Repository: "acme/untracked", Reason: "contains untracked files", Untracked: []archiveprune.UntrackedEntry{{Kind: "file", Path: "notes.txt", Size: 12}}, ReceiptPath: "/tmp/receipt.json"},
	}}
}

const sampleLines = "  deleted      acme/deleted — archived and clean\n  would delete acme/would — archived and clean\n  skipped      acme/skipped — unpushed commits\n  failed       acme/broken — eligible but deletion failed: permission denied\n  skipped      acme/untracked — contains untracked files\n    untracked file notes.txt (12 bytes)\n    receipt /tmp/receipt.json\n\n"

func TestTextReportsEveryResultAndTotals(t *testing.T) {
	t.Parallel()
	for _, apply := range []bool{false, true} {
		t.Run(fmt.Sprint(apply), func(t *testing.T) {
			t.Parallel()
			outcome := sampleOutcome()
			outcome.Apply = apply
			var out bytes.Buffer
			if err := writeText(&out, outcome); err != nil {
				t.Fatal(err)
			}
			total := "1 eligible, 2 skipped; dry-run only, pass --apply to delete\n"
			if apply {
				total = "1 deleted, 2 skipped\n"
			}
			if got := out.String(); got != sampleLines+total {
				t.Fatalf("text=%q", got)
			}
		})
	}
}
func TestFormatsPreserveBytesAndFindingsIdentity(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"text", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			outcome := sampleOutcome()
			text, err := execute(t, testRuntime(), func(context.Context, archiveprune.Options) (archiveprune.Outcome, error) { return outcome, nil }, "--format", format)
			var coded *codedError
			if !errors.As(err, &coded) || coded.code != shared.ExitFindings || coded.message != "archive clean reported errors; see the report above" {
				t.Fatalf("error=%v", err)
			}
			var expected bytes.Buffer
			switch format {
			case "text":
				expected.WriteString(sampleLines + "1 deleted, 2 skipped\n")
			case "yaml":
				raw, e := yaml.Marshal(outcome)
				if e != nil {
					t.Fatal(e)
				}
				expected.Write(raw)
			case "json":
				enc := json.NewEncoder(&expected)
				enc.SetIndent("", "  ")
				if e := enc.Encode(outcome); e != nil {
					t.Fatal(e)
				}
			}
			if text != expected.String() {
				t.Fatalf("output=%q want=%q", text, expected.String())
			}
		})
	}
}
func TestEmptyAndRefusedPlansSucceed(t *testing.T) {
	t.Parallel()
	for _, outcome := range []archiveprune.Outcome{{}, {Results: []archiveprune.Result{{Eligible: true, Reason: "archived and clean"}, {Reason: "untracked files"}}}} {
		text, err := execute(t, testRuntime(), func(context.Context, archiveprune.Options) (archiveprune.Outcome, error) { return outcome, nil })
		if err != nil {
			t.Fatal(err)
		}
		if len(outcome.Results) == 0 && text != "no local clones matched\n" {
			t.Fatalf("empty=%q", text)
		}
		if len(outcome.Results) > 0 && !strings.Contains(text, "1 eligible, 1 skipped; dry-run only") {
			t.Fatalf("plan=%q", text)
		}
	}
}
func TestArgumentsAreRefusedBeforeClean(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"extra"}, {"--format", "toml"}, {"--apply=invalid"}, {"--unknown"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			t.Parallel()
			calls := 0
			text, err := execute(t, testRuntime(), func(context.Context, archiveprune.Options) (archiveprune.Outcome, error) {
				calls++
				return archiveprune.Outcome{}, nil
			}, args...)
			if err == nil || calls != 0 || text != "" {
				t.Fatalf("error=%v calls=%d out=%q", err, calls, text)
			}
			if args[0] == "--format" && err.Error() != `unsupported format "toml"; use text or yaml or json` {
				t.Fatalf("format=%v", err)
			}
		})
	}
}
func TestCleanReceivesContextOptionsAndProgress(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"--delete-untracked"}, {"--apply", "--delete-untracked"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			t.Parallel()
			cmd := New(testRuntime(), Dependencies{Clean: func(ctx context.Context, opts archiveprune.Options) (archiveprune.Outcome, error) {
				if ctx.Value(contextKey{}) != "context" {
					t.Fatal("context lost")
				}
				if opts.ProjectsRoot != "projects" || opts.Filter != "acme" || opts.Apply != (len(args) == 2) || opts.DeleteUntracked != (len(args) > 0) {
					t.Fatalf("options=%+v", opts)
				}
				if _, err := fmt.Fprint(opts.Progress, "progress"); err != nil {
					t.Fatal(err)
				}
				return archiveprune.Outcome{}, nil
			}})
			var out, progress bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&progress)
			cmd.SetContext(context.WithValue(context.Background(), contextKey{}, "context"))
			cmd.SetArgs(append([]string{"clean"}, args...))
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if progress.String() != "progress" {
				t.Fatalf("progress=%q", progress.String())
			}
		})
	}
}
func TestCleanErrorsPassThroughWithoutOutput(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("discovery failed")
	text, err := execute(t, testRuntime(), func(context.Context, archiveprune.Options) (archiveprune.Outcome, error) {
		return sampleOutcome(), sentinel
	})
	if err != sentinel || text != "" {
		t.Fatalf("err=%v out=%q", err, text)
	}
}

type failingWriter struct {
	failAt, calls int
	err           error
}

func (w *failingWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls >= w.failAt {
		return 0, w.err
	}
	return len(p), nil
}
func TestTextReturnsFirstFailureAndStopsWriting(t *testing.T) {
	t.Parallel()
	for _, outcome := range []archiveprune.Outcome{{}, sampleOutcome()} {
		for failAt := 1; failAt <= 9; failAt++ {
			if len(outcome.Results) == 0 && failAt > 1 {
				break
			}
			w := &failingWriter{failAt: failAt, err: errors.New("broken pipe")}
			if err := writeText(w, outcome); err != w.err || w.calls != failAt {
				t.Fatalf("failure %d error=%v calls=%d", failAt, err, w.calls)
			}
		}
	}
}
func TestAllFormatsReturnWriterFailureBeforeFindings(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"text", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			w := &failingWriter{failAt: 1, err: errors.New("broken pipe")}
			cmd := New(testRuntime(), Dependencies{Clean: func(context.Context, archiveprune.Options) (archiveprune.Outcome, error) { return sampleOutcome(), nil }})
			cmd.SetOut(w)
			cmd.SetErr(io.Discard)
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			cmd.SetArgs([]string{"clean", "--format", format})
			if err := cmd.Execute(); err != w.err {
				t.Fatalf("writer error=%v", err)
			}
		})
	}
}
func TestInvocationsReadParsedFlagsAndKeepOptionsSeparate(t *testing.T) {
	t.Parallel()
	for _, apply := range []bool{false, true} {
		t.Run(fmt.Sprint(apply), func(t *testing.T) {
			t.Parallel()
			var projects, filter string
			reads, calls := 0, 0
			runtime := testRuntime()
			runtime.Flags = func() shared.Flags { reads++; return shared.Flags{ProjectsRoot: projects, Filter: filter} }
			root := &cobra.Command{Use: "wb"}
			root.PersistentFlags().StringVar(&projects, "projects-root", "before", "root")
			root.PersistentFlags().StringVar(&filter, "filter", "before", "filter")
			root.AddCommand(New(runtime, Dependencies{Clean: func(_ context.Context, opts archiveprune.Options) (archiveprune.Outcome, error) {
				calls++
				if opts.ProjectsRoot != "after" || opts.Filter != "parsed" || opts.Apply != apply {
					t.Fatalf("options=%+v", opts)
				}
				return archiveprune.Outcome{}, nil
			}}))
			if reads != 0 {
				t.Fatal("Flags read before parsing")
			}
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			args := []string{"archive", "clean", "--projects-root", "after", "--filter", "parsed"}
			if apply {
				args = append(args, "--apply")
			}
			root.SetArgs(args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if reads != 1 || calls != 1 {
				t.Fatalf("reads=%d calls=%d", reads, calls)
			}
		})
	}
}
func TestHelpPreservesAuthorizationAndDefaults(t *testing.T) {
	t.Parallel()
	cmd := New(testRuntime(), Dependencies{Clean: func(context.Context, archiveprune.Options) (archiveprune.Outcome, error) {
		t.Fatal("help called Clean")
		return archiveprune.Outcome{}, nil
	}})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"clean", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"default is a dry-run plan", "--apply", "--delete-untracked", "exact itemized paths", "--format string", "(default \"text\")"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help missing %q", want)
		}
	}
}
