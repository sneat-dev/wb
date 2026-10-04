package cmddeps

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	cliprogress "github.com/sneat-dev/wb/internal/cli/progress"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/deps"
	"github.com/spf13/cobra"
)

type cwDepsFailingWriter struct{}

func (cwDepsFailingWriter) Write([]byte) (int, error) { return 0, errors.New("cwDeps: write refused") }
func cwDepsNewOutCommand(out io.Writer) *cobra.Command {
	command := &cobra.Command{Use: "cw-deps-fixture"}
	command.SetOut(out)
	command.SetErr(out)
	return command
}
func testRuntime() shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return shared.Flags{NonInteractive: true} }, ExitError: func(_ int, s string) error { return errors.New(s) }}
}
func cwCovExec(t *testing.T, _ string, build func() *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	cmd := build()
	var out, errout bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errout)
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	return out.String(), errout.String(), err
}
func cwDepsDepsReportFixture() deps.Report {
	return deps.Report{
		SchemaVersion: 1,
		Target:        deps.Target{Dependency: "github.com/acme/lib", Version: "v1.2.3", Ecosystem: deps.EcosystemGo, Resolved: "v1.2.3"},
		Status:        "completed",
		BaseRef:       "main",
		Parallel:      1,
		Repositories: []deps.RepositoryReport{{
			Repository: "acme/app", Status: "updated", Reason: "dependency set",
			Ref: "main", ChangedFiles: []string{"go.mod"},
		}},
	}
}

func cwDepsDriftReportFixture() deps.DriftReport {
	return deps.DriftReport{
		SchemaVersion: 1, Ecosystem: deps.EcosystemGo, Mode: "offline", BaseRef: "main",
		Summary: deps.DriftSummary{Repositories: 1, Dependencies: 1, Converged: 1},
		Groups: []deps.DriftVersionGroup{{
			Dependency: "github.com/acme/lib", Classification: "converged",
			Versions: []deps.DriftVersionUse{{Version: "v1.2.3", Kind: "declared", Repositories: []string{"acme/app"}}},
		}},
	}
}

func cwDepsBumpReportFixture() deps.BumpReport {
	return deps.BumpReport{
		SchemaVersion: 1, Operation: "deps-bump-cwfixture", Status: "completed",
		Ecosystem: deps.EcosystemGo, BaseRef: "main", Parallel: 1,
		SeedEvents: []deps.ReleaseEvent{{Dependency: "github.com/acme/lib", Version: "v1.2.3", Source: "explicit"}},
	}
}

func cwDepsPeerReportFixture() deps.PeerReport {
	return deps.PeerReport{
		SchemaVersion: 1, Package: "@acme/lib", Version: "1.0.0", Against: "/tmp/app",
		Peers:   []deps.PeerRow{{Peer: "react", Required: "^18.0.0", Installed: "18.2.0", Verdict: deps.PeerSatisfied}},
		Summary: deps.PeerSummary{Total: 1, Satisfied: 1},
	}
}

func TestCwDepsWritersRenderEveryFormatAndRefuseUnknown(t *testing.T) {
	type writer struct {
		name  string
		write func(command *cobra.Command, format string) error
	}
	writers := []writer{
		{"deps set", func(command *cobra.Command, format string) error {
			return writeDependencyReport(command, cwDepsDepsReportFixture(), format)
		}},
		{"deps drift", func(command *cobra.Command, format string) error {
			return writeDependencyReport(command, cwDepsDriftReportFixture(), format)
		}},
		{"deps bump", func(command *cobra.Command, format string) error {
			return writeDependencyReport(command, cwDepsBumpReportFixture(), format)
		}},
		{"deps peers", func(command *cobra.Command, format string) error {
			return writeDependencyReport(command, cwDepsPeerReportFixture(), format)
		}},
	}
	for _, w := range writers {
		t.Run(w.name, func(t *testing.T) {
			for _, format := range []string{"markdown", "yaml", "json"} {
				var out bytes.Buffer
				if err := w.write(cwDepsNewOutCommand(&out), format); err != nil {
					t.Fatalf("%s/%s: %v", w.name, format, err)
				}
				if strings.TrimSpace(out.String()) == "" {
					t.Errorf("%s/%s wrote nothing", w.name, format)
				}
				if format == "json" && !json.Valid(out.Bytes()) {
					t.Errorf("%s/%s is not JSON: %s", w.name, format, out.String())
				}
			}
			err := w.write(cwDepsNewOutCommand(&bytes.Buffer{}), "toml")
			if err == nil || !strings.Contains(err.Error(), `unknown --format "toml"`) {
				t.Fatalf("%s unknown format error = %v", w.name, err)
			}
			// A refused stdout write must surface, never be swallowed.
			if err := w.write(cwDepsNewOutCommand(cwDepsFailingWriter{}), "markdown"); err == nil ||
				!strings.Contains(err.Error(), "write refused") {
				t.Fatalf("%s did not surface a failed stdout write: %v", w.name, err)
			}
		})
	}
}

