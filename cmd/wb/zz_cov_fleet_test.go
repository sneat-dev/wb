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

func TestCwCovReportPrintAndRecordBuckets(t *testing.T) {
	var rep report
	// record() streams immediately, so the whole sequence is captured: that
	// streaming is the point (progress is visible as each repo completes).
	out := cwCovCaptureStdout(t, func() {
		rep.record(&rep.updated, "✓", "acme/b")
		rep.record(&rep.updated, "✓", "acme/a")
		rep.record(&rep.skipped, "·", "acme/skipped")
		rep.record(&rep.forked, "+", "acme/fork")
		rep.record(&rep.archived, "×", "acme/archived")
		rep.record(&rep.errors, "✗", "zebra failure")
		rep.record(&rep.errors, "✗", "alpha failure")
		rep.print()
	})

	if len(rep.updated) != 2 || rep.updated[0] != "acme/b" || rep.updated[1] != "acme/a" {
		t.Fatalf("record did not append in call order: %+v", rep.updated)
	}

	for _, want := range []string{
		"Summary", "Updated  2", "Skipped  1", "Forks    1", "Archived 1", "Errors   2",
		"✓ acme/a", "✓ acme/b", "· acme/skipped", "+ acme/fork", "× acme/archived",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report output missing %q:\n%s", want, out)
		}
	}
	// Errors are sorted in the summary so a rerun reads the same and the list
	// is scannable. The streamed per-repo lines above keep completion order.
	summary := out[strings.Index(out, "Summary"):]
	if strings.Index(summary, "alpha failure") > strings.Index(summary, "zebra failure") {
		t.Errorf("errors are not sorted:\n%s", summary)
	}
}

func TestCwCovPluralSuffixAndFleetRegex(t *testing.T) {
	for count, want := range map[int]string{0: "s", 1: "", 2: "s", 9: "s"} {
		if got := pluralSuffix(count); got != want {
			t.Errorf("pluralSuffix(%d) = %q, want %q", count, got, want)
		}
	}
	for count, want := range map[int]string{0: "ies", 1: "y", 3: "ies"} {
		if got := plural(count); got != want {
			t.Errorf("plural(%d) = %q, want %q", count, got, want)
		}
	}
	if expression, err := compileFleetRegex(""); err != nil || expression != nil {
		t.Fatalf("empty pattern = (%v, %v), want (nil, nil)", expression, err)
	}
	expression, err := compileFleetRegex(`^acme/`)
	if err != nil || expression == nil || !expression.MatchString("acme/app") {
		t.Fatalf("valid pattern = (%v, %v)", expression, err)
	}
	if _, err := compileFleetRegex(`(`); err == nil || !strings.Contains(err.Error(), "invalid --regex") {
		t.Fatalf("invalid pattern error = %v, want a named --regex error", err)
	}
}

func TestCwCovSummarizeGitStatsCountsEveryStatus(t *testing.T) {
	stats := summarizeGitStats([]repositoryStatusInfo{
		{Status: "clean"},
		{Status: "attention"},
		{Status: "attention"},
		{Status: "error"},
		{Status: "something-else"},
	})
	if stats.Inspected != 5 || stats.Clean != 1 || stats.Attention != 2 || stats.Error != 1 {
		t.Fatalf("stats = %+v, want inspected=5 clean=1 attention=2 error=1", stats)
	}
}

