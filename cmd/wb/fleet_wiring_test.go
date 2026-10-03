package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/defaultbranch"
	"github.com/sneat-dev/wb/internal/mergepolicy"
)

func TestFleetDefaultBranchRootFilterRestrictsExactRepositoryScope(t *testing.T) {
	testProjectsRoot := t.TempDir()
	t.Chdir(testProjectsRoot)
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

func TestFleetRootBindingsRetainNeutralDiscoveryAndPolicyAuthorities(t *testing.T) {
	root := cwCovProjectsRoot(t, "acme/app")
	t.Chdir(root)
	cwCovFakeGH(t, "fleet-user", []string{"acme"}, `[]`)
	deps := fleetCommandDependencies()
	owners, diagnostics := deps.InventoryOwners([]string{"explicit"})
	if len(owners) != 3 || len(diagnostics) != 0 {
		t.Fatal(owners, diagnostics)
	}
	actualOwners := fleetOwners([]string{"explicit"})
	if len(actualOwners) != 3 {
		t.Fatal(actualOwners)
	}
	repos, err := fleet(root, "acme/", func() []string { return []string{"acme"} })
	if err != nil || len(repos) != 1 || repos[0].Slug() != "acme/app" {
		t.Fatal(repos, err)
	}
	var progress bytes.Buffer
	merge, err := deps.MergePolicy(t.Context(), mergepolicy.Request{Scope: mergepolicy.Scope{ProjectsRoot: root}, Options: mergepolicy.Options{Repositories: []string{"acme/app"}, Parallel: 1}}, &progress)
	if err != nil || merge.Summary.Inspected != 1 {
		t.Fatal(merge, err)
	}
	branch, err := deps.DefaultBranch(t.Context(), defaultbranch.Request{Scope: defaultbranch.Scope{ProjectsRoot: root}, Options: defaultbranch.Options{Repositories: []string{"acme/app"}, Branch: "main", Parallel: 1}}, &progress)
	if err != nil || branch.Summary.Blocked != 1 || len(branch.Repositories) != 1 || branch.Repositories[0].Disposition != "blocked" || !strings.Contains(branch.Repositories[0].Error, "empty repository has no default branch") {
		t.Fatal(branch, err)
	}
	command := newFleetCmd(testInvocation(t, root))
	for _, name := range []string{"overview", "stats", "status", "prs", "merge-policy", "default-branch", "coverage"} {
		child, _, err := command.Find([]string{name})
		if err != nil || child == command {
			t.Fatal(name, err)
		}
	}
}
