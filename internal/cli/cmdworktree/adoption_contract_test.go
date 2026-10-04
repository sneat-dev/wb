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

type adoptionContextKey struct{}

func adoptionExecute(command *cobra.Command, args ...string) (string, error) {
	var out bytes.Buffer
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetOut(&out)
	command.SetErr(io.Discard)
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), err
}

func TestAdoptionContractsUseLazyOptionsAndAdmissionLifetime(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{ProjectsRoot: "before", Filter: "old"}
	runtime := shared.Runtime{Flags: func() shared.Flags { return flags }}
	ctx := context.WithValue(context.Background(), adoptionContextKey{}, "caller")
	back := NewBackfill(runtime, func(got context.Context, o worktrees.BackfillOptions) ([]worktrees.BackfillResult, error) {
		if got != ctx || !reflect.DeepEqual(o, worktrees.BackfillOptions{ProjectsRoot: "after", Base: "topic", Apply: true}) {
			t.Fatalf("backfill context/options = %v/%+v", got, o)
		}
		return []worktrees.BackfillResult{{Action: worktrees.BackfillSkipped, Path: "checkout", Reason: "existing"}}, nil
	})
	back.SetContext(ctx)
	flags.ProjectsRoot = "after"
	if out, err := adoptionExecute(back, "--base", "topic", "--apply"); err != nil || !strings.Contains(out, "skipped checkout: existing") || strings.Contains(out, "dry-run") {
		t.Fatalf("backfill output=%q error=%v", out, err)
	}
	var order []string
	adopt := NewAdopt(runtime, func(got context.Context, o worktrees.AdoptOptions) ([]worktrees.AdoptResult, error) {
		order = append(order, "operation")
		if got != ctx || !reflect.DeepEqual(o, worktrees.AdoptOptions{ProjectsRoot: "after", Base: "topic", Path: "checkout", Initiator: "human", Filter: "new", Apply: true}) {
			t.Fatalf("adopt context/options = %v/%+v", got, o)
		}
		return []worktrees.AdoptResult{{Action: worktrees.AdoptAdopted, Task: "task", Path: "checkout"}}, nil
	}, func(c *cobra.Command, apply bool) (func(), error) {
		order = append(order, "admit")
		if c.Context() != ctx || !apply {
			t.Fatal("admission context/apply")
		}
		return func() { order = append(order, "release") }, nil
	})
	adopt.Flags().String("initiator", "", "audit actor")
	adopt.SetContext(ctx)
	flags.Filter = "new"
	if out, err := adoptionExecute(adopt, "checkout", "--base", "topic", "--apply", "--initiator", "human"); err != nil || !strings.Contains(out, "task") {
		t.Fatalf("adopt=%q/%v", out, err)
	}
	if !reflect.DeepEqual(order, []string{"admit", "operation", "release"}) {
		t.Fatalf("order=%v", order)
	}
	orphans := NewOrphans(runtime, func(got context.Context, o worktrees.OrphanOptions) (worktrees.OrphanReport, error) {
		if got != ctx || !reflect.DeepEqual(o, worktrees.OrphanOptions{ProjectsRoot: "after", Base: "topic", StaleAfter: 3 * 24 * time.Hour}) {
			t.Fatalf("orphans context/options=%v/%+v", got, o)
		}
		return worktrees.OrphanReport{}, nil
	})
	orphans.SetContext(ctx)
	if out, err := adoptionExecute(orphans, "--base", "topic", "--stale-days", "3", "--only", "remove"); err != nil || !strings.Contains(out, "0 worktrees") {
		t.Fatalf("orphans=%q/%v", out, err)
	}
}

