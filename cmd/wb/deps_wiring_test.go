package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/spf13/cobra"
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
	selected, err := dependencyRepositories(inv, []string{"npm", "set", checkout}, depsSetOptions{parallel: 1, campaign: campaign})
	campaign.finish("selected")
	if err != nil || len(selected) != 1 || selected[0].Slug != "acme/consumer" || !strings.Contains(progress.String(), "select repositories") {
		t.Fatalf("selector=%+v err=%v progress=%q", selected, err, progress.String())
	}
}

func TestDepsRootCompositeAdapterKeepsHomeEngineAndReportCustody(t *testing.T) {
	t.Parallel()
	home, engine := t.TempDir(), t.TempDir()
	inv := &invocation{projectsRoot: home, nonInteractive: true}
	command := &cobra.Command{}
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetContext(context.Background())
	events := []deps.ReleaseEvent{{Dependency: "github.com/acme/provider", Version: "v1.2.0", Source: "explicit"}}
	report, directory, err := executeDepsBumpWithRegistryPolicy(inv, command, deps.EcosystemGo, events, nil, depsSetOptions{maxWaves: 1}, deps.Options{GitHubDir: engine, Ref: "main", DryRun: true, Parallel: 1}, true)
	if err != nil || report.Operation == "" || report.GitHubDir != engine || !strings.HasPrefix(directory, filepath.Join(home, ".wb", "reports")) {
		t.Fatalf("report=%+v directory=%q err=%v", report, directory, err)
	}
	persisted, err := deps.LoadBumpReport(directory)
	if err != nil || persisted.Operation != report.Operation || !persisted.RegistryLookupsSkipped {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
}
