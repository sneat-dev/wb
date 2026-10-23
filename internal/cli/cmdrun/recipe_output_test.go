package cmdrun

import (
	"bytes"
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/runexec"
	"reflect"
	"strings"
	"testing"
)

func reportRows() []runexec.RecipeRow {
	return []runexec.RecipeRow{{Bucket: runexec.Updated, Text: "acme/app — would update"}, {Bucket: runexec.Skipped, Text: "acme/localonly — local-only (not under your GitHub orgs)"}, {Bucket: runexec.Skipped, Text: "acme/remoteonly — remote-only (clone to evaluate)"}, {Bucket: runexec.Forked, Text: "acme/fork"}, {Bucket: runexec.Archived, Text: "acme/archived"}, {Bucket: runexec.Failed, Text: "z failure"}, {Bucket: runexec.Failed, Text: "a failure"}}
}
func TestRecipeReportRetainsBucketLinesSummaryCountsAndSortedErrors(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	rows := reportRows()
	copyRows := append([]runexec.RecipeRow(nil), rows...)
	if err := printRecipeEvent(&out, &errOut, runexec.RecipeEvent{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if err := printRecipeEvent(&out, &errOut, runexec.RecipeEvent{Row: row}); err != nil {
			t.Fatal(err)
		}
	}
	if err := printRecipeResult(&out, runexec.RecipeResult{Rows: rows}, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"✓ acme/app — would", "⑂ acme/fork", "▪ acme/archived", "– acme/localonly — local-only (not under your GitHub orgs)", "– acme/remoteonly — remote-only (clone to evaluate)", "Updated  1", "Skipped  2", "Forks    1", "Archived 1", "Errors   2", "  ✗ a failure\n  ✗ z failure\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing%q: %s", want, out.String())
		}
	}
	if errOut.String() != "dry-run: reporting only; pass --apply to commit & push\n" || !reflect.DeepEqual(rows, copyRows) {
		t.Fatal("warning bytes or input mutation")
	}
	sentinel := errors.New("write refused")
	if err := printRecipeEvent(&out, failingWriter{sentinel}, runexec.RecipeEvent{DryRun: true}); err != sentinel {
		t.Fatal(err)
	}
	if err := printRecipeEvent(failingWriter{sentinel}, &errOut, runexec.RecipeEvent{Row: rows[0]}); err != sentinel {
		t.Fatal(err)
	}
	if err := printRecipeResult(failingWriter{sentinel}, runexec.RecipeResult{Names: []string{"name"}}, true); err != sentinel {
		t.Fatal(err)
	}
	if err := printRecipeResult(failingWriter{sentinel}, runexec.RecipeResult{Rows: rows}, false); err != sentinel {
		t.Fatal(err)
	}
	writer := &failAfterNWriter{failAt: 2}
	if err := printRecipeResult(writer, runexec.RecipeResult{Rows: rows}, false); err == nil || writer.writes != 2 {
		t.Fatal(err)
	}
}
func TestRecipeCommandReadsParsedFlagsListsAndMapsFindings(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{ProjectsRoot: "before", Filter: "before", ExtraOrgs: []string{"org"}}
	runtime := testRuntime()
	runtime.Flags = func() shared.Flags { return flags }
	deps := fakeDependencies()
	deps.Recipes = func(_ context.Context, r runexec.RecipeRequest) (runexec.RecipeResult, error) {
		if r.ProjectsRoot != "parsed" || r.Filter != "parsed" || !reflect.DeepEqual(r.ExtraOrgs, []string{"org"}) || r.ConfigPath != "config" || r.Name != "name" || !r.Apply {
			t.Fatalf("request=%+v", r)
		}
		r.ExtraOrgs[0] = "changed"
		if err := r.Observe(runexec.RecipeEvent{Row: runexec.RecipeRow{Bucket: runexec.Updated, Text: "landed"}}); err != nil {
			return runexec.RecipeResult{}, err
		}
		return runexec.RecipeResult{Rows: []runexec.RecipeRow{{Bucket: runexec.Updated, Text: "landed"}}, Findings: true}, nil
	}
	command := New(runtime, deps)
	command.Flags().StringVar(&flags.ProjectsRoot, "projects-root", flags.ProjectsRoot, "")
	command.Flags().StringVar(&flags.Filter, "filter", flags.Filter, "")
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs([]string{"name", "--apply", "--config", "config", "--projects-root", "parsed", "--filter", "parsed"})
	command.SilenceErrors = true
	command.SilenceUsage = true
	err := command.Execute()
	coded, ok := err.(*codedError)
	if !ok || coded.code != 1 || !strings.Contains(coded.message, "the recipe reported errors") || flags.ExtraOrgs[0] != "org" {
		t.Fatalf("error%v flags%+v", err, flags)
	}
	for _, args := range [][]string{{"--list"}, {}} {
		deps.Recipes = func(_ context.Context, r runexec.RecipeRequest) (runexec.RecipeResult, error) {
			if r.Name != "" {
				t.Fatal(r)
			}
			return runexec.RecipeResult{Names: []string{"broken", "gated", "readme"}}, nil
		}
		out, _, err := executeTestCommand(t, args, deps)
		if err != nil || out != "broken\ngated\nreadme\n" {
			t.Fatalf("list=%q err%v", out, err)
		}
	}
}
func TestRecipeCommandPreservesDiagnosticAndWriterFailures(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("source failed")
	for _, discovery := range []bool{true, false} {
		deps := fakeDependencies()
		deps.Recipes = func(context.Context, runexec.RecipeRequest) (runexec.RecipeResult, error) {
			return runexec.RecipeResult{}, &runexec.RecipeFailure{Discovery: discovery, Err: sentinel}
		}
		_, stderr, err := executeTestCommand(t, []string{"name"}, deps)
		coded, ok := err.(*codedError)
		if !ok || coded.code != 1 || !strings.Contains(stderr, "source failed") || strings.HasPrefix(stderr, "discovery error:") != discovery {
			t.Fatalf("stderr=%q err=%v", stderr, err)
		}
		command := New(testRuntime(), deps)
		command.SetArgs([]string{"name"})
		command.SetErr(failingWriter{sentinel})
		command.SilenceErrors = true
		command.SilenceUsage = true
		if err := command.Execute(); err != sentinel {
			t.Fatal(err)
		}
	}
	deps := fakeDependencies()
	deps.Recipes = func(_ context.Context, r runexec.RecipeRequest) (runexec.RecipeResult, error) {
		if err := r.Observe(runexec.RecipeEvent{DryRun: true}); err != nil {
			return runexec.RecipeResult{}, err
		}
		return runexec.RecipeResult{}, nil
	}
	command := New(testRuntime(), deps)
	command.SetArgs([]string{"name"})
	command.SetErr(failingWriter{sentinel})
	command.SilenceErrors = true
	command.SilenceUsage = true
	if err := command.Execute(); err != sentinel {
		t.Fatal(err)
	}
	deps.Recipes = func(context.Context, runexec.RecipeRequest) (runexec.RecipeResult, error) {
		return runexec.RecipeResult{Names: []string{"name"}}, nil
	}
	command = New(testRuntime(), deps)
	command.SetArgs([]string{"--list"})
	command.SetOut(failingWriter{sentinel})
	command.SetErr(&bytes.Buffer{})
	command.SilenceErrors = true
	command.SilenceUsage = true
	if err := command.Execute(); err != sentinel {
		t.Fatal(err)
	}
}
func TestRecipeListThreadsItsFlagAndConfigIntoOperation(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	deps.Recipes = func(_ context.Context, r runexec.RecipeRequest) (runexec.RecipeResult, error) {
		if !r.List || r.Name != "" || r.ConfigPath != "configured" {
			t.Fatal(r)
		}
		return runexec.RecipeResult{Names: []string{"refresh-ci"}}, nil
	}
	out, _, err := executeTestCommand(t, []string{"--list", "--config", "configured"}, deps)
	if err != nil || !strings.Contains(out, "refresh-ci") {
		t.Fatalf("output=%q err=%v", out, err)
	}
}
