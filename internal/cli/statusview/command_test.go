package statusview

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/reposelection"
	"github.com/sneat-dev/wb/internal/repostatus"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type codedError struct {
	code    int
	message string
}

func (e *codedError) Error() string { return e.message }
func testRuntime(flags *shared.Flags) shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return *flags }, ExitError: func(code int, message string) error { return &codedError{code, message} }}
}
func fakeDependencies() Dependencies {
	return Dependencies{
		Collect: func(_ reposelection.Request, observer repostatus.Observer) (repostatus.Index, error) {
			observer.Start(1)
			row := repostatus.Row{Repository: "acme/repo", Status: "clean"}
			observer.Complete(reposelection.Target{Repository: row.Repository}, row)
			return repostatus.Index{SchemaVersion: 1, Repositories: []repostatus.Row{row}}, nil
		},
		WriteReports: func(repostatus.Index, string, bool, string) error { return nil }, Interactive: func(io.Writer, bool) bool { return false },
	}
}
func execute(command *cobra.Command, args ...string) (out, stderr string, err error) {
	var output, progress bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&progress)
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetArgs(args)
	err = command.Execute()
	return output.String(), progress.String(), err
}

type failedWriter struct{ err error }

func (w failedWriter) Write([]byte) (int, error) { return 0, w.err }

