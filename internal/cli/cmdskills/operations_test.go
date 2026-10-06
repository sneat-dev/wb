package cmdskills

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/wbskills"

	"github.com/strongo/cli-helpers/skillsync"
	skillscmd "github.com/strongo/cli-helpers/skillsync/cobracmd"
)

type sentinelWriter struct {
	remaining int
	err       error
}

func (w *sentinelWriter) Write(p []byte) (int, error) {
	if w.remaining == 0 {
		return 0, w.err
	}
	w.remaining--
	return len(p), nil
}

func fakeHook() HookDependencies {
	return HookDependencies{Quote: shared.ShellQuoteArg, Executable: func() string { return "/verified/wb" }, Home: func() (string, error) { return "/private/home", nil }, MergeSettings: func(string, string) ([]byte, bool, error) { return []byte("settings\n"), true, nil }, WriteSettings: func(string, []byte) error { return nil }, Announcement: func() string { return "registration reminder" }}
}

func TestHookFlagsDelegatePathsAndAvoidWritesForDryRun(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"--settings", "/specific/settings"}, {"--dry-run"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			deps := fakeHook()
			writes := 0
			deps.MergeSettings = func(path, command string) ([]byte, bool, error) {
				want := filepath.Join("/private/home", ".claude", "settings.json")
				if len(args) > 1 {
					want = "/specific/settings"
				}
				if path != want || command != "/verified/wb skills hook run 2>/dev/null; exit 0" {
					t.Fatalf("path=%s command=%s", path, command)
				}
				return []byte("settings\n"), true, nil
			}
			deps.WriteSettings = func(path string, document []byte) error {
				writes++
				if string(document) != "settings\n" {
					t.Fatal(string(document))
				}
				return nil
			}
			cmd := newSkillsHookInstallCmd(deps)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			dry := len(args) == 1
			if dry {
				if writes != 0 || out.String() != "settings\n" {
					t.Fatalf("writes=%d out=%q", writes, out.String())
				}
			} else if writes != 1 || !strings.Contains(out.String(), "hook registered") {
				t.Fatalf("writes=%d out=%q", writes, out.String())
			}
		})
	}
}

