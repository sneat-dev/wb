package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/fleetinspect"

	"github.com/sneat-dev/wb/internal/prinventory"
	"github.com/sneat-dev/wb/internal/testenv"
)

// cwCovRun drives the whole CLI in-process. run() mutates process globals
// (commandStarted, the session resolver, WB_EXECUTABLE), so every caller must
// be a non-parallel test; testenv.Isolate restores the ambient agent env.
func cwCovRun(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	testenv.Isolate(t)
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

// cwCovCaptureStdout swaps os.Stdout for a pipe while fn runs, so a function
// that prints with fmt.Print can be asserted on. Only ever called from
// non-parallel tests: Go resumes parallel tests only after every sequential
// test in the package has returned.
func cwCovCaptureStdout(t *testing.T, fn func()) string {
	t.Helper()
	previous := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	restore := func() { os.Stdout = previous }
	defer restore()
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(reader)
		done <- string(data)
	}()
	fn()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	restore()
	out := <-done
	_ = reader.Close()
	return out
}

// cwCovFakeGH installs a hermetic fake `gh` on PATH. It answers the three
// shapes wb's discovery/inventory code uses: `api user` and `api user/orgs`
// (HTTP-include framing, because those go through githubobserver.Get), raw
// JSON for every other `api` endpoint (githubobserver.Read), and a repo list.
func cwCovFakeGH(t *testing.T, user string, orgs []string, remoteReposJSON string) {
	t.Helper()
	binDir := t.TempDir()
	orgsJSON, err := json.Marshal(cwCovOrgLogins(orgs))
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "api" ]; then
  case "$2" in
    user)
      printf 'HTTP/2 200 OK\n\n{"login":"%s"}\n'
      exit 0
      ;;
    user/orgs)
      printf 'HTTP/2 200 OK\n\n%s\n'
      exit 0
      ;;
    *)
      printf '{"total_count":0,"items":[]}\n'
      exit 0
      ;;
  esac
fi
if [ "$1" = "repo" ] && [ "$2" = "list" ]; then
  printf '%%s\n' '%s'
  exit 0
