package cmdfleet

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/mergepolicy"
	"github.com/spf13/cobra"
)

func TestFleetDefaultBranchHelpAndPolicyPrecedence(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{}
	runtime := testRuntime(&flags)
	deps := fakeDependencies()
	_ = runtime
	_ = deps

	command := NewDefaultBranch(runtime, deps)
	for _, name := range []string{"apply", "branch", "org", "repo", "user", "all-orgs", "parallel", "report-dir", "reconcile-from", "reconcile-sha256", "temporarily-unarchive", "migrate-pages-source", "rewrite-workflow-triggers", "restore-archive-from", "restore-archive-sha256", "format", "json"} {
		if command.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s", name)
		}
	}
	if !strings.Contains(command.Long, "read-only") || !strings.Contains(command.Long, "never rewrites") || !strings.Contains(command.Long, "accepted response") {
		t.Fatal("help omits safety contract")
	}
}

func TestFleetDefaultBranchRejectsUnsafeFlagCombinations(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{}
	runtime := testRuntime(&flags)
	deps := fakeDependencies()
	_ = runtime
	_ = deps

	for name, args := range map[string][]string{
		"missing apply scope":          {"default-branch", "--apply"},
		"repo and org":                 {"default-branch", "--repo", "acme/app", "--org", "acme"},
		"all orgs and org":             {"default-branch", "--all-orgs", "--org", "acme"},
		"invalid parallel":             {"default-branch", "--repo", "acme/app", "--parallel", "0"},
		"resume without apply":         {"default-branch", "--reconcile-from", "receipt.json"},
		"digest without receipt":       {"default-branch", "--reconcile-sha256", strings.Repeat("a", 64)},
		"receipt without valid digest": {"default-branch", "--apply", "--repo", "acme/app", "--reconcile-from", "receipt.json"},
	} {
		t.Run(name, func(t *testing.T) {
			command := quietCommand(New(runtime, deps))
			command.SetOut(&bytes.Buffer{})
			command.SetErr(&bytes.Buffer{})
			command.SetArgs(args)
			if err := command.Execute(); err == nil {
				t.Fatalf("unsafe flags were accepted: %v", args)
			}
		})
	}
}

func TestFleetDefaultBranchRequiresDigestForReconciliationReceipt(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{}
	runtime := testRuntime(&flags)
	deps := fakeDependencies()
	_ = runtime
	_ = deps

	root := quietCommand(New(runtime, deps))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"default-branch", "--repo", "acme/app", "--apply", "--reconcile-from", "receipt.json"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "--reconcile-sha256") {
		t.Fatalf("missing receipt digest was accepted: %v", err)
	}
}

func TestCwFleetRequestedDefaultBranchOwnersReadsTheRootOrg(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{}
	runtime := testRuntime(&flags)
	deps := fakeDependencies()
	_ = runtime
	_ = deps

	command := NewDefaultBranch(runtime, deps)
	if got := requestedOwners(runtime, command, []string{"local"}); len(got) != 1 || got[0] != "local" {
		t.Fatalf("command-local owners = %v", got)
	}
	root := &cobra.Command{Use: "wb"}
	root.PersistentFlags().StringArray("org", nil, "additional owner")
	defaultBranch := NewDefaultBranch(runtime, deps)
	root.AddCommand(defaultBranch)
	if err := root.PersistentFlags().Set("org", "root-org"); err != nil {
		t.Fatal(err)
	}
	flags.ExtraOrgs = []string{"root-org"}
	got := requestedOwners(runtime, defaultBranch, []string{"local"})
	if len(got) != 2 || got[1] != "root-org" {
		t.Fatalf("root owners = %v", got)
	}
}

func TestDefaultBranchCLIRejectsUnsafeArchiveAndPagesFlagCombinations(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{}
	runtime := testRuntime(&flags)
	deps := fakeDependencies()
	_ = runtime
	_ = deps

	digest := strings.Repeat("a", 64)
	for _, test := range []struct {
		name, want string
		args       []string
	}{
		{"restore requires apply", "--restore-archive-from requires --apply", []string{"--restore-archive-from", "receipt.json"}},
		{"restore requires digest", "--restore-archive-sha256", []string{"--apply", "--repo", "acme/app", "--restore-archive-from", "receipt.json"}},
		{"orphan restore digest", "--restore-archive-sha256 requires", []string{"--restore-archive-sha256", digest}},
		{"restore excludes migration flags", "requires exactly one --repo", []string{"--apply", "--repo", "acme/app", "--restore-archive-from", "receipt.json", "--restore-archive-sha256", digest, "--branch", "main"}},
		{"Pages migration excludes archived transition", "--migrate-pages-source does not support archived", []string{"--repo", "acme/app", "--migrate-pages-source", "--temporarily-unarchive"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := quietCommand(New(runtime, deps))
			var stdout, stderr bytes.Buffer
			command.SetOut(&stdout)
			command.SetErr(&stderr)
			command.SetArgs(append([]string{"default-branch"}, test.args...))
			if err := command.Execute(); err == nil || !strings.Contains(err.Error(), test.want) || stdout.Len() != 0 {
				t.Fatalf("unsafe args %q: stdout=%q stderr=%q err=%v, want %q", test.args, stdout.String(), stderr.String(), err, test.want)
			}
		})
	}
}

