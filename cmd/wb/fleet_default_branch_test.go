package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/defaultbranch"
	"github.com/spf13/cobra"
)

func TestFleetDefaultBranchHelpAndPolicyPrecedence(t *testing.T) {
	command := newFleetDefaultBranchCmd(&invocation{})
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
	for name, args := range map[string][]string{
		"missing apply scope":          {"fleet", "default-branch", "--apply"},
		"repo and org":                 {"fleet", "default-branch", "--repo", "acme/app", "--org", "acme"},
		"all orgs and org":             {"fleet", "default-branch", "--all-orgs", "--org", "acme"},
		"invalid parallel":             {"fleet", "default-branch", "--repo", "acme/app", "--parallel", "0"},
		"resume without apply":         {"fleet", "default-branch", "--reconcile-from", "receipt.json"},
		"digest without receipt":       {"fleet", "default-branch", "--reconcile-sha256", strings.Repeat("a", 64)},
		"receipt without valid digest": {"fleet", "default-branch", "--apply", "--repo", "acme/app", "--reconcile-from", "receipt.json"},
	} {
		t.Run(name, func(t *testing.T) {
			command := newRootCmd()
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
	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fleet", "default-branch", "--repo", "acme/app", "--apply", "--reconcile-from", "receipt.json"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "--reconcile-sha256") {
		t.Fatalf("missing receipt digest was accepted: %v", err)
	}
}

func TestFleetDefaultBranchRootFilterRestrictsExactRepositoryScope(t *testing.T) {
	originalConfig := defaultbranch.ConfigPath
	t.Cleanup(func() {
		defaultbranch.ConfigPath = originalConfig
	})
	testProjectsRoot := t.TempDir()
	defaultbranch.ConfigPath = func() string { return filepath.Join(t.TempDir(), "absent.yaml") }
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"--projects-root", testProjectsRoot, "--filter", "selected", "fleet", "default-branch", "--repo", "acme/other", "--branch", "main", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"inspected": 0`) {
		t.Fatalf("root --filter did not restrict exact scope: %s", out.String())
	}
}

// TestCwFleetRequestedDefaultBranchOwnersReadsTheRootOrg proves that
// --org is read from both the command-local selection and, once the root
// persistent --org was actually set, the invocation's extraOrgs.
func TestCwFleetRequestedDefaultBranchOwnersReadsTheRootOrg(t *testing.T) {
	inv := &invocation{}
	command := newFleetDefaultBranchCmd(inv)
	if got := requestedDefaultBranchOwners(inv, command, []string{"local"}); len(got) != 1 || got[0] != "local" {
		t.Fatalf("command-local owners = %v", got)
	}
	root := &cobra.Command{Use: "wb"}
	root.PersistentFlags().StringArray("org", nil, "additional owner")
	defaultBranch := newFleetDefaultBranchCmd(inv)
	root.AddCommand(defaultBranch)
	if err := root.PersistentFlags().Set("org", "root-org"); err != nil {
		t.Fatal(err)
	}
	inv.extraOrgs = []string{"root-org"}
	got := requestedDefaultBranchOwners(inv, defaultBranch, []string{"local"})
	if len(got) != 2 || got[1] != "root-org" {
		t.Fatalf("root owners = %v", got)
	}
}