fi
printf '{"total_count":0,"items":[]}\n'
exit 0
`, user, string(orgsJSON), remoteReposJSON)
	if err := testenv.WriteExecutableFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("WB_HOME", t.TempDir())
}

func cwCovOrgLogins(orgs []string) []map[string]string {
	out := make([]map[string]string, 0, len(orgs))
	for _, org := range orgs {
		out = append(out, map[string]string{"login": org})
	}
	return out
}

// cwCovProjectsRoot builds a two-level {org}/{repo} tree of real git clones.
func cwCovProjectsRoot(t *testing.T, repos ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, slug := range repos {
		initTestRepository(t, filepath.Join(root, filepath.FromSlash(slug)))
	}
	return root
}

func TestCwCovFleetCommandsEmitReportsInProcess(t *testing.T) {
	t.Setenv("WB_HOME", t.TempDir())
	root := cwCovProjectsRoot(t, "acme/clean", "acme/dirty")
	t.Chdir(root)
	if err := os.WriteFile(filepath.Join(root, "acme", "dirty", "notes.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reportDir := filepath.Join(t.TempDir(), "reports")

	// Stats JSON uses the command-bound output writer.
	var stdout string
	var code int
	stdout, _, code = cwCovRun(t, "fleet", "stats", "--projects-root", root, "--format", "json", "--report-dir", reportDir)
	if code != exitOK {
		t.Fatalf("fleet stats exit = %d", code)
	}
	var stats fleetinspect.StatsReport
	if err := json.Unmarshal([]byte(stdout), &stats); err != nil {
		t.Fatalf("fleet stats stdout is not JSON: %v\n%s", err, stdout)
	}
	if stats.Git.Inspected != 2 || stats.Git.Attention != 1 || stats.Inventory.Repositories != 2 {
		t.Fatalf("stats = %+v", stats)
	}
	for _, name := range []string{"fleet-stats.md", "fleet-stats.yaml"} {
		if _, err := os.Stat(filepath.Join(reportDir, name)); err != nil {
			t.Errorf("fleet stats --report-dir did not write %s: %v", name, err)
		}
	}

	// Overview markdown uses the same command-bound writer.
	stdout, _, code = cwCovRun(t, "fleet", "overview", "--projects-root", root, "--all")
	if code != exitOK {
		t.Fatalf("fleet overview exit = %d", code)
	}
	for _, want := range []string{"# WB fleet overview", "## Stats", "## Attention", "acme/dirty"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("overview stdout missing %q:\n%s", want, stdout)
		}
	}

	// Status: the worklist is delivered on the command's own writer.
	stdout, _, code = cwCovRun(t, "fleet", "status", "--projects-root", root, "--format", "json")
	if code != exitOK {
		t.Fatalf("fleet status exit = %d", code)
	}

	if !strings.Contains(stdout, `"repository": "acme/dirty"`) {
		t.Fatalf("fleet status bound output = %s", stdout)
	}

	// YAML and unknown-format handling.
	stdout, _, code = cwCovRun(t, "fleet", "stats", "--projects-root", root, "--format", "yaml")
	if code != exitOK || !strings.Contains(stdout, "schema_version") {
		t.Fatalf("fleet stats --format yaml: exit=%d stdout=%s", code, stdout)
	}
	_, stderr, code := cwCovRun(t, "fleet", "stats", "--projects-root", root, "--format", "toml")
	if code != exitFindings || !strings.Contains(stderr, "unknown --format") {
		t.Fatalf("fleet stats --format toml: exit=%d stderr=%s", code, stderr)
	}
	_, _, code = cwCovRun(t, "fleet", "stats", "--projects-root", root, "--regex", "(")
	if code != exitFindings {
		t.Fatalf("fleet stats --regex ( exit = %d, want a findings error", code)
	}
}

func TestCwCovFleetRemoteAndHooksDepth(t *testing.T) {
	root := cwCovProjectsRoot(t, "acme/app")
	t.Chdir(root)
	cwCovFakeGH(t, "cwcov-user", nil, `[]`)

	var stdout string
	var code int
	stdout, _, code = cwCovRun(t, "fleet", "stats", "--projects-root", root, "--format", "json", "--remote", "--hooks")
	if code != exitOK && code != exitFindings {
		t.Fatalf("fleet stats --remote --hooks exit = %d\n%s", code, stdout)
	}
	var stats fleetinspect.StatsReport
	if err := json.Unmarshal([]byte(stdout), &stats); err != nil {
		t.Fatalf("stats JSON: %v\n%s", err, stdout)
	}
	if stats.Remote == nil || stats.Hooks == nil {
		t.Fatalf("--remote/--hooks did not populate depth sections: %+v", stats)
	}
	if stats.Hooks.Repositories != 1 {
		t.Fatalf("hooks rollup = %+v, want the single local repo", stats.Hooks)
	}

}

func TestCwCovFleetPRsCommandInProcess(t *testing.T) {
	t.Chdir(t.TempDir())
	cwCovFakeGH(t, "cwcov-user", []string{"cwcov-org"}, `[]`)
	// The report is written to the command's own stdout, which run() wires to
	// the buffer it hands back.
	stdout, stderr, code := cwCovRun(t, "fleet", "prs", "--format", "json")
	if code != exitOK && code != exitFindings {
		t.Fatalf("fleet prs exit = %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var report prinventory.Report
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("fleet prs stdout is not JSON: %v\n%s", err, stdout)
	}
	if len(report.Owners) != 2 {
		t.Fatalf("owners = %+v, want the user and the org", report.Owners)
	}

	_, _, code = cwCovRun(t, "fleet", "prs", "--format", "toml")
	if code != exitFindings {
		t.Fatalf("fleet prs --format toml exit = %d, want a findings error", code)
	}
}