func TestCwCovFleetSummarySentences(t *testing.T) {
	remote := fleetRemoteStats{
		WouldClone: 1, WouldPull: 2, SkippedDirty: 3, Ignored: 4, EmptyRemote: 5,
		ArchivedUnlandable: 6, LocalOnly: 7, RemoteOnly: 8, NoOp: 9, Error: 10,
	}
	sentences := map[string]string{
		"inventory": fleetInventorySummary(fleetInventoryStats{Organizations: 1, Repositories: 2}),
		"git":       fleetGitSummary(fleetGitStats{Attention: 1, Clean: 2, Error: 3, Inspected: 6}),
		"layout":    fleetLayoutSummary(fleetLayoutStats{OK: 1, TopLevel: 2, Misowned: 3, NoOrigin: 4, Unreadable: 5}),
		"remote":    fleetRemoteSummary(remote),
		"hooks":     fleetHooksSummary(fleetHooksStats{Findings: 1, Repositories: 1, Errors: 1}),
		"worktrees": fleetWorktreeSummary(fleetWorktreeStats{Tasks: 1, Checkouts: 2, Dirty: 3, Locked: 4}),
	}
	for name, sentence := range sentences {
		if strings.TrimSpace(sentence) == "" {
			t.Errorf("%s summary is empty", name)
		}
	}
	if !strings.Contains(sentences["inventory"], "1 organization · 2 local repositorys") {
		t.Errorf("singular/plural inventory = %q", sentences["inventory"])
	}
	for _, want := range []string{"1 would-clone", "2 would-pull", "10 error"} {
		if !strings.Contains(sentences["remote"], want) {
			t.Errorf("remote summary missing %q: %q", want, sentences["remote"])
		}
	}
	if !strings.Contains(sentences["hooks"], "1 finding across 1 repository (1 error)") {
		t.Errorf("hooks summary = %q", sentences["hooks"])
	}
	if !strings.Contains(sentences["worktrees"], "1 task · 2 checkouts · 3 dirty · 4 locked") {
		t.Errorf("worktree summary = %q", sentences["worktrees"])
	}
}

func TestCwCovFleetMarkdownRendersOptionalSections(t *testing.T) {
	stats := fleetStatsReport{
		SchemaVersion: 1,
		Inventory:     fleetInventoryStats{Organizations: 1, Repositories: 3},
		Git:           fleetGitStats{Inspected: 3, Clean: 2, Attention: 1},
		Layout:        fleetLayoutStats{OK: 3},
		Worktrees:     fleetWorktreeStats{Tasks: 1, Checkouts: 1},
	}
	withoutOptional := fleetStatsMarkdown(stats)
	if strings.Contains(withoutOptional, "- remote:") || strings.Contains(withoutOptional, "- hooks:") {
		t.Fatalf("optional sections rendered without data:\n%s", withoutOptional)
	}
	stats.Remote = &fleetRemoteStats{NoOp: 3}
	stats.Hooks = &fleetHooksStats{Repositories: 3, Findings: 2}
	withOptional := fleetStatsMarkdown(stats)
	for _, want := range []string{"# WB fleet stats", "- remote:", "- hooks:", "2 findings across 3 repositorys"} {
		if !strings.Contains(withOptional, want) {
			t.Errorf("stats markdown missing %q:\n%s", want, withOptional)
		}
	}

	overview := fleetOverviewMarkdown(fleetOverviewReport{
		SchemaVersion: 1,
		Stats:         stats,
		Status: statusIndex{SchemaVersion: 1, Repositories: []repositoryStatusInfo{
			{Repository: "acme/dirty", Status: "attention", Summary: "modified"},
		}},
	}, true)
	for _, want := range []string{"# WB fleet overview", "## Stats", "## Attention", "acme/dirty"} {
		if !strings.Contains(overview, want) {
			t.Errorf("overview markdown missing %q:\n%s", want, overview)
		}
	}
}

func TestCwCovWriteFleetStatsOutputFormats(t *testing.T) {
	report := fleetStatsReport{SchemaVersion: 1, Inventory: fleetInventoryStats{Repositories: 2}}
	reportDir := filepath.Join(t.TempDir(), "reports")

	for _, format := range []string{"markdown", "yaml", "json"} {
		var out string
		var err error
		out = cwCovCaptureStdout(t, func() {
			err = writeFleetStatsOutput(report, format, reportDir)
		})
		if err != nil {
			t.Fatalf("format %s: %v", format, err)
		}
		if strings.TrimSpace(out) == "" {
			t.Errorf("format %s produced no stdout", format)
		}
	}
	for _, name := range []string{"fleet-stats.md", "fleet-stats.yaml"} {
		data, readErr := os.ReadFile(filepath.Join(reportDir, name))
		if readErr != nil {
			t.Fatalf("report file %s: %v", name, readErr)
		}
		if !strings.Contains(string(data), "schema_version") && !strings.Contains(string(data), "# WB fleet stats") {
			t.Errorf("%s does not contain the report: %s", name, data)
		}
	}
	err := writeFleetStatsOutput(report, "toml", "")
	if err == nil || !strings.Contains(err.Error(), `unknown --format "toml"`) {
		t.Fatalf("unknown format error = %v", err)
	}
}

