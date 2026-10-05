package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/depsrun"
	"github.com/sneat-dev/wb/internal/testenv"
)

func TestDepsRootRegistryAndLazyNativeBinding(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	checkout := initTestRepository(t, filepath.Join(root, "acme", "consumer"))
	if err := os.WriteFile(filepath.Join(checkout, "go.mod"), []byte("module github.com/acme/consumer\n\ngo 1.27.0\n\nrequire github.com/acme/provider v1.0.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// The genuine wave engine fetches its configured origin even for dry runs.
	// Keep both the pushed branch and every fetch inside owned private fixtures.
	origin := t.TempDir()
	runGit(t, origin, "init", "--bare", "-b", "main")
	testenv.ConfigureGitAutoMaintenanceOff(t, origin)
	runGit(t, checkout, "config", "user.name", "WB Test")
	runGit(t, checkout, "config", "user.email", "wb-test@example.invalid")
	runGit(t, checkout, "add", "go.mod")
	runGit(t, checkout, "commit", "-m", "seed dependency module")
	runGit(t, checkout, "remote", "add", "origin", origin)
	runGit(t, checkout, "push", "-u", "origin", "main")
	inv := &invocation{projectsRoot: filepath.Join(root, "stale"), nonInteractive: true}
	family := newDepsCmd(inv)
	if len(family.Commands()) != 9 || len(family.Aliases) != 1 || family.Aliases[0] != "dep" {
		t.Fatalf("registry=%v aliases=%v", family.Commands(), family.Aliases)
	}
	for _, name := range []string{"set", "bump", "publish", "graph", "drift", "peers", "propagate", "policy", "go-directive"} {
		child, _, err := family.Find([]string{name})
		if err != nil || child == family {
			t.Fatalf("registered child %q=%v/%v", name, child, err)
		}
	}
	inv.projectsRoot = root
	child, _, err := family.Find([]string{"set"})
	if err != nil {
		t.Fatal(err)
	}
	family.RemoveCommand(child)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	child.SetContext(ctx)
	directory := filepath.Join(root, "report")
	var out, notes bytes.Buffer
	child.SetOut(&out)
	child.SetErr(&notes)
	child.SilenceErrors = true
	child.SilenceUsage = true
	child.SetArgs([]string{"go", "github.com/acme/provider@v1.2.0", checkout, "--dry-run", "--format", "json", "--report-dir", directory})
	if err := child.Execute(); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out.Bytes()) || !strings.Contains(out.String(), "acme/consumer") {
		t.Fatalf("native output=%q", out.String())
	}
	if _, err := os.Stat(filepath.Join(directory, "deps-set.yaml")); err != nil {
		t.Fatalf("native report custody=%v", err)
	}
	// The actual npm selector adapter preserves native identity and selection progress.
	var progress bytes.Buffer
	campaign := newCampaignProgress(&progress, true, "npm selection")
	selected, err := newDependencyService().Select(context.Background(), depsrun.Selection{ProjectsRoot: inv.projectsRoot, RepositoryPath: checkout, Parallel: 1, Progress: campaign.reporter()})
	campaign.finish("selected")
	if err != nil || len(selected) != 1 || selected[0].Slug != "acme/consumer" || !strings.Contains(progress.String(), "select repositories") {
		t.Fatalf("selector=%+v err=%v progress=%q", selected, err, progress.String())
	}
}