func TestStatusConstructorsPreserveFullDefaultRequestsAndLazyFlags(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name                string
		new                 func(shared.Runtime, Dependencies) *cobra.Command
		args                []string
		fleet               bool
		path, filter, title string
	}{
		{"repository", NewRepository, nil, false, ".", "", "# WB repository status"},
		{"repository explicit", NewRepository, []string{"/repo"}, false, "/repo", "", "# WB repository status"},
		{"historical fleet", NewHistorical, nil, true, ".", "parsed-filter", "# WB local repository status"},
		{"historical explicit", NewHistorical, []string{"/repo"}, false, "/repo", "parsed-filter", "# WB repository status"},
		{"fleet", NewFleet, nil, true, ".", "parsed-filter", "# WB fleet status"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			flags := shared.Flags{ProjectsRoot: "before", Filter: "before"}
			deps := fakeDependencies()
			var actual reposelection.Request
			deps.Collect = func(request reposelection.Request, observer repostatus.Observer) (repostatus.Index, error) {
				actual = request
				return repostatus.Index{SchemaVersion: 1, Repositories: []repostatus.Row{{Repository: "clean", Status: "clean"}, {Repository: "dirty", Status: "attention"}}}, nil
			}
			cmd := test.new(testRuntime(&flags), deps)
			cmd.PersistentFlags().StringVar(&flags.ProjectsRoot, "projects-root", "before", "")
			cmd.PersistentFlags().StringVar(&flags.Filter, "filter", "before", "")
			flags.NonInteractive = true
			args := append(append([]string(nil), test.args...), "--projects-root", "parsed-root", "--filter", "parsed-filter")
			out, progress, err := execute(cmd, args...)
			want := reposelection.Request{Path: test.path, ProjectsRoot: "parsed-root", Filter: test.filter, Fleet: test.fleet, Parallel: 4}
			if err != nil || actual != want {
				t.Fatalf("request=%+v want=%+v err=%v", actual, want, err)
			}
			if !strings.HasPrefix(out, test.title) || progress != "" {
				t.Fatalf("out=%q progress=%q", out, progress)
			}
			if test.fleet && (!strings.Contains(out, "1 clean repository hidden") || strings.Contains(out, "| `clean`")) {
				t.Fatal(out)
			}
			if !test.fleet && !strings.Contains(out, "| `clean`") {
				t.Fatal(out)
			}
		})
	}
}
func TestStatusFlagsReachOptionsReportsAndAllDetails(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{ProjectsRoot: "root", Filter: "acme", NonInteractive: true}
	deps := fakeDependencies()
	var actual reposelection.Request
	var report repostatus.Index
	var directory, title string
	var details bool
	deps.Collect = func(request reposelection.Request, observer repostatus.Observer) (repostatus.Index, error) {
		actual = request
		observer.Start(1)
		row := repostatus.Row{Repository: "clean", Status: "clean", Modified: []string{"a.go"}}
		observer.Complete(reposelection.Target{Repository: "clean"}, row)
		return repostatus.Index{SchemaVersion: 1, Repositories: []repostatus.Row{row}}, nil
	}
	deps.Interactive = func(out io.Writer, forced bool) bool {
		if !forced || out == nil {
			t.Fatal("terminal policy lost")
		}
		return false
	}
	deps.WriteReports = func(value repostatus.Index, dir string, detail bool, heading string) error {
		report = value
		directory = dir
		details = detail
		title = heading
		return nil
	}
	out, _, err := execute(NewFleet(testRuntime(&flags), deps), "--parallel", "2", "--match", "acme/*", "--regex", "repo", "--format", "json", "--report-dir", "reports", "--details", "--all")
	want := reposelection.Request{Path: ".", ProjectsRoot: "root", Filter: "acme", Fleet: true, Parallel: 2, Match: "acme/*", Regex: "repo"}
	if err != nil || actual != want || directory != "reports" || !details || title != "# WB fleet status\n\n" || report.HiddenClean != 0 || len(report.Repositories) != 1 || !strings.Contains(out, `"repository": "clean"`) {
		t.Fatalf("request=%+v report=%+v dir=%q details=%t title=%q out=%q err=%v", actual, report, directory, details, title, out, err)
	}
}
func TestStatusErrorsAndLateReportFormatPrecedence(t *testing.T) {
	t.Parallel()
	operation := errors.New("select")
	write := errors.New("report")
	deps := fakeDependencies()
	deps.Collect = func(reposelection.Request, repostatus.Observer) (repostatus.Index, error) {
		return repostatus.Index{}, operation
	}
	deps.Interactive = func(io.Writer, bool) bool { t.Fatal("interactive before valid selection"); return false }
	out, progress, err := execute(NewHistorical(testRuntime(&shared.Flags{}), deps))
	if err != operation || out != "" || progress != "" {
		t.Fatalf("out=%q progress=%q err=%v", out, progress, err)
	}
	deps = fakeDependencies()
	calls := 0
	deps.WriteReports = func(repostatus.Index, string, bool, string) error { calls++; return write }
	out, _, err = execute(NewFleet(testRuntime(&shared.Flags{}), deps), "--report-dir", "reports", "--format", "toml")
	if err != write || calls != 1 || out != "" {
		t.Fatalf("out=%q calls=%d err=%v", out, calls, err)
	}
	deps.WriteReports = func(repostatus.Index, string, bool, string) error { calls++; return nil }
	out, _, err = execute(NewFleet(testRuntime(&shared.Flags{}), deps), "--report-dir", "reports", "--format", "toml")
	if err == nil || err.Error() != `unknown --format "toml" (want markdown, yaml, or json)` || calls != 2 || out != "" {
		t.Fatalf("late format out=%q calls=%d err=%v", out, calls, err)
	}
}
func TestStatusOutputPrecedesFindingsAndWriterErrorsWin(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	deps.Collect = func(reposelection.Request, repostatus.Observer) (repostatus.Index, error) {
		return repostatus.Index{SchemaVersion: 1, Repositories: []repostatus.Row{{Repository: "broken", Status: "error", Error: "bad Git"}}}, nil
	}
	out, _, err := execute(NewRepository(testRuntime(&shared.Flags{}), deps))
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != 1 || coded.message != "one or more repositories could not be inspected; see the `error` field of each `error` row above" || !strings.Contains(out, "bad Git") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	sentinel := errors.New("writer")
	for _, format := range []string{"markdown", "yaml", "json"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			cmd := NewRepository(testRuntime(&shared.Flags{}), deps)
			cmd.SetOut(failedWriter{sentinel})
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"--format", format})
			if err := cmd.Execute(); err != sentinel {
				t.Fatalf("writer identity=%v", err)
			}
		})
	}
}
func TestStatusInvalidArgumentsAndFlagBindingsDoNotInspect(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		new  func(shared.Runtime, Dependencies) *cobra.Command
		args []string
	}{{NewRepository, []string{"a", "b"}}, {NewHistorical, []string{"a", "b"}}, {NewFleet, []string{"a"}}, {NewRepository, []string{"--all"}}, {NewRepository, []string{"--match", "a"}}} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			t.Parallel()
			deps := fakeDependencies()
			deps.Collect = func(reposelection.Request, repostatus.Observer) (repostatus.Index, error) {
				t.Fatal("inspection called")
				return repostatus.Index{}, nil
			}
			if _, _, err := execute(test.new(testRuntime(&shared.Flags{}), deps), test.args...); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}