func TestCwCovWriteFleetOverviewOutputFormats(t *testing.T) {
	report := fleetOverviewReport{SchemaVersion: 1, Stats: fleetStatsReport{Inventory: fleetInventoryStats{Repositories: 1}}}
	reportDir := filepath.Join(t.TempDir(), "reports")
	for _, format := range []string{"markdown", "yaml", "json"} {
		var err error
		out := cwCovCaptureStdout(t, func() {
			err = writeFleetOverviewOutput(report, format, reportDir, false)
		})
		if err != nil {
			t.Fatalf("format %s: %v", format, err)
		}
		if strings.TrimSpace(out) == "" {
			t.Errorf("format %s produced no stdout", format)
		}
	}
	for _, name := range []string{"fleet-overview.md", "fleet-overview.yaml"} {
		if _, err := os.Stat(filepath.Join(reportDir, name)); err != nil {
			t.Fatalf("report file %s: %v", name, err)
		}
	}
	if err := writeFleetOverviewOutput(report, "toml", "", false); err == nil {
		t.Fatal("unknown format was accepted")
	}
}

func TestCwCovFleetInventoryWorktreesAndLayout(t *testing.T) {
	t.Setenv("WB_HOME", t.TempDir())
	root := cwCovProjectsRoot(t, "acme/app", "acme/other", "beta/tool")
	options := qualityOptions{parallel: 2}

	inventory, err := fleetInventory(root, "", options)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Organizations != 2 || inventory.Repositories != 3 {
		t.Fatalf("inventory = %+v, want 2 orgs / 3 repos", inventory)
	}
	filtered, err := fleetInventory(root, "acme/app", options)
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Repositories != 1 || filtered.Organizations != 1 {
		t.Fatalf("filtered inventory = %+v, want 1 org / 1 repo", filtered)
	}
	byRegex, err := fleetInventory(root, "", qualityOptions{parallel: 2, regex: `^beta/`})
	if err != nil {
		t.Fatal(err)
	}
	if byRegex.Repositories != 1 {
		t.Fatalf("regex inventory = %+v, want 1 repo", byRegex)
	}
	if _, err := fleetInventory(root, "", qualityOptions{parallel: 2, regex: `(`}); err == nil {
		t.Fatal("an invalid --regex must be refused")
	}

	worktreeStats, err := fleetWorktreeRollup(root, "", options)
	if err != nil {
		t.Fatal(err)
	}
	if worktreeStats.Checkouts != 0 || worktreeStats.Tasks != 0 {
		t.Fatalf("worktrees = %+v, want no WB-managed worktrees", worktreeStats)
	}

	layoutStats, err := fleetLayoutRollup(root)
	if err != nil {
		t.Fatal(err)
	}
	if layoutStats.NoOrigin+layoutStats.OK+layoutStats.TopLevel+layoutStats.Misowned+layoutStats.Unreadable != 3 {
		t.Fatalf("layout = %+v, want all three checkouts classified", layoutStats)
	}

	hooksStats, err := fleetHooksRollup(&invocation{}, []qualityTarget{{repository: "acme/app", path: filepath.Join(root, "acme", "app")}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if hooksStats.Repositories != 1 {
		t.Fatalf("hooks = %+v, want one repository inspected", hooksStats)
	}
}

func TestCwCovFleetOwnersAndFleetDiscovery(t *testing.T) {
	root := cwCovProjectsRoot(t, "acme/app")
	cwCovFakeGH(t, "cwcov-user", []string{"cwcov-org", "other-org"},
		`[{"name":"remote-only","isArchived":false,"isFork":false,"sshUrl":"git@example.test:acme/remote-only.git"}]`)

	owners := fleetOwners([]string{"cwcov-extra"})
	assertContains := func(list []string, want string) {
		t.Helper()
		for _, item := range list {
			if item == want {
				return
			}
		}
		t.Errorf("owners %v missing %q", list, want)
	}
	assertContains(owners, "cwcov-user")
	assertContains(owners, "cwcov-org")
	assertContains(owners, "other-org")
	assertContains(owners, "cwcov-extra")

	repos, err := fleet(root, "", func() []string { return []string{"acme"} })
	if err != nil {
		t.Fatal(err)
	}
	slugs := map[string]bool{}
	for _, repo := range repos {
		slugs[repo.Slug()] = true
	}
	if !slugs["acme/app"] {
		t.Errorf("local repo missing from fleet result: %v", slugs)
	}
	if !slugs["acme/remote-only"] {
		t.Errorf("remote repo missing from fleet result: %v", slugs)
	}

	filtered, err := fleet(root, "app", func() []string { return []string{"acme"} })
	if err != nil {
		t.Fatal(err)
	}
	for _, repo := range filtered {
		if !strings.Contains(repo.Slug(), "app") {
			t.Errorf("filter leaked %q", repo.Slug())
		}
	}
}

func TestCwCovFleetOwnersFallsBackToExtraOrgsOnGHFailure(t *testing.T) {
	binDir := t.TempDir()
	if err := testenv.WriteExecutableFile(filepath.Join(binDir, "gh"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WB_HOME", t.TempDir())

	owners := fleetOwners([]string{"only-extra"})
	if len(owners) != 1 || owners[0] != "only-extra" {
		t.Fatalf("owners = %v, want just the explicit extra org when discovery fails", owners)
	}
}

func TestCwCovFleetCommandsEmitReportsInProcess(t *testing.T) {
	t.Setenv("WB_HOME", t.TempDir())
	root := cwCovProjectsRoot(t, "acme/clean", "acme/dirty")
	if err := os.WriteFile(filepath.Join(root, "acme", "dirty", "notes.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reportDir := filepath.Join(t.TempDir(), "reports")

	// Stats: the JSON path prints through os.Stdout, so capture it.
	var stdout string
	var code int
	stdout = cwCovCaptureStdout(t, func() {
		_, _, code = cwCovRun(t, "fleet", "stats", "--projects-root", root, "--format", "json", "--report-dir", reportDir)
	})
	if code != exitOK {
		t.Fatalf("fleet stats exit = %d", code)
	}
	var stats fleetStatsReport
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

	// Overview: markdown goes to os.Stdout too.
	stdout = cwCovCaptureStdout(t, func() {
		_, _, code = cwCovRun(t, "fleet", "overview", "--projects-root", root, "--all")
	})
	if code != exitOK {
		t.Fatalf("fleet overview exit = %d", code)
	}
	for _, want := range []string{"# WB fleet overview", "## Stats", "## Attention", "acme/dirty"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("overview stdout missing %q:\n%s", want, stdout)
		}
	}

	// Status: the worklist is delivered on the command's own writer.
	_, _, code = cwCovRun(t, "fleet", "status", "--projects-root", root, "--format", "json")
	if code != exitOK {
		t.Fatalf("fleet status exit = %d", code)
	}

	// YAML and unknown-format handling.
	stdout = cwCovCaptureStdout(t, func() {
		_, _, code = cwCovRun(t, "fleet", "stats", "--projects-root", root, "--format", "yaml")
	})
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
	cwCovFakeGH(t, "cwcov-user", nil, `[]`)

	var stdout string
	var code int
	stdout = cwCovCaptureStdout(t, func() {
		_, _, code = cwCovRun(t, "fleet", "stats", "--projects-root", root, "--format", "json", "--remote", "--hooks")
	})
	if code != exitOK && code != exitFindings {
		t.Fatalf("fleet stats --remote --hooks exit = %d\n%s", code, stdout)
	}
	var stats fleetStatsReport
	if err := json.Unmarshal([]byte(stdout), &stats); err != nil {
		t.Fatalf("stats JSON: %v\n%s", err, stdout)
	}
	if stats.Remote == nil || stats.Hooks == nil {
		t.Fatalf("--remote/--hooks did not populate depth sections: %+v", stats)
	}
	if stats.Hooks.Repositories != 1 {
		t.Fatalf("hooks rollup = %+v, want the single local repo", stats.Hooks)
	}

	// fleetRemoteRollup is also drivable directly.
	remote, err := fleetRemoteRollup(root, "", qualityOptions{parallel: 1})
	if err != nil {
		t.Fatal(err)
	}
	if remote.LocalOnly != 1 {
		t.Fatalf("remote rollup = %+v, want one local-only repository", remote)
	}
	if _, err := fleetRemoteRollup(root, "", qualityOptions{parallel: 1, regex: `(`}); err == nil {
		t.Fatal("an invalid --regex must be refused by fleetRemoteRollup")
	}
}

func TestCwCovResolvePRInventoryOwnersDedupesAndDiagnoses(t *testing.T) {
	cwCovFakeGH(t, "Cw-User", []string{"cw-org", "CW-ORG"}, `[]`)
	owners, diagnostics := resolvePRInventoryOwners([]string{"extra-org", "  ", "CW-ORG"})
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diagnostics)
	}
	got := map[string]string{}
	for _, owner := range owners {
		got[owner.Login] = owner.Qualifier
	}
	if len(owners) != 3 {
		t.Fatalf("owners = %+v, want user + one org (case-insensitive dedupe) + extra", owners)
	}
	if got["Cw-User"] != "user" || got["cw-org"] != "org" || got["extra-org"] != "org" {
		t.Fatalf("owners = %+v", owners)
	}
	// Sorted by qualifier:login so the snapshot is deterministic.
	for i := 1; i < len(owners); i++ {
		if owners[i-1].String() > owners[i].String() {
			t.Fatalf("owners are not sorted: %+v", owners)
		}
	}

	// With discovery broken the failure must be reported, not silently ignored.
	binDir := t.TempDir()
	if err := testenv.WriteExecutableFile(filepath.Join(binDir, "gh"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WB_HOME", t.TempDir())
	owners, diagnostics = resolvePRInventoryOwners(nil)
	if len(owners) != 0 {
		t.Fatalf("owners = %+v, want none when discovery fails", owners)
	}
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %+v, want one per failed discovery", diagnostics)
	}
}

func TestCwCovWritePRInventoryOutput(t *testing.T) {
	report := prinventory.Report{SchemaVersion: 1, Complete: true}
	reportDir := filepath.Join(t.TempDir(), "reports")
	for _, format := range []string{"markdown", "json"} {
		command := newFleetPRsCmd(&invocation{})
		var out bytes.Buffer
		command.SetOut(&out)
		command.SetErr(&out)
		if err := writePRInventoryOutput(command, report, format, reportDir); err != nil {
			t.Fatalf("format %s: %v", format, err)
		}
		if strings.TrimSpace(out.String()) == "" {
			t.Errorf("format %s produced no output", format)
		}
	}
	for _, name := range []string{"pull-request-inventory.json", "pull-request-inventory.md"} {
		data, err := os.ReadFile(filepath.Join(reportDir, name))
		if err != nil {
			t.Fatalf("report file %s: %v", name, err)
		}
		if strings.TrimSpace(string(data)) == "" {
			t.Errorf("%s is empty", name)
		}
	}
	command := newFleetPRsCmd(&invocation{})
	if err := writePRInventoryOutput(command, report, "toml", ""); err == nil ||
		!strings.Contains(err.Error(), `unsupported --format "toml"`) {
		t.Fatalf("unsupported format error = %v", err)
	}
}

func TestCwCovFleetPRsCommandInProcess(t *testing.T) {
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
