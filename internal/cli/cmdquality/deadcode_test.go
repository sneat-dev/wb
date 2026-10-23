package cmdquality

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/quality"
)

func deadcodeTestCommand(t *testing.T, analyze func(context.Context, string, quality.DeadcodeOptions) (quality.DeadcodeReport, error), out *bytes.Buffer, args ...string) *cobra.Command {
	deps := testDependencies(t)
	deps.Analyze = analyze
	command := NewDeadcode(testRuntime(), deps)
	command.SilenceUsage = true
	command.SetOut(out)
	command.SetErr(&bytes.Buffer{})
	command.SetArgs(args)
	return command
}

func TestDeadcodeCommandRejectsInvalidOptionsBeforeAnalysis(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, want string
		args       []string
	}{
		{"conflicting baseline flags", "mutually exclusive", []string{"--no-baseline", "--update-baseline"}},
		{"unsupported format", "unsupported --format", []string{"--format", "xml"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			called := false
			command := deadcodeTestCommand(t, func(context.Context, string, quality.DeadcodeOptions) (quality.DeadcodeReport, error) {
				called = true
				return quality.DeadcodeReport{}, nil
			}, new(bytes.Buffer), tc.args...)
			err := command.Execute()
			var exit *testExitError
			if !errors.As(err, &exit) || exit.code != exitUsage || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Execute() error = %v, want usage error containing %q", err, tc.want)
			}
			if called {
				t.Fatal("analyzer ran after invalid options")
			}
		})
	}
}

func TestDeadcodeCommandReportsNewFindingsWithExactAnalyzerOptions(t *testing.T) {
	t.Parallel()
	repository := "/fixture/repository"
	const identity = "example.com/repo/pkg.Unreachable"
	finding := quality.DeadcodeFinding{Identity: identity, File: "pkg/a.go", Line: 17}
	var gotPath string
	var gotOptions quality.DeadcodeOptions
	var output bytes.Buffer
	command := deadcodeTestCommand(t, func(_ context.Context, path string, options quality.DeadcodeOptions) (quality.DeadcodeReport, error) {
		gotPath, gotOptions = path, options
		return quality.DeadcodeReport{Findings: []quality.DeadcodeFinding{finding}, New: []quality.DeadcodeFinding{finding}, BaselinePath: "baseline.txt"}, nil
	}, &output, repository, "--baseline", "baseline.txt", "--filter", "pkg", "--generated", "--packages", "./cmd/...", "--timeout", "2s", "--format", "json")
	err := command.Execute()
	var exit *testExitError
	if !errors.As(err, &exit) || exit.code != exitFindings || !strings.Contains(err.Error(), "1 function(s)") {
		t.Fatalf("new finding error = %v, want findings exit", err)
	}
	if gotPath != repository || gotOptions.BaselinePath != "baseline.txt" || gotOptions.Filter != "pkg" || !gotOptions.IncludeGenerated || gotOptions.Timeout != 2*time.Second || len(gotOptions.Patterns) != 1 || gotOptions.Patterns[0] != "./cmd/..." {
		t.Fatalf("analyzer received path %q and options %#v", gotPath, gotOptions)
	}
	var decoded quality.DeadcodeReport
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil || len(decoded.New) != 1 || decoded.New[0].Identity != identity {
		t.Fatalf("JSON output = %q, decoded %#v, error %v", output.String(), decoded, err)
	}
}

func TestDeadcodeCommandWithoutBaselineRendersYAMLWithoutGating(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	command := deadcodeTestCommand(t, func(_ context.Context, _ string, options quality.DeadcodeOptions) (quality.DeadcodeReport, error) {
		if options.BaselinePath != "" {
			t.Fatalf("--no-baseline passed %q to analyzer", options.BaselinePath)
		}
		return quality.DeadcodeReport{Findings: []quality.DeadcodeFinding{{Identity: "example.com/repo/pkg.Helper"}}}, nil
	}, &output, "--no-baseline", "--format", "yaml")
	if err := command.Execute(); err != nil {
		t.Fatalf("unbaselined report failed: %v", err)
	}
	var report quality.DeadcodeReport
	if err := yaml.Unmarshal(output.Bytes(), &report); err != nil || len(report.Findings) != 1 || report.Findings[0].Identity != "example.com/repo/pkg.Helper" {
		t.Fatalf("YAML output = %q, decoded %#v, error %v", output.String(), report, err)
	}
}

