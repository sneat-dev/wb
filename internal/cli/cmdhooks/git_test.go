package cmdhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
)

func executeFamily(f commands, args ...string) (string, string, error) {
	cmd := New(f.runtime, f.git, f.life, f.agent)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}
func TestHookRegistrationAndArgumentRejectionNeverDelegates(t *testing.T) {
	t.Parallel()
	f := fakeCommands()
	root := New(f.runtime, f.git, f.life, f.agent)
	if len(root.Commands()) != 9 {
		t.Fatal(root.Commands())
	}
	for _, args := range [][]string{{"install", "one", "two"}, {"repair", "--fleet", "path"}, {"install", "--fleet", "path"}, {"check", "--fleet", "path"}, {"run"}, {"push-tier", "extra"}, {"agent", "install", "extra"}, {"agent", "pre-tool-use", "extra"}, {"lifecycle", "retry"}, {"lifecycle", "check", "extra"}, {"lifecycle", "run-pending"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			g := fakeCommands()
			g.git.Apply = func(hooks.ApplyOptions) (hooks.ApplyResult, error) {
				t.Fatal("unexpected apply")
				return hooks.ApplyResult{}, nil
			}
			g.git.Check = func(string, string, string, string) (hooks.CheckReport, error) {
				t.Fatal("unexpected check")
				return hooks.CheckReport{}, nil
			}
			g.git.LocalRepos = func(string, string) ([]discover.Repo, error) { t.Fatal("unexpected scan"); return nil, nil }
			g.life.Drain = func(_ context.Context, _ DrainOptions) (lifecyclehooks.Report, error) {
				t.Fatal("unexpected drain")
				return lifecyclehooks.Report{}, nil
			}
			if _, _, err := executeFamily(g, args...); err == nil {
				t.Fatal("invalid args succeeded")
			}
		})
	}
}
func TestInstallRepairFlagsObserveCurrentRuntimeAndDefaultOptions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args          []string
		path, config  string
		repair, force bool
	}{{[]string{"install"}, ".", "", false, false}, {[]string{"repair", "/repo", "--config", "policy", "--force"}, "/repo", "policy", true, true}} {
		t.Run(tc.args[0], func(t *testing.T) {
			t.Parallel()
			f := fakeCommands()
			flags := shared.Flags{ProjectsRoot: "before"}
			f.runtime.Flags = func() shared.Flags { return flags }
			called := false
			f.git.Apply = func(got hooks.ApplyOptions) (hooks.ApplyResult, error) {
				called = true
				want := hooks.ApplyOptions{RepoPath: tc.path, ConfigPath: tc.config, WBExecutable: "/verified/wb", ProjectsRoot: "after", Repair: tc.repair, Force: tc.force}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("options=%+v want=%+v", got, want)
				}
				return hooks.ApplyResult{Actions: []string{"installed"}, Report: hooks.CheckReport{RepoRoot: tc.path, MetricsPath: "metrics"}}, nil
			}
			cmd := New(f.runtime, f.git, f.life, f.agent)
			flags.ProjectsRoot = "after"
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(tc.args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if !called || !strings.Contains(out.String(), "installed") || !strings.Contains(out.String(), "local metrics: metrics") {
				t.Fatal(out.String())
			}
		})
	}
}
func TestCheckFlagsJSONFindingsAndDelegatedErrorIdentity(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("check failed")
	for _, mode := range []string{"text", "json", "error"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := fakeCommands()
			f.git.Check = func(repo, config, exe, root string) (hooks.CheckReport, error) {
				if repo != "/repo" || config != "policy" || exe != "/verified/wb" || root != "/projects" {
					t.Fatal(repo, config, exe, root)
				}
				if mode == "error" {
					return hooks.CheckReport{}, sentinel
				}
				return hooks.CheckReport{RepoRoot: repo, Findings: []hooks.Finding{{Code: "missing", Message: "repair"}}}, nil
			}
			args := []string{"validate", "/repo", "--config", "policy"}
			if mode == "json" {
				args = append(args, "--json")
			}
			out, _, err := executeFamily(f, args...)
			if mode == "error" {
				if err != sentinel {
					t.Fatal(err)
				}
				return
			}
			var finding *CheckError
			if !errors.As(err, &finding) || finding.Count != 1 || finding.Fleet {
				t.Fatal(err)
			}
			if mode == "json" && !json.Valid([]byte(out)) {
				t.Fatal(out)
			}
			if mode == "text" && !strings.Contains(out, "missing") {
				t.Fatal(out)
			}
		})
	}
}
func TestFleetRetainsPartialResultsAndCounts(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("operation failed")
	for _, kind := range []string{"install", "repair", "check"} {
		for _, jsonOut := range []bool{false, true} {
			t.Run(kind+fmt.Sprint(jsonOut), func(t *testing.T) {
				t.Parallel()
				f := fakeCommands()
				f.git.LocalRepos = func(root, filter string) ([]discover.Repo, error) {
					if root != "/projects" || filter != "acme" {
						t.Fatal(root, filter)
					}
					return []discover.Repo{{Org: "acme", Name: "bad", Path: "bad"}, {Org: "acme", Name: "findings", Path: "findings"}, {Org: "acme", Name: "good", Path: "good"}}, nil
				}
				f.git.Apply = func(opt hooks.ApplyOptions) (hooks.ApplyResult, error) {
					if opt.RepoPath == "bad" {
						return hooks.ApplyResult{}, sentinel
					}
					return hooks.ApplyResult{Actions: []string{"repaired"}}, nil
				}
				f.git.Check = func(repo, _, _, _ string) (hooks.CheckReport, error) {
					if repo == "bad" {
						return hooks.CheckReport{}, sentinel
					}
					if repo == "findings" {
						return hooks.CheckReport{Findings: []hooks.Finding{{Code: "one"}, {Code: "two"}}}, nil
					}
					return hooks.CheckReport{}, nil
				}
				args := []string{kind, "--fleet"}
				if jsonOut && kind == "check" {
					args = append(args, "--json")
				}
				out, errOut, err := executeFamily(f, args...)
				if err == nil {
					t.Fatal("partial failure lost")
				}
				if kind == "check" {
					var coded *CheckError
					if !errors.As(err, &coded) || coded.Count != 3 || !coded.Fleet {
						t.Fatal(err)
					}
					if jsonOut {
						var entries []fleetHooksCheck
						if e := json.Unmarshal([]byte(out), &entries); e != nil || len(entries) != 3 || entries[0].Error != sentinel.Error() {
							t.Fatalf("entries=%v err=%v", entries, e)
						}
					} else if !strings.Contains(out, "3 problems") {
						t.Fatal(out)
					}
				} else if !strings.Contains(out, "Processed 3 repositories; 1 failed") || !strings.Contains(errOut, "acme/bad") {
					t.Fatal(out, errOut)
				}
			})
		}
	}
}
func TestGitOperationFailuresAndWarningsHaveExactPolicy(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("operation failed")
	for _, kind := range []string{"install", "check", "fleet-install", "fleet-check", "run"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f := fakeCommands()
			f.git.Apply = func(hooks.ApplyOptions) (hooks.ApplyResult, error) { return hooks.ApplyResult{}, sentinel }
			f.git.Check = func(string, string, string, string) (hooks.CheckReport, error) { return hooks.CheckReport{}, sentinel }
			f.git.LocalRepos = func(string, string) ([]discover.Repo, error) { return nil, sentinel }
			f.git.Run = func(opt hooks.RunOptions) (hooks.RunResult, error) {
				if opt.RepoPath != "." || opt.Hook != "pre-push" || len(opt.Args) != 1 || opt.Args[0] != "origin" || opt.ProjectsRoot != "/projects" || opt.WBExecutable != "/verified/wb" || opt.Stdin == nil || opt.Stdout == nil || opt.Stderr == nil {
					t.Fatalf("options=%+v", opt)
				}
				return hooks.RunResult{MetricsError: errors.New("metrics failed")}, sentinel
			}
			args := []string{kind}
			switch kind {
			case "fleet-install":
				args = []string{"install", "--fleet"}
			case "fleet-check":
				args = []string{"check", "--fleet"}
			case "run":
				args = []string{"run", "pre-push", "origin"}
			}
			_, errOut, err := executeFamily(f, args...)
			if err != sentinel {
				t.Fatal(err)
			}
			if kind == "run" && !strings.Contains(errOut, "metrics failed") {
				t.Fatal(errOut)
			}
		})
	}
	f := fakeCommands()
	f.git.Run = func(hooks.RunOptions) (hooks.RunResult, error) { return hooks.RunResult{MetricsError: sentinel}, nil }
	cmd := New(f.runtime, f.git, f.life, f.agent)
	cmd.SetArgs([]string{"run", "pre-commit"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(&failWriter{err: sentinel})
	if err := cmd.Execute(); err != nil {
		t.Fatal("metrics warning turned success into failure", err)
	}
}
