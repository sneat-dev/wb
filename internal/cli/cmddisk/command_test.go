package cmddisk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/disk"
)

type codedError struct {
	code    int
	message string
}

func (err *codedError) Error() string { return err.message }

func runtimeFor(root string) shared.Runtime {
	return shared.Runtime{
		Flags:     func() shared.Flags { return shared.Flags{ProjectsRoot: root} },
		ExitError: func(code int, message string) error { return &codedError{code, message} },
	}
}

func execute(command *cobra.Command, out io.Writer, args ...string) error {
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetOut(out)
	command.SetErr(io.Discard)
	command.SetArgs(args)
	return command.Execute()
}

func ampleReport() disk.Report {
	return disk.Report{
		Filesystem:      disk.Filesystem{Path: "/fixture", TotalBytes: 100, UsedBytes: 20, AvailableBytes: 80},
		Categories:      []disk.Category{{Name: "shared Go cache", Kind: "cache", Roots: []string{"/cache"}, ApparentBytes: 5, UnsharedBytes: 3}},
		AttributedBytes: 3,
	}
}

func TestReportFormatsPreserveTheCollectedReport(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"text", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			report := ampleReport()
			var out bytes.Buffer
			calls := 0
			command := New(runtimeFor("/projects"), Dependencies{Collect: func(ctx context.Context, options disk.Options) (disk.Report, error) {
				calls++
				if options.ProjectsRoot != "/projects" || !options.SkipSizes || options.MinimumAvailableRatio != disk.DefaultMinimumAvailableRatio || options.WBHome != "" || options.FilesystemProbe != nil {
					t.Fatalf("options = %+v", options)
				}
				return report, nil
			}})
			args := []string{"--skip-sizes"}
			if format != "text" {
				args = append(args, "--format", format)
			}
			if err := execute(command, &out, args...); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("Collect calls = %d", calls)
			}
			switch format {
			case "text":
				if out.String() != disk.Render(report) {
					t.Fatalf("text = %q", out.String())
				}
				if !strings.Contains(out.String(), "RECLAIM") || !strings.Contains(out.String(), "APPARENT") {
					t.Fatal("missing category headings")
				}
			case "json":
				var got disk.Report
				if err := json.Unmarshal(out.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, report) {
					t.Fatalf("report = %+v", got)
				}
				expected, err := json.MarshalIndent(report, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if out.String() != string(expected)+"\n" {
					t.Fatalf("JSON bytes = %q", out.String())
				}
			case "yaml":
				var got disk.Report
				if err := yaml.Unmarshal(out.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, report) {
					t.Fatalf("report = %+v", got)
				}
				var expected bytes.Buffer
				encoder := yaml.NewEncoder(&expected)
				if err := encoder.Encode(report); err != nil {
					t.Fatal(err)
				}
				if err := encoder.Close(); err != nil {
					t.Fatal(err)
				}
				if out.String() != expected.String() {
					t.Fatalf("YAML bytes = %q", out.String())
				}
			}
		})
	}
}

func TestInvalidOptionsRefuseBeforeCollection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		args    []string
		message string
	}{
		{"format", []string{"--format", "toml"}, `unsupported --format "toml": use text, json, or yaml`},
		{"negative minimum", []string{"--minimum-available", "-0.1"}, "--minimum-available must be between 0 and 1"},
		{"large minimum", []string{"--minimum-available", "2"}, "--minimum-available must be between 0 and 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			command := New(runtimeFor("/projects"), Dependencies{Collect: func(context.Context, disk.Options) (disk.Report, error) { calls++; return disk.Report{}, nil }})
			var out bytes.Buffer
			err := execute(command, &out, tc.args...)
			var coded *codedError
			if !errors.As(err, &coded) || coded.code != shared.ExitUsage || coded.message != tc.message {
				t.Fatalf("error = %#v", err)
			}
			if calls != 0 || out.Len() != 0 {
				t.Fatalf("calls = %d, output = %q", calls, out.String())
			}
		})
	}
}

func TestArgumentsAndUnknownFlagsRefuseBeforeCollection(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"unexpected"}, {"--unknown"}, {"--minimum-available", "invalid"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			command := New(runtimeFor("/projects"), Dependencies{Collect: func(context.Context, disk.Options) (disk.Report, error) {
				t.Fatal("collection must not run")
				return disk.Report{}, nil
			}})
			if err := execute(command, io.Discard, args...); err == nil {
				t.Fatal("expected argument refusal")
			}
		})
	}
}

func TestCollectorReceivesContextAndPreservesItsError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	want := errors.New("collection unavailable")
	command := New(runtimeFor("/projects"), Dependencies{Collect: func(got context.Context, options disk.Options) (disk.Report, error) {
		if got != ctx {
			t.Fatal("context changed")
		}
		if options.MinimumAvailableRatio != 0.25 || options.SkipSizes {
			t.Fatalf("options = %+v", options)
		}
		return disk.Report{}, want
	}})
	command.SetContext(ctx)
	var out bytes.Buffer
	if err := execute(command, &out, "--minimum-available", "0.25"); err != want {
		t.Fatalf("error = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("output = %q", out.String())
	}
}