func TestDeadcodeCommandUpdatesBaselineFromAnalyzerFindings(t *testing.T) {
	t.Parallel()
	const repository = "/fixture/repository"
	const identity = "example.com/repo/pkg.Helper"
	var output bytes.Buffer
	deps := testDependencies(t)
	deps.Analyze = func(_ context.Context, path string, options quality.DeadcodeOptions) (quality.DeadcodeReport, error) {
		if path != repository || options.BaselinePath != ".wb/deadcode-baseline.txt" {
			t.Fatalf("analyzer received %q %#v", path, options)
		}
		return quality.DeadcodeReport{Findings: []quality.DeadcodeFinding{{Identity: identity}}}, nil
	}
	writes := 0
	deps.WriteDeadcodeBaseline = func(path string, findings []quality.DeadcodeFinding) error {
		writes++
		if path != filepath.Join(repository, ".wb", "deadcode-baseline.txt") || len(findings) != 1 || findings[0].Identity != identity {
			t.Fatalf("baseline request %q %+v", path, findings)
		}
		return nil
	}
	cmd := NewDeadcode(testRuntime(), deps)
	cmd.SetOut(&output)
	cmd.SetArgs([]string{repository, "--update-baseline"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if writes != 1 || !strings.Contains(output.String(), "recorded 1 unreachable function(s)") {
		t.Fatalf("writes=%d output=%q", writes, output.String())
	}
}

func TestDeadcodeCommandPreservesAnalyzerError(t *testing.T) {
	t.Parallel()
	boom := errors.New("analyzer unavailable")
	command := deadcodeTestCommand(t, func(context.Context, string, quality.DeadcodeOptions) (quality.DeadcodeReport, error) {
		return quality.DeadcodeReport{}, boom
	}, new(bytes.Buffer))
	if err := command.Execute(); !errors.Is(err, boom) {
		t.Fatalf("Execute() error = %v, want analyzer failure", err)
	}
}

func TestDeadcodeTextReportShowsNewFixedAndMissingBaselineEvidence(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	command := &cobra.Command{Use: "deadcode"}
	command.SetOut(&output)
	report := quality.DeadcodeReport{
		Findings: []quality.DeadcodeFinding{{Identity: "example.com/repo/pkg.New"}},
		New:      []quality.DeadcodeFinding{{Identity: "example.com/repo/pkg.New", File: "pkg/new.go", Line: 21}},
		Fixed:    []string{"example.com/repo/pkg.Fixed"}, BaselinePath: ".wb/deadcode-baseline.txt", BaselineMissing: true,
	}
	if err := writeDeadcodeReport(command, "text", report); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"New unreachable functions (1)", "pkg/new.go:21: example.com/repo/pkg.New", "Baseline entries now reachable or gone (1)", "example.com/repo/pkg.Fixed", "No baseline at .wb/deadcode-baseline.txt"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("text report missing %q: %q", want, output.String())
		}
	}
}

func TestDeadcodeTextReportExplainsCleanBaselineAndPropagatesWriteFailure(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{Use: "deadcode"}
	var output bytes.Buffer
	command.SetOut(&output)
	if err := writeDeadcodeReport(command, "text", quality.DeadcodeReport{Findings: []quality.DeadcodeFinding{{Identity: "existing"}}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "1 unreachable function(s), all baselined; nothing new") {
		t.Fatalf("clean baseline report = %q", output.String())
	}
	boom := errors.New("output closed")
	command.SetOut(failingWriter{err: boom})
	if err := writeDeadcodeReport(command, "text", quality.DeadcodeReport{}); !errors.Is(err, boom) {
		t.Fatalf("write error = %v, want %v", err, boom)
	}
}