func TestStatusInstancesKeepWritersAndJoinedProgressIndependent(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	deps.Interactive = func(io.Writer, bool) bool { return true }
	first := NewRepository(testRuntime(&shared.Flags{}), deps)
	second := NewFleet(testRuntime(&shared.Flags{}), deps)
	var a, b, progress bytes.Buffer
	first.SetOut(&a)
	first.SetErr(&progress)
	second.SetOut(&b)
	second.SetErr(io.Discard)
	first.SetArgs([]string{"--format", "json"})
	second.SetArgs([]string{"--format", "yaml", "--all"})
	if err := first.Execute(); err != nil {
		t.Fatal(err)
	}
	finished := progress.Len()
	if err := second.Execute(); err != nil {
		t.Fatal(err)
	}
	if progress.Len() != finished || !strings.Contains(progress.String(), "status: inspected 1 repositories in") || !strings.HasPrefix(a.String(), "{") || !strings.HasPrefix(b.String(), "schema_version:") {
		t.Fatalf("a=%q b=%q progress=%q", a.String(), b.String(), progress.String())
	}
}
func TestReportWriteFailuresPreserveOrderModesAndPartialEffects(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("write")
	for _, fail := range []string{"mkdir", "status.md", "status.yaml", ""} {
		t.Run(fail, func(t *testing.T) {
			t.Parallel()
			var calls []string
			var yamlData []byte
			mkdir := func(path string, mode os.FileMode) error {
				calls = append(calls, "mkdir")
				if path != "reports" || mode != 0o755 {
					t.Fatal(path, mode)
				}
				if fail == "mkdir" {
					return sentinel
				}
				return nil
			}
			write := func(path string, raw []byte, mode os.FileMode) error {
				name := filepath.Base(path)
				calls = append(calls, name)
				if mode != 0o644 {
					t.Fatal(mode)
				}
				if name == "status.md" && !strings.HasPrefix(string(raw), "# WB local repository status") {
					t.Fatalf("Markdown=%q", raw)
				}
				if name == "status.yaml" {
					yamlData = raw
				}
				if name == fail {
					return sentinel
				}
				return nil
			}
			err := writeReportsWith(repostatus.Index{SchemaVersion: 1}, "reports", false, "", mkdir, write)
			want := []string{"mkdir", "status.md", "status.yaml"}
			switch fail {
			case "mkdir":
				want = want[:1]
			case "status.md":
				want = want[:2]
			}
			if !reflect.DeepEqual(calls, want) || fail != "" && err != sentinel || fail == "" && err != nil {
				t.Fatalf("calls=%v err=%v", calls, err)
			}
			if fail == "" && string(yamlData) != "schema_version: 1\nrepositories: []\n" {
				t.Fatalf("YAML=%q", yamlData)
			}
		})
	}
}
func TestBoundFormatsPreserveOriginalSerializationBytes(t *testing.T) {
	t.Parallel()
	report := repostatus.Index{SchemaVersion: 1, Repositories: []repostatus.Row{{Repository: "a/repo", Path: "/repo", Status: "attention", Summary: "1 modified file", Modified: []string{"a.go"}}}}
	for _, format := range []string{"yaml", "markdown", "json"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			if err := writeOutput(&out, report, format, false, ""); err != nil {
				t.Fatal(err)
			}
			if format == "yaml" {
				old, err := yaml.Marshal(report)
				if err != nil || !bytes.Equal(out.Bytes(), old) {
					t.Fatalf("new=%q old=%q err=%v", out.String(), old, err)
				}
			} else if format == "markdown" {
				if out.String() != Markdown(report, false, "# WB local repository status\n\n") {
					t.Fatal(out.String())
				}
			} else if !strings.Contains(out.String(), "  \"repositories\": [") || !strings.HasSuffix(out.String(), "}\n") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestActualPartialReportsKeepCompletedMarkdownBeforeYAMLFailure(t *testing.T) {
	t.Parallel()
	for _, blocked := range []string{"status.md", "status.yaml"} {
		t.Run(blocked, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			if err := os.Mkdir(filepath.Join(directory, blocked), 0o755); err != nil {
				t.Fatal(err)
			}
			err := WriteReports(repostatus.Index{SchemaVersion: 1}, directory, false, "")
			if err == nil {
				t.Fatal("blocked output accepted")
			}
			raw, mdErr := os.ReadFile(filepath.Join(directory, "status.md"))
			if blocked == "status.yaml" && (mdErr != nil || !strings.HasPrefix(string(raw), "# WB local repository status")) {
				t.Fatalf("completed Markdown=%q err=%v", raw, mdErr)
			}
			if blocked == "status.md" {
				if _, err := os.Stat(filepath.Join(directory, "status.yaml")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("YAML written after first write failure: %v", err)
				}
			}
		})
	}
}
func TestUnsupportedFormatStillWritesBothActualReportsWithOriginalModes(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "reports")
	deps := fakeDependencies()
	deps.WriteReports = WriteReports
	out, _, err := execute(NewRepository(testRuntime(&shared.Flags{}), deps), "--report-dir", directory, "--format", "toml")
	if err == nil || err.Error() != `unknown --format "toml" (want markdown, yaml, or json)` || out != "" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	for _, name := range []string{"status.md", "status.yaml"} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("%s mode=%v err=%v", name, info, err)
		}
		raw, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || len(raw) == 0 {
			t.Fatalf("%s content=%q err=%v", name, raw, err)
		}
	}
}
