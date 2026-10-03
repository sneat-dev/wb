package cmdhooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/agentguard"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/spf13/cobra"
)

type countedInput struct {
	io.Reader
	closed bool
}

func (r *countedInput) Close() error { r.closed = true; return nil }
func TestAgentInputDelegatesCurrentRootAndClosesNamedInput(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"", "-", "payload"} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			f := fakeCommands()
			file := &countedInput{Reader: strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"git status"}}`)}
			opened := 0
			f.agent.OpenInput = func(path string) (io.ReadCloser, error) {
				opened++
				if path != "payload" {
					t.Fatal(path)
				}
				return file, nil
			}
			calls := 0
			f.agent.Inspect = func(call agentguard.ToolCall, opts agentguard.Options) agentguard.Decision {
				calls++
				if call.ToolName != "Bash" || opts.ProjectsRoot != "/projects" || opts.WBExecutable != "/verified/wb" {
					t.Fatalf("call=%+v opts=%+v", call, opts)
				}
				return agentguard.Decision{Deny: true, Reason: "no writes"}
			}
			cmd := New(f.runtime, f.git, f.life, f.agent)
			cmd.SetArgs([]string{"agent", "pre-tool-use", "--input", input})
			cmd.SetIn(strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"git status"}}`))
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !json.Valid(out.Bytes()) || !strings.Contains(out.String(), "no writes") {
				t.Fatal(calls, out.String())
			}
			if input == "payload" {
				if opened != 1 || !file.closed {
					t.Fatal("named input not closed")
				}
			} else if opened != 0 || file.closed {
				t.Fatal("stdin path opened a file")
			}
		})
	}
}
func TestAgentInputAndOutputFailuresAlwaysFailOpen(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("IO failed")
	f := fakeCommands()
	f.agent.OpenInput = func(string) (io.ReadCloser, error) { return nil, sentinel }
	f.agent.Inspect = func(agentguard.ToolCall, agentguard.Options) agentguard.Decision {
		t.Fatal("unreadable input inspected")
		return agentguard.Decision{}
	}
	if out, _, err := executeFamily(f, "agent", "pre-tool-use", "--input", "missing"); err != nil || out != "" {
		t.Fatal(err, out)
	}
	f = fakeCommands()
	f.agent.Inspect = func(agentguard.ToolCall, agentguard.Options) agentguard.Decision {
		return agentguard.Decision{Deny: true, Reason: "refused"}
	}
	cmd := New(f.runtime, f.git, f.life, f.agent)
	cmd.SetArgs([]string{"agent", "pre-tool-use"})
	cmd.SetIn(strings.NewReader(`{}`))
	cmd.SetOut(&failWriter{err: sentinel})
	if err := cmd.Execute(); err != nil {
		t.Fatal("protocol writer failure blocked an agent", err)
	}
}
func TestAgentInstallDefaultAndExplicitPathsDelegateWithoutUnwantedWrites(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"--settings", "/custom/settings"}, {"--dry-run"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			f := fakeCommands()
			writes := 0
			wantPath := filepath.Join("/home", ".claude", "settings.json")
			if len(args) > 1 {
				wantPath = "/custom/settings"
			}
			f.agent.MergeSettings = func(path, command string) ([]byte, bool, error) {
				if path != wantPath || command != "/verified/wb hooks agent pre-tool-use 2>/dev/null; exit 0" {
					t.Fatal(path, command)
				}
				return []byte("document\n"), true, nil
			}
			f.agent.WriteSettings = func(path string, raw []byte) error {
				writes++
				if path != wantPath || string(raw) != "document\n" {
					t.Fatal(path, string(raw))
				}
				return nil
			}
			out, _, err := executeFamily(f, append([]string{"agent", "install"}, args...)...)
			if err != nil {
				t.Fatal(err)
			}
			if len(args) == 1 {
				if writes != 0 || out != "document\n" {
					t.Fatal(writes, out)
				}
			} else if writes != 1 || !strings.Contains(out, "registered") {
				t.Fatal(writes, out)
			}
		})
	}
}
func TestAgentInstallErrorsAndUnchangedSettingsPreserveIdentity(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("settings failed")
	for _, mode := range []string{"home", "merge", "write", "unchanged"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := fakeCommands()
			writes := 0
			if mode == "home" {
				f.agent.Home = func() (string, error) { return "", sentinel }
			}
			if mode == "merge" {
				f.agent.MergeSettings = func(string, string) ([]byte, bool, error) { return nil, false, sentinel }
			}
			if mode == "write" {
				f.agent.WriteSettings = func(string, []byte) error { writes++; return sentinel }
			}
			if mode == "unchanged" {
				f.agent.MergeSettings = func(string, string) ([]byte, bool, error) { return []byte("unchanged"), false, nil }
				f.agent.WriteSettings = func(string, []byte) error { t.Fatal("unchanged settings written"); return nil }
			}
			out, _, err := executeFamily(f, "agent", "install")
			if mode == "unchanged" {
				if err != nil || !strings.Contains(out, "already registered") {
					t.Fatal(err, out)
				}
				return
			}
			if !errors.Is(err, sentinel) {
				t.Fatal(err)
			}
			if mode == "write" && writes != 1 {
				t.Fatal(writes)
			}
		})
	}
}
func TestMetricsAndMeasureFlagsUseFreshPolicyAndSourcePerInstance(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"metrics", "measure"} {
		for _, supplied := range []bool{false, true} {
			t.Run(kind+fmt.Sprint(supplied), func(t *testing.T) {
				t.Parallel()
				f := fakeCommands()
				f.git.LoadPolicy = func(repo, config string) (hooks.Policy, error) {
					wantRepo, wantConfig := ".", ""
					if supplied {
						wantRepo = "/repo"
						wantConfig = "policy"
					}
					if repo != wantRepo || config != wantConfig {
						t.Fatal(repo, config)
					}
					return hooks.Policy{Metrics: hooks.MetricsPolicy{Path: "policy-file"}}, nil
				}
				f.git.ReadEvents = func(path string) ([]hooks.Event, error) {
					want := "policy-file"
					if supplied {
						want = "custom-file"
					}
					if path != want {
						t.Fatal(path)
					}
					return []hooks.Event{{Timestamp: f.git.Now(), Repository: "acme/app", Hook: "post-commit", Action: "commit", DurationMS: 120}}, nil
				}
				args := []string{kind, "--json"}
				if supplied {
					args = append(args, "/repo", "--config", "policy", "--file", "custom-file", "--days", "3", "--repo", "acme")
				}
				out, _, err := executeFamily(f, args...)
				if err != nil || !json.Valid([]byte(out)) {
					t.Fatal(err, out)
				}
				if kind == "metrics" {
					var report hooks.MetricsSummary
					if e := json.Unmarshal([]byte(out), &report); e != nil {
						t.Fatal(e)
					}
					if report.Commits != 1 {
						t.Fatal(report)
					}
					if supplied && report.RepositoryFilter != "acme" {
						t.Fatal(report)
					}
				}
			})
		}
	}
	f := fakeCommands()
	var paths []string
	f.git.LoadPolicy = func(string, string) (hooks.Policy, error) {
		return hooks.Policy{Metrics: hooks.MetricsPolicy{Path: "second-policy"}}, nil
	}
	f.git.ReadEvents = func(path string) ([]hooks.Event, error) { paths = append(paths, path); return nil, nil }
	first := New(f.runtime, f.git, f.life, f.agent)
	second := New(f.runtime, f.git, f.life, f.agent)
	for i, cmd := range []*cobra.Command{first, second} {
		args := []string{"metrics", "--json"}
		if i == 0 {
			args = append(args, "--file", "first-file")
		}
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(paths, []string{"first-file", "second-policy"}) {
		t.Fatal(paths)
	}
}
func TestMetricsSourceFailuresKeepIdentity(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("source failed")
	for _, kind := range []string{"metrics", "measure"} {
		for _, phase := range []string{"policy", "events"} {
			t.Run(kind+phase, func(t *testing.T) {
				t.Parallel()
				f := fakeCommands()
				if phase == "policy" {
					f.git.LoadPolicy = func(string, string) (hooks.Policy, error) { return hooks.Policy{}, sentinel }
					f.git.ReadEvents = func(string) ([]hooks.Event, error) { t.Fatal("events read after policy failed"); return nil, nil }
				} else {
					f.git.ReadEvents = func(string) ([]hooks.Event, error) { return nil, sentinel }
				}
				if _, _, err := executeFamily(f, kind); err != sentinel {
					t.Fatal(err)
				}
			})
		}
	}
}
func TestPushTierPublishesSpecialExitCodesAndFallback(t *testing.T) {
	t.Parallel()
	for _, tier := range []int{0, 1, 2, -1} {
		t.Run(fmt.Sprint(tier), func(t *testing.T) {
			t.Parallel()
			f := fakeCommands()
			f.git.Classify = func(reader io.Reader, root string) (hooks.Classification, error) {
				raw, _ := io.ReadAll(reader)
				if root != "." || string(raw) != "refs" {
					t.Fatal(root, string(raw))
				}
				switch tier {
				case 0:
					return hooks.Classification{Tier: hooks.TierSkip, Reason: "skip"}, nil
				case 2:
					return hooks.Classification{Tier: hooks.TierPublication, Reason: "publication"}, nil
				case -1:
					return hooks.Classification{}, errors.New("classifier failed")
				default:
					return hooks.Classification{Tier: hooks.TierLint, Reason: "feature"}, nil
				}
			}
			code := -1
			f.git.Exit = func(got int) { code = got }
			cmd := New(f.runtime, f.git, f.life, f.agent)
			cmd.SetArgs([]string{"push-tier"})
			cmd.SetIn(strings.NewReader("refs"))
			var out bytes.Buffer
			cmd.SetOut(&out)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			want := tier
			if tier == -1 {
				want = 1
			}
			if code != want || !strings.Contains(out.String(), fmt.Sprintf("tier %d", want)) {
				t.Fatal(code, out.String())
			}
			if tier == -1 && !strings.Contains(out.String(), "defaulting to the fast lane") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestAgentInstallDryRunPropagatesOutputFailure(t *testing.T) {
	t.Parallel()
	family := fakeCommands()
	cmd := family.newHooksAgentInstallCmd()
	cmd.SetArgs([]string{"--settings", "/missing/settings.json", "--dry-run"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetOut(&failWriter{err: errors.New("write refused")})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "write refused") {
		t.Fatalf("dry-run output error = %v", err)
	}
}
