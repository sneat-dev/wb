package cmdworktree

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

type inventoryContextKey struct{}

func TestInventoryCommandOptionsReadCurrentFlags(t *testing.T) {
	t.Parallel()
	yes, no := true, false
	cases := []struct {
		name    string
		build   func(shared.Runtime, func(context.Context, worktrees.ListOptions) (worktrees.ListOutcome, error)) *cobra.Command
		args    []string
		options worktrees.ListOptions
	}{
		{"list-defaults", NewList, nil, worktrees.ListOptions{Base: "main", Workers: worktrees.DefaultInspectWorkers, TTL: 7 * 24 * time.Hour, IncludeDetached: true}},
		{"list-finalized", NewList, []string{"task", "--base", "release", "--parallel", "3", "--ttl", "2h", "--github", "--absorbed-by", "landing", "--only", "active", "--finalized"}, worktrees.ListOptions{Task: "task", Base: "release", Workers: 3, TTL: 2 * time.Hour, GitHub: true, AbsorbedBy: "landing", OwnerState: "active", Finalized: &yes, IncludeDetached: true}},
		{"list-not-finalized", NewList, []string{"task", "--not-finalized"}, worktrees.ListOptions{Task: "task", Base: "main", Workers: worktrees.DefaultInspectWorkers, TTL: 7 * 24 * time.Hour, Finalized: &no, IncludeDetached: true}},
		{"summary-defaults", NewSummary, []string{"task"}, worktrees.ListOptions{Task: "task", Base: "main"}},
		{"summary-explicit", NewSummary, []string{"task", "--base", "release", "--github"}, worktrees.ListOptions{Task: "task", Base: "release", GitHub: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			flags := shared.Flags{ProjectsRoot: "old", Filter: "old-filter"}
			reads, calls := 0, 0
			runtime := shared.Runtime{Flags: func() shared.Flags { reads++; return flags }}
			command := tc.build(runtime, func(ctx context.Context, opts worktrees.ListOptions) (worktrees.ListOutcome, error) {
				calls++
				if ctx.Value(inventoryContextKey{}) != "ctx" {
					t.Fatal("context lost")
				}
				expected := tc.options
				expected.ProjectsRoot = "current"
				expected.Filter = "current-filter"
				if !reflect.DeepEqual(opts, expected) {
					t.Fatalf("options=%+v want=%+v", opts, expected)
				}
				return worktrees.ListOutcome{SchemaVersion: 1}, nil
			})
			if reads != 0 || calls != 0 {
				t.Fatal("operation/flags captured before parse")
			}
			flags = shared.Flags{ProjectsRoot: "current", Filter: "current-filter"}
			command.SetContext(context.WithValue(context.Background(), inventoryContextKey{}, "ctx"))
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			command.SetArgs(tc.args)
			if err := command.Execute(); err != nil || reads != 1 || calls != 1 {
				t.Fatalf("err=%v reads=%d calls=%d", err, reads, calls)
			}
			if strings.HasPrefix(tc.name, "summary") {
				if command.Annotations["wb.dev/discovery-terms"] != "inspect progress status next action task work worktree branch pull request ready merge" {
					t.Fatal("discovery policy lost")
				}
				for _, flag := range []string{"ttl", "parallel", "finalized", "not-finalized", "only", "absorbed-by"} {
					if command.Flags().Lookup(flag) != nil {
						t.Fatalf("summary gained flag %s", flag)
					}
				}
			}
		})
	}
}