func TestAdoptionContractsDefaultsJSONAndOrdinaryValidationErrors(t *testing.T) {
	t.Parallel()
	runtime := shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: "root", Filter: "filter"} }}
	backResults := []worktrees.BackfillResult{{Action: worktrees.BackfillSkipped, Path: "private"}}
	adoptResults := []worktrees.AdoptResult{{Action: worktrees.AdoptWouldAdopt, Path: "private"}}
	orphanReport := worktrees.OrphanReport{Unscanned: []string{"private"}}
	factories := []struct {
		name string
		make func() *cobra.Command
		args []string
		want any
	}{
		{"backfill", func() *cobra.Command {
			return NewBackfill(runtime, func(_ context.Context, o worktrees.BackfillOptions) ([]worktrees.BackfillResult, error) {
				if o.Base != "main" || o.Apply {
					t.Fatalf("defaults=%+v", o)
				}
				return backResults, nil
			})
		}, nil, backResults},
		{"adopt", func() *cobra.Command {
			return NewAdopt(runtime, func(_ context.Context, o worktrees.AdoptOptions) ([]worktrees.AdoptResult, error) {
				if o.Base != "main" || o.Apply || !o.AllExternal || o.Path != "" || o.Filter != "filter" || o.Initiator != "" {
					t.Fatalf("defaults=%+v", o)
				}
				return adoptResults, nil
			}, func(_ *cobra.Command, apply bool) (func(), error) {
				if apply {
					t.Fatal("dry run admitted as mutation")
				}
				return func() {}, nil
			})
		}, []string{"--all-external"}, adoptResults},
		{"orphans", func() *cobra.Command {
			return NewOrphans(runtime, func(_ context.Context, o worktrees.OrphanOptions) (worktrees.OrphanReport, error) {
				if o.Base != "main" || o.StaleAfter != 14*24*time.Hour {
					t.Fatalf("defaults=%+v", o)
				}
				return orphanReport, nil
			})
		}, []string{"--only", "remove"}, orphanReport},
	}
	for _, tc := range factories {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := adoptionExecute(tc.make(), append(append([]string{}, tc.args...), "--format", "json")...)
			want, _ := json.MarshalIndent(tc.want, "", "  ")
			if err != nil || out != string(want)+"\n" {
				t.Fatalf("JSON=%q/%v want=%q", out, err, want)
			}
			if _, err = adoptionExecute(tc.make(), append(append([]string{}, tc.args...), "--format", "yaml")...); err == nil || err.Error() != "unsupported format \"yaml\"; use text or json" {
				t.Fatalf("format error=%v", err)
			}
		})
	}
	operation := func(context.Context, worktrees.AdoptOptions) ([]worktrees.AdoptResult, error) {
		t.Fatal("selector reached operation")
		return nil, nil
	}
	admit := func(*cobra.Command, bool) (func(), error) { t.Fatal("selector reached admission"); return nil, nil }
	for _, args := range [][]string{nil, {"path", "--all-external"}} {
		if _, err := adoptionExecute(NewAdopt(runtime, operation, admit), args...); err == nil || err.Error() != "supply exactly one of a worktree path or --all-external" {
			t.Fatalf("selector=%v", err)
		}
	}
}

func TestAdoptionContractsPreserveOperationAdmissionAndWriterErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("operation failure")
	runtime := shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }}
	var released int
	factories := []struct {
		name string
		make func(bool) *cobra.Command
		args []string
	}{
		{"backfill", func(fail bool) *cobra.Command {
			return NewBackfill(runtime, func(context.Context, worktrees.BackfillOptions) ([]worktrees.BackfillResult, error) {
				if fail {
					return nil, boom
				}
				return []worktrees.BackfillResult{{Action: worktrees.BackfillSkipped, Path: "skip", Reason: "because"}, {Action: "written"}}, nil
			})
		}, nil},
		{"adopt", func(fail bool) *cobra.Command {
			return NewAdopt(runtime, func(context.Context, worktrees.AdoptOptions) ([]worktrees.AdoptResult, error) {
				if fail {
					return nil, boom
				}
				return []worktrees.AdoptResult{{Action: worktrees.AdoptWouldAdopt, Task: "task"}}, nil
			}, func(*cobra.Command, bool) (func(), error) { return func() { released++ }, nil })
		}, []string{"--all-external"}},
		{"orphans", func(fail bool) *cobra.Command {
			return NewOrphans(runtime, func(context.Context, worktrees.OrphanOptions) (worktrees.OrphanReport, error) {
				if fail {
					return worktrees.OrphanReport{}, boom
				}
				return worktrees.OrphanReport{}, nil
			})
		}, nil},
	}
	for _, tc := range factories {
		if out, err := adoptionExecute(tc.make(true), tc.args...); err != boom || out != "" {
			t.Fatalf("%s operation=%q/%v", tc.name, out, err)
		}
		for _, format := range []string{"text", "json"} {
			command := tc.make(false)
			command.SetOut(&activeLimitedWriter{})
			command.SetErr(io.Discard)
			command.SetArgs(append(append([]string{}, tc.args...), "--format", format))
			if err := command.Execute(); err == nil {
				t.Fatalf("%s %s writer returned nil", tc.name, format)
			}
		}
	}
	if released != 3 {
		t.Fatalf("release count=%d", released)
	}
	command := NewAdopt(runtime, func(context.Context, worktrees.AdoptOptions) ([]worktrees.AdoptResult, error) {
		t.Fatal("denied operation called")
		return nil, nil
	}, func(*cobra.Command, bool) (func(), error) { return nil, boom })
	if _, err := adoptionExecute(command, "--all-external", "--apply"); err != boom {
		t.Fatalf("admission=%v", err)
	}
	// Backfill's shared totals and suffix must also propagate later writes.
	for allow := 1; allow < 4; allow++ {
		command := factories[0].make(false)
		command.SetOut(&activeLimitedWriter{Allow: allow})
		command.SetErr(io.Discard)
		if err := command.Execute(); err == nil {
			t.Fatalf("backfill later write %d returned nil", allow)
		}
	}
	if err := writeAdoption(io.Discard, []worktrees.AdoptResult{{Action: "other"}}, true); err != nil {
		t.Fatal(err)
	}
}