func TestHookFailuresReturnTheOriginalCause(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("operation failed")
	for _, kind := range []string{"home", "merge", "write", "dry-output", "already-output", "registered-output"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			deps := fakeHook()
			args := []string{}
			deps.Home = func() (string, error) {
				if kind == "home" {
					return "", sentinel
				}
				return "/home", nil
			}
			deps.MergeSettings = func(string, string) ([]byte, bool, error) {
				if kind == "merge" {
					return nil, false, sentinel
				}
				return []byte("doc"), kind != "already-output", nil
			}
			deps.WriteSettings = func(string, []byte) error {
				if kind == "write" {
					return sentinel
				}
				return nil
			}
			writer := io.Writer(io.Discard)
			if strings.HasSuffix(kind, "output") {
				writer = &sentinelWriter{err: sentinel}
			}
			if kind == "dry-output" {
				args = []string{"--dry-run"}
			}
			cmd := newSkillsHookInstallCmd(deps)
			cmd.SetOut(writer)
			cmd.SetArgs(args)
			if err := cmd.Execute(); !errors.Is(err, sentinel) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	for _, remaining := range []int{0, 1} {
		cmd := newSkillsHookPrintCmd(fakeHook())
		cmd.SetOut(&sentinelWriter{remaining: remaining, err: sentinel})
		if err := cmd.Execute(); !errors.Is(err, sentinel) {
			t.Fatalf("print stage=%d err=%v", remaining, err)
		}
	}
	cmd := newSkillsHookRunCmd(fakeHook())
	cmd.SetOut(&sentinelWriter{err: sentinel})
	if err := cmd.Execute(); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

func TestHookPrintSuccessfulBytesStayUnchanged(t *testing.T) {
	t.Parallel()
	deps := fakeHook()
	doc, err := json.MarshalIndent(skillsHookSettingsSnippet(deps.Executable(), deps.Quote), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	cmd := newSkillsHookPrintCmd(deps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := string(doc) + "\n\nMerge this into ~/.claude/settings.json's \"hooks\" key (preserving any\nother hooks already there), or run: wb skills hook install\n"
	if out.String() != want {
		t.Fatalf("out=%q want=%q", out.String(), want)
	}
}

func TestSkillsParentAndInvalidHookArgumentsNeverOperate(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("config unavailable")
	sync := SyncDependencies{Config: func() (skillsync.Config, error) { return skillsync.Config{}, sentinel }}
	hook := fakeHook()
	hook.MergeSettings = func(string, string) ([]byte, bool, error) {
		t.Fatal("invalid arguments merged settings")
		return nil, false, nil
	}
	hook.Announcement = func() string { t.Fatal("invalid arguments ran announcement"); return "" }
	root := New(testRuntime(), sync, hook)
	if root.Name() != "skills" || len(root.Commands()) != 2 {
		t.Fatal(root.Commands())
	}
	for _, args := range [][]string{{"hook", "install", "extra"}, {"hook", "print", "extra"}, {"hook", "run", "extra"}} {
		cmd := New(testRuntime(), sync, hook)
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("args=%q accepted", args)
		}
	}
	cmd := newSkillsSyncCmd(testRuntime(), sync)
	cmd.SetArgs(nil)
	var coded *exitError
	if err := cmd.Execute(); !errors.As(err, &coded) || coded.code != exitFindings || !strings.Contains(coded.message, sentinel.Error()) {
		t.Fatalf("err=%v", err)
	}
}

func TestReportWriterFailurePathsUseOnlyFakeResults(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("output failed")
	noChanges := skillscmd.TargetResult{Dir: "/fake", Report: skillsync.Report{Dir: "/fake"}}
	failed := noChanges
	failed.Err = errors.New("target failed")
	for _, tc := range []struct {
		name, format string
		results      []skillscmd.TargetResult
		remaining    int
	}{{"json", "json", []skillscmd.TargetResult{noChanges}, 0}, {"footer", "text", []skillscmd.TargetResult{noChanges}, 1}, {"error-detail", "text", []skillscmd.TargetResult{failed}, 1}, {"delegate", "text", []skillscmd.TargetResult{noChanges}, 0}} {
		if err := writeSkillsSyncReports(&sentinelWriter{remaining: tc.remaining, err: sentinel}, tc.results, tc.format); !errors.Is(err, sentinel) {
			t.Fatalf("%s err=%v", tc.name, err)
		}
	}
}

func TestReportsRenderAllActionsAndMultipleTargetsWithoutFixtures(t *testing.T) {
	t.Parallel()
	report := skillsync.Report{Dir: "/fake/skills", DryRun: true, CLIVersion: "1.2.3", Bundles: []skillsync.ResolvedBundle{{Plugin: wbskills.PluginIdentity(), PriorCLIVersion: "1.0.0"}}}
	for _, action := range []skillsync.Action{skillsync.Added, skillsync.Updated, skillsync.Unchanged, skillsync.Removed, skillsync.Conflict} {
		report.Changes = append(report.Changes, skillsync.Change{Name: string(action) + "-skill", Action: action})
	}
	results := []skillscmd.TargetResult{{Harness: "cursor", Dir: report.Dir, Report: report}, {Harness: "codex", Dir: "/fake/codex", Report: report}}
	for _, format := range []string{"text", "json"} {
		var out bytes.Buffer
		if err := writeSkillsSyncReports(&out, results, format); err != nil {
			t.Fatal(err)
		}
		for _, word := range []string{"added-skill", "updated-skill", "unchanged-skill", "removed-skill", "conflict-skill"} {
			if !strings.Contains(out.String(), word) {
				t.Fatalf("%s missing %s: %s", format, word, out.String())
			}
		}
		if format == "json" {
			var payload skillsSyncMultiJSON
			if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if !payload.DryRun || payload.WBVersion != "1.2.3" || len(payload.Targets) != 2 || payload.Targets[0].PriorWBVersion != "1.0.0" {
				t.Fatalf("payload=%+v", payload)
			}
		} else if !strings.Contains(out.String(), "would sync") {
			t.Fatal(out.String())
		}
	}
	// Commands own mutable flag/output state even when sharing immutable fake data.
	deps := testSyncDependencies()
	first := newSkillsSyncCmd(testRuntime(), deps)
	second := newSkillsSyncCmd(testRuntime(), deps)
	if err := first.ParseFlags([]string{"--dry-run", "--format=json"}); err != nil {
		t.Fatal(err)
	}
	if value, _ := second.Flags().GetBool("dry-run"); value {
		t.Fatal("flags leaked across instances")
	}
	if value, _ := second.Flags().GetString("format"); value != "text" {
		t.Fatal(value)
	}
	if second.Flags().Lookup("newer-compatible") == nil {
		t.Fatal("newer-compatible flag missing")
	}
}

func TestSkillsConflictMapsOwnedNamesAndMarkerToFindings(t *testing.T) {
	t.Parallel()
	report := skillsync.Report{Dir: "/private/target", Changes: []skillsync.Change{{Name: "owned", Action: skillsync.Conflict}, {Name: "safe", Action: skillsync.Unchanged}}}
	err := (skillsSyncErrors{runtime: testRuntime()}).Conflict(report)
	var coded *exitError
	if !errors.As(err, &coded) || coded.code != exitFindings || !strings.Contains(err.Error(), "1 skill(s)") || !strings.Contains(err.Error(), report.Dir) {
		t.Fatalf("error=%v", err)
	}
}
