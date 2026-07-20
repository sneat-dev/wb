package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/hooks"
)

func TestMetricBar(t *testing.T) {
	for _, test := range []struct {
		value int
		want  string
	}{
		{value: 0, want: "·"},
		{value: 3, want: "███"},
		{value: 99, want: strings.Repeat("█", 20)},
	} {
		if got := metricBar(test.value); got != test.want {
			t.Fatalf("metricBar(%d) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestLocalHookReposFiltersAndSorts(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{
		"z-org/beta/.git",
		"a-org/alpha/.git",
		"a-org/ignored",
	} {
		if err := os.MkdirAll(filepath.Join(root, relative), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	repos, err := localHookRepos(root, "a-org/")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Slug() != "a-org/alpha" {
		t.Fatalf("localHookRepos() = %#v, want only a-org/alpha", repos)
	}

	repos, err = localHookRepos(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 || repos[0].Slug() != "a-org/alpha" || repos[1].Slug() != "z-org/beta" {
		t.Fatalf("localHookRepos() = %#v, want sorted repositories", repos)
	}
}

func TestApplyAndCheckHooksFleet(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{"acme/alpha", "acme/beta"} {
		repo := filepath.Join(root, relative)
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		command := exec.Command("git", "init", "-b", "main")
		command.Dir = repo
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git init %s: %v\n%s", repo, err, output)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	previousRoot, previousFilter := projectsRoot, filterFlag
	projectsRoot, filterFlag = root, "acme/"
	t.Cleanup(func() {
		projectsRoot, filterFlag = previousRoot, previousFilter
	})

	command := &cobra.Command{}
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	if err := applyHooksFleet(command, "", false, false); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "acme/alpha") || !strings.Contains(got, "acme/beta") || !strings.Contains(got, "Processed 2 repositories; 0 failed") {
		t.Fatalf("fleet install output:\n%s", got)
	}

	output.Reset()
	if err := checkHooksFleet(command, "", false); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "Checked 2 repositories; 0 problems") {
		t.Fatalf("fleet check output:\n%s", got)
	}
}

func TestPrintHookMetricsExplainsPushAttempts(t *testing.T) {
	cmd := &cobra.Command{}
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := printHookMetrics(cmd, hooks.MetricsSummary{
		From:         "2026-07-19",
		Through:      "2026-07-20",
		Commits:      2,
		PushAttempts: 1,
		HookRuns:     3,
		Days: []hooks.DailyMetrics{
			{Date: "2026-07-19", Commits: 2},
			{Date: "2026-07-20", PushAttempts: 1},
		},
	}, "/tmp/events.jsonl"); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, wanted := range []string{"2 commits", "1 push attempts", "Git has no post-push hook", "/tmp/events.jsonl"} {
		if !strings.Contains(got, wanted) {
			t.Fatalf("output missing %q:\n%s", wanted, got)
		}
	}
}