func TestInventoryCommandsValidateBeforeExecutingAndPreserveUsageIdentity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		build func(shared.Runtime, func(context.Context, worktrees.ListOptions) (worktrees.ListOutcome, error)) *cobra.Command
		args  []string
		usage bool
	}{
		{"list-format", NewList, []string{"--format", "yaml"}, false}, {"summary-format", NewSummary, []string{"task", "--format", "yaml"}, false}, {"list-conflict", NewList, []string{"--finalized", "--not-finalized"}, true}, {"summary-task-required", NewSummary, nil, false}, {"list-too-many", NewList, []string{"a", "b"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			sentinel := errors.New("coded usage")
			command := tc.build(shared.Runtime{Flags: func() shared.Flags { t.Fatal("flags read before validation"); return shared.Flags{} }, ExitError: func(code int, message string) error {
				if code != shared.ExitUsage || message != "--finalized and --not-finalized cannot be combined" {
					t.Fatalf("usage %d %s", code, message)
				}
				return sentinel
			}}, func(context.Context, worktrees.ListOptions) (worktrees.ListOutcome, error) {
				calls++
				return worktrees.ListOutcome{}, nil
			})
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			command.SetArgs(tc.args)
			err := command.Execute()
			if err == nil || calls != 0 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
			if tc.usage && !errors.Is(err, sentinel) {
				t.Fatalf("usage identity=%v", err)
			}
		})
	}
}

func TestInventoryCommandsPreserveOutcomeAndStreamFailureOrder(t *testing.T) {
	t.Parallel()
	for _, family := range []string{"list", "summary"} {
		t.Run(family, func(t *testing.T) {
			t.Parallel()
			for _, stage := range []string{"operation", "diagnostic", "artifact", "text-write", "json-write", "text", "json"} {
				t.Run(stage, func(t *testing.T) {
					t.Parallel()
					sentinel := errors.New("operation or stdout failure")
					outcome := worktrees.ListOutcome{SchemaVersion: 1, Results: []worktrees.ListResult{{Task: "task", Repository: "owner/repo", Clean: true}}, Diagnostics: []worktrees.ListDiagnostic{{Task: "task", Path: "candidate", Message: "diagnostic"}}, Artifacts: []worktrees.LifecycleArtifact{{Kind: "stage", State: "pending", Path: "artifact"}}, Purged: []worktrees.PurgedArtefact{{Task: "task", Path: "retired", Kind: "retired_stage"}}}
					operation := func(context.Context, worktrees.ListOptions) (worktrees.ListOutcome, error) {
						if stage == "operation" {
							return outcome, sentinel
						}
						return outcome, nil
					}
					runtime := shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: "private"} }}
					var command *cobra.Command
					args := []string{}
					if family == "list" {
						command = NewList(runtime, operation)
					} else {
						command = NewSummary(runtime, operation)
						args = append(args, "task")
					}
					if strings.HasPrefix(stage, "json") {
						args = append(args, "--format", "json")
					}
					var stdout, stderr bytes.Buffer
					command.SetOut(&stdout)
					command.SetErr(&stderr)
					if stage == "diagnostic" {
						command.SetErr(&activeLimitedWriter{Allow: 0})
					}
					if stage == "artifact" {
						command.SetErr(&activeLimitedWriter{Allow: 1})
					}
					if strings.HasSuffix(stage, "-write") {
						command.SetOut(collaborationWriterError{sentinel})
					}
					command.SilenceUsage, command.SilenceErrors = true, true
					command.SetArgs(args)
					err := command.Execute()
					switch stage {
					case "operation":
						if !errors.Is(err, sentinel) || stdout.Len() != 0 || stderr.Len() != 0 {
							t.Fatalf("operation err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
						}
					case "diagnostic", "artifact":
						if err == nil || stdout.Len() != 0 {
							t.Fatalf("stderr failure err=%v stdout=%q", err, stdout.String())
						}
					case "text-write", "json-write":
						if !errors.Is(err, sentinel) {
							t.Fatalf("stdout err=%v", err)
						}
					default:
						if err != nil {
							t.Fatal(err)
						}
						want := "warning: task task candidate candidate: diagnostic\ninfo: inventory classified WB internal stage as pending: artifact\n"
						if stderr.String() != want {
							t.Fatalf("diagnostics=%q", stderr.String())
						}
						if stage == "json" {
							var actual worktrees.ListOutcome
							if err := json.Unmarshal(stdout.Bytes(), &actual); err != nil || !reflect.DeepEqual(actual, outcome) {
								t.Fatalf("envelope=%+v err=%v", actual, err)
							}
						} else if !strings.Contains(stdout.String(), "owner/repo") {
							t.Fatalf("text=%q", stdout.String())
						}
					}
				})
			}
		})
	}
}