func TestFleetMergePolicyHelpAndFlags(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{}
	runtime := testRuntime(&flags)
	deps := fakeDependencies()
	_ = runtime
	_ = deps

	command := NewMergePolicy(runtime, deps)
	for _, name := range []string{"apply", "org", "repo", "user", "parallel", "report-dir", "resume", "format", "json"} {
		if command.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s", name)
		}
	}
	for _, phrase := range []string{"read-only", "required-linear-history", "merge-queue", "never weakens", "organization", "enterprise"} {
		if !strings.Contains(command.Long, phrase) {
			t.Errorf("help does not explain %q", phrase)
		}
	}
	if got := command.Flags().Lookup("apply").DefValue; got != "false" {
		t.Fatalf("--apply default = %s", got)
	}
	if got, want := command.Flags().Lookup("parallel").DefValue, strconv.Itoa(min(deps.Budget(), 16)); got != want {
		t.Fatalf("--parallel default = %s, want WB CPU budget %s", got, want)
	}
}

func TestFleetMergePolicyApplyRequiresExplicitScope(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{}
	runtime := testRuntime(&flags)
	deps := fakeDependencies()
	_ = runtime
	_ = deps

	command := NewMergePolicy(runtime, deps)
	if err := command.Flags().Set("apply", "true"); err != nil {
		t.Fatal(err)
	}
	err := command.RunE(command, nil)
	if err == nil || !strings.Contains(err.Error(), "explicit --org, --repo, or --user") {
		t.Fatalf("error = %v", err)
	}
}

func TestCwDepsPrintMergePolicyReportRendersEveryDisposition(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{}
	runtime := testRuntime(&flags)
	deps := fakeDependencies()
	_ = runtime
	_ = deps

	report := cwDepsMergePolicyReportFixture()
	var out bytes.Buffer
	if err := printMergePolicyReport(&out, report); err != nil {
		t.Fatalf("printMergePolicyReport: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"Merge policy: check",
		"4 repositories · 1 compliant · 1 drift · 1 blocked · 1 errors · 0 applied",
		"compliant acme/clean",
		"merge commits only; PR title + PR body",
		"drift     acme/drift",
		"blocked   acme/conflict",
		"error     acme/broken",
		"decode repository settings",
		"organization ruleset 9 (2 selected)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("merge policy report missing %q:\n%s", want, text)
		}
	}
	// A persisted report names its own path.
	report.ReportPath = "/tmp/merge-policy.json"
	out.Reset()
	if err := printMergePolicyReport(&out, report); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Report: /tmp/merge-policy.json") {
		t.Errorf("report path missing:\n%s", out.String())
	}
	if err := printMergePolicyReport(&defaultBranchFailWriter{failAt: 1}, report); err == nil {
		t.Error("a failed report write must be surfaced")
	}
}

func TestCwDepsRequestedMergePolicyOwnersReadsTheRootOrg(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{}
	runtime := testRuntime(&flags)
	deps := fakeDependencies()
	_ = runtime
	_ = deps

	command := NewMergePolicy(runtime, deps)
	if got := requestedOwners(runtime, command, []string{"local"}); len(got) != 1 || got[0] != "local" {
		t.Fatalf("command-local owners = %v", got)
	}
	root := &cobra.Command{Use: "wb"}
	root.PersistentFlags().StringArray("org", nil, "additional owner")
	mergePolicy := NewMergePolicy(runtime, deps)
	root.AddCommand(mergePolicy)
	if err := root.PersistentFlags().Set("org", "root-org"); err != nil {
		t.Fatal(err)
	}
	flags.ExtraOrgs = []string{"root-org"}
	got := requestedOwners(runtime, mergePolicy, []string{"local"})
	if len(got) != 2 || got[1] != "root-org" {
		t.Fatalf("root owners = %v", got)
	}
}

func TestCwDepsMergePolicyCommandUsageRefusals(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{}
	runtime := testRuntime(&flags)
	deps := fakeDependencies()
	_ = runtime
	_ = deps

	tests := map[string]struct {
		args []string
		want string
	}{
		"repo with org":        {[]string{"--repo", "acme/app", "--org", "acme"}, "--repo cannot be combined"},
		"zero parallel":        {[]string{"--parallel", "0"}, "--parallel must be between"},
		"resume without apply": {[]string{"--resume", "--report-dir", "/tmp/r"}, "--resume requires --apply"},
		"apply without scope":  {[]string{"--apply"}, "--apply requires explicit"},
		"invalid repo":         {[]string{"--repo", "not-a-slug"}, "invalid --repo"},
	}

	deps.MergePolicy = func(_ context.Context, r mergepolicy.Request, _ io.Writer) (mergepolicy.Report, error) {
		if len(r.Repositories) == 1 && r.Repositories[0] == "not-a-slug" {
			return mergepolicy.Report{}, errors.New("invalid --repo")
		}
		t.Fatal("operation reached for usage error")
		return mergepolicy.Report{}, nil
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			stdout, _, err := execute(t, NewMergePolicy(runtime, deps), test.args...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("args %v error = %v, want %q\n%s", test.args, err, test.want, stdout)
			}
		})
	}
}