func TestFindingsRenderBeforeReturningTheRuntimeError(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"text", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			report := ampleReport()
			report.Findings = []string{"low headroom"}
			want := errors.New("coded finding")
			runtime := runtimeFor("/projects")
			runtime.ExitError = func(code int, message string) error {
				if code != shared.ExitFindings || message != "wb disk reported findings; see the report above" {
					t.Fatalf("code/message = %d/%q", code, message)
				}
				return want
			}
			command := New(runtime, Dependencies{Collect: func(context.Context, disk.Options) (disk.Report, error) { return report, nil }})
			var out bytes.Buffer
			if err := execute(command, &out, "--format", format); err != want {
				t.Fatalf("error = %v", err)
			}
			if !strings.Contains(out.String(), "low headroom") {
				t.Fatalf("report = %q", out.String())
			}
		})
	}
}

type failingWriter struct{ err error }

func (writer failingWriter) Write([]byte) (int, error) { return 0, writer.err }

func TestReportWriteFailuresPrecedeFindings(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"text", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			want := errors.New("disk output unavailable")
			runtime := runtimeFor("/projects")
			runtime.ExitError = func(int, string) error { t.Fatal("write failure must precede findings"); return nil }
			report := ampleReport()
			report.Findings = []string{"low headroom"}
			command := New(runtime, Dependencies{Collect: func(context.Context, disk.Options) (disk.Report, error) { return report, nil }})
			err := execute(command, failingWriter{want}, "--format", format)
			if format == "yaml" {
				// yaml.v3 wraps its writer diagnostic rather than preserving error identity.
				if err == nil || !strings.Contains(err.Error(), want.Error()) {
					t.Fatalf("error = %v", err)
				}
			} else if err != want {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestMinimumBoundaryValuesReachTheOperation(t *testing.T) {
	t.Parallel()
	for _, minimum := range []string{"0", "1"} {
		t.Run(minimum, func(t *testing.T) {
			t.Parallel()
			calls := 0
			command := New(runtimeFor("/projects"), Dependencies{Collect: func(_ context.Context, options disk.Options) (disk.Report, error) {
				calls++
				if minimum == "0" && options.MinimumAvailableRatio != 0 || minimum == "1" && options.MinimumAvailableRatio != 1 {
					t.Fatalf("minimum = %v", options.MinimumAvailableRatio)
				}
				return ampleReport(), nil
			}})
			if err := execute(command, io.Discard, "--minimum-available", minimum); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls = %d", calls)
			}
		})
	}
}

func TestHelpDescribesAccountingAndDefaultsWithoutCollecting(t *testing.T) {
	t.Parallel()
	command := New(runtimeFor("/projects"), Dependencies{Collect: func(context.Context, disk.Options) (disk.Report, error) {
		t.Fatal("help collected")
		return disk.Report{}, nil
	}})
	var out bytes.Buffer
	if err := execute(command, &out, "--help"); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"RECLAIM", "APPARENT", "Nothing reported the growth", "nothing here deletes anything", "Exit codes: 0 nothing to flag, 1 findings, 2 usage.", "--format", "--skip-sizes", "--minimum-available"} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("help missing %q", text)
		}
	}
	if got := command.Flags().Lookup("format").DefValue; got != "text" {
		t.Fatalf("format default = %s", got)
	}
	if got := command.Flags().Lookup("minimum-available").DefValue; got != "0.1" {
		t.Fatalf("minimum default = %s", got)
	}
}

func TestInvocationsReadParsedFlagsAndKeepLocalOptionsSeparate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		projects string
		args     []string
		skip     bool
		minimum  float64
	}{
		{"first", "/first", []string{"--skip-sizes", "--minimum-available", "0.25"}, true, 0.25},
		{"second", "/second", nil, false, disk.DefaultMinimumAvailableRatio},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			projects := "/before-parsing"
			reads, calls := 0, 0
			runtime := runtimeFor(projects)
			runtime.Flags = func() shared.Flags {
				reads++
				return shared.Flags{ProjectsRoot: projects}
			}
			command := New(runtime, Dependencies{Collect: func(_ context.Context, options disk.Options) (disk.Report, error) {
				calls++
				if options.ProjectsRoot != tc.projects || options.SkipSizes != tc.skip || options.MinimumAvailableRatio != tc.minimum {
					t.Fatalf("options = %+v", options)
				}
				return ampleReport(), nil
			}})
			if reads != 0 {
				t.Fatal("invocation flags read before parsing")
			}
			root := &cobra.Command{Use: "wb"}
			root.PersistentFlags().StringVar(&projects, "projects-root", projects, "fixture root")
			root.AddCommand(command)
			args := append([]string{"disk", "--projects-root", tc.projects}, tc.args...)
			if err := execute(root, io.Discard, args...); err != nil {
				t.Fatal(err)
			}
			if reads != 1 || calls != 1 {
				t.Fatalf("flag reads / operation calls = %d / %d", reads, calls)
			}
		})
	}
}