func TestDependencyOptionsNoVerifyForcesValidationModeNone(t *testing.T) {
	inv := shared.Flags{ProjectsRoot: "/tmp/does-not-matter"}
	got := dependencyOptions(inv, depsSetOptions{noVerify: true, validation: string(deps.ValidationModeFull)}, nil)
	if got.ValidationMode != deps.ValidationModeNone {
		t.Fatalf("ValidationMode = %q, want %q", got.ValidationMode, deps.ValidationModeNone)
	}
	if got.Verify {
		t.Fatal("Verify = true with --no-verify, want false")
	}
}

func TestDependencyValidationModesKeepFastBoundToExactPRHeadCI(t *testing.T) {
	tests := []struct {
		name       string
		options    depsSetOptions
		flags      map[string]string
		wantMode   deps.ValidationMode
		wantChecks int
		wantError  string
	}{
		{name: "full default", options: depsSetOptions{validation: "full"}, wantMode: deps.ValidationModeFull, wantChecks: 3},
		{name: "fast merged", options: depsSetOptions{validation: "fast", merge: true}, flags: map[string]string{"validation": "fast"}, wantMode: deps.ValidationModeFast},
		{name: "fast pull request", options: depsSetOptions{validation: "fast", pr: true}, flags: map[string]string{"validation": "fast"}, wantMode: deps.ValidationModeFast},
		{name: "fast dry run", options: depsSetOptions{validation: "fast", dryRun: true}, flags: map[string]string{"validation": "fast"}, wantMode: deps.ValidationModeFast},
		{name: "fast without publication", options: depsSetOptions{validation: "fast"}, flags: map[string]string{"validation": "fast"}, wantError: "requires --pr or --merge"},
		{name: "fast with local checks", options: depsSetOptions{validation: "fast", merge: true, checks: "lint"}, flags: map[string]string{"validation": "fast", "checks": "lint"}, wantError: "cannot be used together"},
		{name: "legacy no verify", options: depsSetOptions{validation: "full", noVerify: true}, wantMode: deps.ValidationModeNone},
		{name: "legacy no verify with validation", options: depsSetOptions{validation: "fast", noVerify: true}, flags: map[string]string{"validation": "fast"}, wantError: "cannot be used together"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := newBump(testRuntime(), testOperations())
			for name, value := range test.flags {
				if err := command.Flags().Set(name, value); err != nil {
					t.Fatal(err)
				}
			}
			mode, checks, err := dependencyValidationOptions(command, test.options)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("validation error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil || mode != test.wantMode || len(checks) != test.wantChecks {
				t.Fatalf("validation = mode %q checks %v err %v, want mode %q and %d checks", mode, checks, err, test.wantMode, test.wantChecks)
			}
		})
	}
}

func TestDependencyValidationOptionsNoVerifyRejectsChecks(t *testing.T) {
	command := &cobra.Command{Use: "bump"}
	var checks string
	command.Flags().StringVar(&checks, "checks", "", "checks")
	if err := command.Flags().Set("checks", "lint"); err != nil {
		t.Fatalf("setting --checks: %v", err)
	}
	_, _, err := dependencyValidationOptions(command, depsSetOptions{noVerify: true})
	if err == nil || !strings.Contains(err.Error(), "--no-verify and --checks cannot be used together") {
		t.Fatalf("dependencyValidationOptions(noVerify, --checks changed) = %v, want the no-verify/--checks refusal", err)
	}
}

func TestDepsBumpRefusesAScopeWithoutLatest(t *testing.T) {
	bump := newBump(testRuntime(), testOperations())
	bump.SetArgs([]string{"npm", "--fleet", "--changed", "@acme/core@0.1.0", "--scope", "@acme/*"})
	bump.SetOut(io.Discard)
	bump.SetErr(io.Discard)
	bump.SilenceUsage = true
	err := bump.Execute()
	if err == nil || !strings.Contains(err.Error(), "--latest") || !strings.Contains(err.Error(), "--changed") {
		t.Fatalf("deps bump --scope without --latest error = %v, want a refusal naming both ways out", err)
	}
}

func TestDepsBumpRefusesLatestWithoutAScope(t *testing.T) {
	bump := newBump(testRuntime(), testOperations())
	bump.SetArgs([]string{"npm", "--fleet", "--latest", "--dry-run"})
	bump.SetOut(io.Discard)
	bump.SetErr(io.Discard)
	bump.SilenceUsage = true
	if err := bump.Execute(); err == nil || !strings.Contains(err.Error(), "--scope") {
		t.Fatalf("deps bump --latest without --scope error = %v, want a refusal naming --scope", err)
	}
}

func TestDepsCommandExposesCumulativeLifecycleFlags(t *testing.T) {
	t.Parallel()
	command := newSet(testRuntime(), testOperations())
	for _, name := range []string{"commit", "push", "pr", "merge", "parallel", "resume", "retry", "timeout", "propagate", "max-waves", "release-poll", "refresh-after", "dependency-order", "layer", "validation"} {
		if command.Flags().Lookup(name) == nil {
			t.Errorf("deps set is missing --%s", name)
		}
	}
}

func TestDepsCommandIncludesBumpWithWaveLifecycleFlags(t *testing.T) {
	t.Parallel()
	command := New(testRuntime(), testOperations())
	bump, _, err := command.Find([]string{"bump"})
	if err != nil || bump == command {
		t.Fatalf("find bump: command=%q, error=%v", bump.Name(), err)
	}
	for _, name := range []string{"changed", "fleet", "parallel", "max-waves", "release-poll", "refresh-after", "resume", "commit", "push", "pr", "merge", "validation"} {
		if bump.Flags().Lookup(name) == nil {
			t.Errorf("deps bump is missing --%s", name)
		}
	}
}

func TestDepsCommandIncludesGraphViewsAndBrowserReportFlags(t *testing.T) {
	t.Parallel()
	command := New(testRuntime(), testOperations())
	graph, _, err := command.Find([]string{"graph"})
	if err != nil || graph == command {
		t.Fatalf("find graph: command=%q, error=%v", graph.Name(), err)
	}
	for _, name := range []string{"fleet", "match", "regex", "ref", "parallel", "dependency", "view", "format", "report-dir", "open"} {
		if graph.Flags().Lookup(name) == nil {
			t.Errorf("deps graph is missing --%s", name)
		}
	}
}

func TestDepsDriftFleetWithRepositoryPathIsRejected(t *testing.T) {
	_, _, err := cwCovExec(t, t.TempDir(), func() *cobra.Command { return newDrift(testRuntime(), testOperations()) }, "--fleet", "some/path")
	if err == nil || !strings.Contains(err.Error(), "repository-path cannot be used with --fleet") {
		t.Fatalf("wb deps drift --fleet some/path: err = %v, want the repository-path/--fleet refusal", err)
	}
}

func TestDepsGraphAndBumpAcceptNpmEcosystem(t *testing.T) {
	t.Parallel()
	graph := newGraph(testRuntime(), testOperations())
	graph.SetArgs([]string{"--ecosystem", "cobol", "--fleet"})
	graph.SetOut(io.Discard)
	graph.SetErr(io.Discard)
	graph.SilenceUsage = true
	if err := graph.Execute(); err == nil || !strings.Contains(err.Error(), "go and npm ecosystems") {
		t.Fatalf("deps graph --ecosystem cobol error = %v, want a go/npm ecosystem rejection", err)
	}

	bump := newBump(testRuntime(), testOperations())
	bump.SetArgs([]string{"cobol", "--fleet", "--changed", "example.com/a@v1.0.0"})
	bump.SetOut(io.Discard)
	bump.SetErr(io.Discard)
	bump.SilenceUsage = true
	if err := bump.Execute(); err == nil || !strings.Contains(err.Error(), "go and npm ecosystems") {
		t.Fatalf("deps bump cobol error = %v, want a go/npm ecosystem rejection", err)
	}
}

func TestDepsSetRejectsUnusableDependencyOrderCombinations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args    []string
		message string
	}{
		{args: []string{"go", "example.com/a@v1.0.0", "--layer", "0"}, message: "--layer requires --dependency-order"},
		{args: []string{"github-actions", "acme/cicd@v1.0.0", "--dependency-order"}, message: "only for the go ecosystem"},
		{args: []string{"go", "example.com/a@v1.0.0", "--dependency-order", "--propagate", "--fleet"}, message: "cannot be used together"},
		{args: []string{"go", "example.com/a@v1.0.0", "--dependency-order", "--layer", "two"}, message: "invalid layer selection"},
	}
	for _, test := range tests {
		command := newSet(testRuntime(), testOperations())
		command.SetArgs(test.args)
		command.SetOut(io.Discard)
		command.SetErr(io.Discard)
		command.SilenceUsage = true
		err := command.Execute()
		if err == nil || !strings.Contains(err.Error(), test.message) {
			t.Errorf("deps set %v error = %v, want %q", test.args, err, test.message)
		}
	}
}
func testOperations() Dependencies { return Dependencies{Campaign: cliprogress.NewCampaign} }
