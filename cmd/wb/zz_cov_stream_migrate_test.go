package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/migrate"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/streamsync"
	"github.com/spf13/cobra"
)

func TestCwCovParseLibraryTargets(t *testing.T) {
	parsed, err := parseLibraryTargets([]string{
		"github.com/acme/library/backend@v0.6.0",
		"@acme/package@1.2.3",
		"lodash@4.17.21",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 3 {
		t.Fatalf("parsed = %+v, want three libraries", parsed)
	}
	if parsed[0].Name != "github.com/acme/library/backend" || parsed[0].Target != "v0.6.0" ||
		parsed[0].Ecosystem != string(streams.EcosystemGo) {
		t.Errorf("go library = %+v", parsed[0])
	}
	// A scoped npm package starts with @, so the separator is the LAST @.
	if parsed[1].Name != "@acme/package" || parsed[1].Target != "1.2.3" ||
		parsed[1].Ecosystem != string(streams.EcosystemNpm) {
		t.Errorf("scoped npm library = %+v", parsed[1])
	}
	if parsed[2].Name != "lodash" || parsed[2].Ecosystem != string(streams.EcosystemNpm) {
		t.Errorf("bare npm library = %+v", parsed[2])
	}

	for _, bad := range []string{"no-version", "@1.2.3", "name@", "@"} {
		if _, err := parseLibraryTargets([]string{bad}); err == nil || !strings.Contains(err.Error(), "must be <name>@<version>") {
			t.Errorf("parseLibraryTargets(%q) error = %v, want a named usage error", bad, err)
		}
	}
	if targets, err := parseLibraryTargets(nil); err != nil || len(targets) != 0 {
		t.Fatalf("empty input = (%+v, %v)", targets, err)
	}
}

func TestCwCovPrintStreamSyncRendersEveryRowShape(t *testing.T) {
	results := []streamsync.Result{
		{
			Stream: "checkout-rewrite", Repository: "acme/app",
			RecordedRemoteHead: "remote-head", RemoteAdvanced: true,
			StreamRebase: streamsync.RebaseResult{Branch: "stream/checkout-rewrite", Rebased: true},
			AgentRebases: []streamsync.RebaseResult{
				{Branch: "agent/one", Agent: "lane-1", Rebased: true},
				{Branch: "agent/two", Agent: "lane-2", Conflicts: []string{"pkg/a.go"}},
				{Branch: "agent/three", Agent: "lane-3", Detail: "could not rebase"},
			},
			Bumps: []streamsync.BumpResult{{
				Action: "bumped", Library: streamsync.Library{Name: "github.com/acme/lib", Target: "v1.2.3"},
			}},
			Batch: &streamsync.BatchResult{
				Passed: true, Runs: 2,
				Elements: []streamsync.Element{{Name: "a"}, {Name: "b"}},
				Skipped:  []string{"-race"}, Unguarded: []string{"e2e"}, Unverified: []string{"fuzz"},
			},
			Unpushed:    streamsync.UnpushedReport{Repository: "acme/app", Branch: "stream/x", Commits: 2},
			PushSkipped: "push not justified by a named trigger",
			Push:        &streamsync.PushDecision{SHA: "abc123", Trigger: streamsync.TriggerExplicit, Reason: "hand-off"},
			Errors:      []string{"bump failed"},
			BaseBefore:  "before",
			BaseAfter:   "after",
		},
	}

	command := newStreamSyncCmd(&invocation{})
	var out bytes.Buffer
	command.SetOut(&out)
	// The rich result carries a conflicting agent rebase, so reporting it is a
	// findings exit; the rendering is what this assertion is about.
	if err := printStreamSync(command, "text", results); exitCodeOf(t, err) != exitFindings {
		t.Fatalf("rich result error = %v, want findings", err)
	}
	text := out.String()
	for _, want := range []string{
		"acme/app on stream/checkout-rewrite",
		"fast-forwarded to fetched remote head remote-head",
		"rebased", "CONFLICT: pkg/a.go", "failed: could not rebase",
		"bumped", "github.com/acme/lib", "v1.2.3",
		"batch passed in 2 run(s) over 2 element(s)",
		"skipped: -race", "UNGUARDED: e2e", "UNVERIFIED: fuzz",
		"pushed abc123: trigger=", "! bump failed",
		"push not justified by a named trigger",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("stream sync text missing %q:\n%s", want, text)
		}
	}

	// A failed result must turn into exit findings, not a silent success.
	failing := []streamsync.Result{{StreamRebase: streamsync.RebaseResult{Branch: "stream/x"}}}
	if err := printStreamSync(command, "text", failing); exitCodeOf(t, err) != exitFindings {
		t.Fatalf("un-rebased stream did not report findings: %v", err)
	}
	// A clean result is a success.
	clean := []streamsync.Result{{
		Repository:   "acme/app",
		StreamRebase: streamsync.RebaseResult{Branch: "stream/ok", Rebased: true},
		Unpushed:     streamsync.UnpushedReport{Repository: "acme/app"},
	}}
	out.Reset()
	if err := printStreamSync(command, "text", clean); err != nil {
		t.Fatalf("clean stream reported an error: %v", err)
	}
	if !strings.Contains(out.String(), "nothing unpushed") {
		t.Errorf("clean stream output = %q", out.String())
	}

	out.Reset()
	if err := printStreamSync(command, "json", results); exitCodeOf(t, err) != exitFindings {
		t.Fatalf("json rich result error = %v, want findings", err)
	}
	var decoded []streamsync.Result
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("stream sync JSON: %v\n%s", err, out.String())
	}
	if len(decoded) != 1 || decoded[0].Repository != "acme/app" {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestCwCovPrintBatchRendersCulpritAndScanLimit(t *testing.T) {
	command := newStreamSyncCmd(&invocation{})
	var out bytes.Buffer
	command.SetOut(&out)
	batch := streamsync.BatchResult{
		Passed: false, Runs: 3,
		Elements:           []streamsync.Element{{Name: "a"}, {Name: "b"}, {Name: "c"}},
		Culprit:            &streamsync.Element{Name: "b", SHA: "sha-b"},
		ProvenGood:         []streamsync.Element{{Name: "a"}},
		FailingCheck:       "test",
		Skipped:            []string{"-race"},
		Unguarded:          []string{"e2e"},
		Unverified:         []string{"fuzz"},
		UnexaminedElements: 1,
	}
	if err := printBatch(&out, batch); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"batch FAILED in 3 run(s) over 3 element(s)",
		"culprit: b (test)", "proven good: 1 element(s) before it",
		"skipped: -race", "UNGUARDED: e2e", "UNVERIFIED: fuzz",
		"never examined",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("batch text missing %q:\n%s", want, text)
		}
	}

	// The interaction-failure path states that no element was the cause.
	out.Reset()
	if err := printBatch(&out, streamsync.BatchResult{
		Passed: false, Runs: 3, Elements: []streamsync.Element{{Name: "a"}},
		InteractionFailure: true, FailingCheck: "test",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "interaction failure") {
		t.Errorf("interaction failure not reported:\n%s", out.String())
	}
}

func TestCwCovRecordStreamSyncRemoteHeadReportsUnknownMember(t *testing.T) {
	store := streams.OpenAt(filepath.Join(t.TempDir(), "streams"))
	if _, err := store.Create(streams.Stream{
		Name: "cw-cov", Members: []streams.Member{{
			Repository: "acme/app", Role: streams.RoleConsumer, Branch: "stream/cw-cov",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := recordStreamSyncRemoteHead(store, "cw-cov", "acme/absent", "head"); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown member error = %v, want a named refusal", err)
	}
	// Recorded heads are matched case-insensitively, the way a remote's
	// canonical casing may differ from a hand-written stream file.
	if err := recordStreamSyncRemoteHead(store, "cw-cov", "ACME/APP", "fresh-head"); err != nil {
		t.Fatalf("case-insensitive update: %v", err)
	}
	persisted, err := store.Load("cw-cov")
	if err != nil {
		t.Fatal(err)
	}
	if member, ok := persisted.Member("acme/app"); !ok || member.Lease.RecordedHead != "fresh-head" {
		t.Fatalf("persisted member = %+v", member)
	}
}

func TestCwCovStreamEventSinkAppendsToTheStreamLog(t *testing.T) {
	store := streams.OpenAt(filepath.Join(t.TempDir(), "streams"))
	sink := streamEventSink{log: store.EventLog("cw-cov")}
	if err := sink.Append(streamsync.Event{
		Stream: "cw-cov", Verb: "sync", Phase: "rebase", Repository: "acme/app",
		Outcome: "ok", Detail: "rebased", Evidence: map[string]string{"head": "sha"},
	}); err != nil {
		t.Fatal(err)
	}
	events, err := streams.ReadEvents(store.EventLog("cw-cov").Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want one recorded event", events)
	}
	if events[0].Stream != "cw-cov" || events[0].Verb != "sync" || events[0].Repository != "acme/app" ||
		events[0].Outcome != "ok" || len(events[0].Evidence) != 1 {
		t.Fatalf("recorded event = %+v", events[0])
	}
}

func TestCwCovWorkflowMechanismsReadsPullRequestWorkflows(t *testing.T) {
	empty := t.TempDir()
	present, opaque, err := workflowMechanisms{}.Present(empty)
	if err != nil {
		t.Fatal(err)
	}
	if len(present) != 0 || opaque {
		t.Fatalf("no-workflow dir = (%v, %v)", present, opaque)
	}

	root := t.TempDir()
	workflow := filepath.Join(root, ".github", "workflows", "stream.yml")
	if err := os.MkdirAll(filepath.Dir(workflow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workflow, []byte(`name: stream
on:
  pull_request:
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: go build ./...
      - run: go vet ./...
      - run: go test -count=1 ./...
`), 0o644); err != nil {
		t.Fatal(err)
	}
	present, _, err = workflowMechanisms{}.Present(root)
	if err != nil {
		t.Fatal(err)
	}
	if !present["go vet"] || !present["-count=1"] {
		t.Fatalf("a pull-request workflow with vet/count invocations reported %v", present)
	}
}

func TestCwCovStreamSyncCommandUsageRefusals(t *testing.T) {
	root := t.TempDir()

	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newStreamSyncCmd(testInvocation(t, root)) }, "cw-cov", "--library", "no-version")
	if code := exitCodeOf(t, err); code != exitUsage {
		t.Fatalf("bad --library exit = %d\n%s", code, stdout)
	}
	if !strings.Contains(err.Error(), "must be <name>@<version>") {
		t.Errorf("bad --library error = %v", err)
	}
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newStreamSyncCmd(testInvocation(t, root)) }, "cw-cov", "--format", "toml"); err == nil ||
		!strings.Contains(err.Error(), `unsupported format "toml"`) {
		t.Fatalf("bad --format error = %v", err)
	}
	// A stream that does not exist is an error, never a silent no-op.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newStreamSyncCmd(testInvocation(t, root)) }, "absent-stream"); err == nil {
		t.Fatal("syncing an unknown stream must fail")
	}
}

const cwCovMigrationSpec = `
format = "https://sneat.dev/workbench/formats/migration/v1"

migration "cw-cov-migration" {
  title = "Coverage migration"

  scope {
    languages = ["go"]
  }

  text_replace "go" {
    from = "OLDNAME"
    to   = "NEWNAME"
  }
}
`

// cwCovHierarchicalMigrationSpec names its own module through an import step:
// a campaign refuses a migration that references no Go module at all.
const cwCovHierarchicalMigrationSpec = `
format = "https://sneat.dev/workbench/formats/migration/v1"

migration "cw-cov-migration" {
  title = "Coverage migration"

  scope {
    languages = ["go"]
  }

  import_replace "go" {
    from = "github.com/acme/sample/old"
    to   = "github.com/acme/sample/new"
  }
}
`

func cwCovMigrationSource(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module github.com/acme/sample\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "value.go"), []byte("package sample\n\nvar OLDNAME = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestCwCovParseModuleRefs(t *testing.T) {
	refs, err := parseModuleRefs([]string{"github.com/acme/lib=v1.2.3", "github.com/acme/other=main"})
	if err != nil {
		t.Fatal(err)
	}
	if refs["github.com/acme/lib"] != "v1.2.3" || refs["github.com/acme/other"] != "main" || len(refs) != 2 {
		t.Fatalf("refs = %+v", refs)
	}
	for _, value := range []string{"noref", "=v1", "mod=", ""} {
		if _, err := parseModuleRefs([]string{value}); err == nil {
			t.Errorf("parseModuleRefs(%q) accepted an invalid pair", value)
		}
	}
	if _, err := parseModuleRefs([]string{"a=v1", "a=v2"}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestCwCovRunMigratePlansChecksAndApplies(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "migration.hcl")
	if err := os.WriteFile(specPath, []byte(cwCovMigrationSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	root := cwCovMigrationSource(t)

	// Dry run reports the change and writes nothing.
	planned := filepath.Join(t.TempDir(), "planned")
	if code := cwCovCaptureStdoutInt(t, func() int {
		return runMigrate(specPath, []string{root}, false, false, "markdown", planned)
	}); code != 0 {
		t.Fatalf("dry run exit = %d, want 0", code)
	}
	body, err := os.ReadFile(filepath.Join(root, "value.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "OLDNAME") {
		t.Fatalf("dry run changed the source: %s", body)
	}
	if _, err := os.Stat(filepath.Join(planned, "migration.md")); err != nil {
		t.Errorf("dry run did not write its report: %v", err)
	}

	// --check exits 1 when changes would be made.
	if code := cwCovCaptureStdoutInt(t, func() int {
		return runMigrate(specPath, []string{root}, false, true, "json", "")
	}); code != 1 {
		t.Fatalf("--check exit = %d, want 1", code)
	}

	// --apply writes the change and exits 0.
	applied := filepath.Join(t.TempDir(), "applied")
	if code := cwCovCaptureStdoutInt(t, func() int {
		return runMigrate(specPath, []string{root}, true, false, "markdown", applied)
	}); code != 0 {
		t.Fatalf("--apply exit = %d, want 0", code)
	}
	body, err = os.ReadFile(filepath.Join(root, "value.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "NEWNAME") || strings.Contains(string(body), "OLDNAME") {
		t.Fatalf("--apply did not rewrite the source: %s", body)
	}
	if _, err := os.Stat(filepath.Join(applied, "migration.md")); err != nil {
		t.Errorf("--apply did not write the report directory: %v", err)
	}

	// --apply and --check together are a usage error.
	if code := cwCovCaptureStdoutInt(t, func() int {
		return runMigrate(specPath, []string{root}, true, true, "markdown", "")
	}); code != 2 {
		t.Fatalf("--apply --check exit = %d, want 2", code)
	}
	// An unreadable spec is a usage error, not a silent pass.
	if code := cwCovCaptureStdoutInt(t, func() int {
		return runMigrate(filepath.Join(t.TempDir(), "absent.hcl"), []string{root}, false, false, "markdown", "")
	}); code != 2 {
		t.Fatalf("missing spec exit = %d, want 2", code)
	}
	// An unknown format is refused after planning (the tree is already migrated,
	// so this is a format refusal rather than a findings exit).
	if code := cwCovCaptureStdoutInt(t, func() int {
		return runMigrate(specPath, []string{root}, false, false, "toml", "")
	}); code != 2 {
		t.Fatalf("unknown format exit = %d, want 2", code)
	}
	// A source root that cannot be scanned is a usage error.
	if code := cwCovCaptureStdoutInt(t, func() int {
		return runMigrate(specPath, []string{filepath.Join(t.TempDir(), "absent")}, false, false, "markdown", "")
	}); code != 2 {
		t.Fatalf("missing root exit = %d, want 2", code)
	}
}

func TestCwCovWriteMigrationAndCampaignReports(t *testing.T) {
	spec := migrate.Spec{Format: "https://sneat.dev/workbench/formats/migration/v1", ID: "cw-cov", Title: "Coverage"}
	report := migrate.NewReport(spec, migrate.Plan{}, []string{t.TempDir()}, "planned")
	for _, format := range []string{"markdown", "yaml", "json"} {
		var err error
		out := cwCovCaptureStdout(t, func() { err = writeMigrationReport(report, format, "") })
		if err != nil {
			t.Fatalf("migration report %s: %v", format, err)
		}
		if strings.TrimSpace(out) == "" {
			t.Errorf("migration report %s produced no output", format)
		}
	}
	if err := writeMigrationReport(report, "toml", ""); err == nil {
		t.Fatal("unknown migration format was accepted")
	}

	campaign := migrate.CampaignReport{
		SchemaVersion: 1,
		Migration:     migrate.ReportMigration{ID: "cw-cov", Format: spec.Format},
		Status:        "completed",
		SourceRoot:    "/tmp/src",
		GitHubDir:     "/tmp/github",
		BaseRef:       "main",
		Parallel:      1,
	}
	for _, format := range []string{"markdown", "yaml", "json"} {
		var err error
		out := cwCovCaptureStdout(t, func() { err = writeCampaignReport(campaign, format) })
		if err != nil {
			t.Fatalf("campaign report %s: %v", format, err)
		}
		if strings.TrimSpace(out) == "" {
			t.Errorf("campaign report %s produced no output", format)
		}
	}
	if err := writeCampaignReport(campaign, "toml"); err == nil {
		t.Fatal("unknown campaign format was accepted")
	}
}

func TestCwCovRunHierarchicalMigrationRefusalsAndCleanup(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "migration.hcl")
	if err := os.WriteFile(specPath, []byte(cwCovMigrationSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	root := cwCovMigrationSource(t)
	githubDir := t.TempDir()
	t.Setenv("WB_HOME", t.TempDir())

	cases := []struct {
		name    string
		roots   []string
		options hierarchicalMigrationOptions
		want    int
	}{
		{
			name:    "cleanup cannot be combined with apply",
			options: hierarchicalMigrationOptions{cleanup: true, apply: true},
			want:    2,
		},
		{
			name:    "check is not supported hierarchically",
			options: hierarchicalMigrationOptions{check: true},
			want:    2,
		},
		{
			name:    "no-verify and verify conflict",
			options: hierarchicalMigrationOptions{noVerify: true, verifyExplicit: true},
			want:    2,
		},
		{
			name:    "invalid module ref",
			options: hierarchicalMigrationOptions{moduleRefs: []string{"broken"}},
			want:    2,
		},
		{
			name:    "cleanup refuses a source root",
			roots:   []string{root},
			options: hierarchicalMigrationOptions{cleanup: true},
			want:    2,
		},
		{
			name:    "hierarchical requires exactly one root",
			options: hierarchicalMigrationOptions{},
			want:    2,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			options := test.options
			options.githubDir = githubDir
			options.format = "json"
			if code := cwCovCaptureStdoutInt(t, func() int {
				return runHierarchicalMigration(&invocation{}, specPath, test.roots, options)
			}); code != test.want {
				t.Fatalf("exit = %d, want %d", code, test.want)
			}
		})
	}

	// An unreadable spec is refused after the option checks.
	if code := cwCovCaptureStdoutInt(t, func() int {
		return runHierarchicalMigration(&invocation{}, filepath.Join(t.TempDir(), "absent.hcl"), nil,
			hierarchicalMigrationOptions{githubDir: githubDir, cleanup: true})
	}); code != 2 {
		t.Fatalf("missing spec exit = %d, want 2", code)
	}

	// Cleanup with no campaign worktrees is a successful, empty pass.
	if code := cwCovCaptureStdoutInt(t, func() int {
		return runHierarchicalMigration(&invocation{}, specPath, nil, hierarchicalMigrationOptions{cleanup: true, githubDir: githubDir})
	}); code != 0 {
		t.Fatalf("cleanup exit = %d, want 0", code)
	}

	// A campaign whose target module is already cloned locally needs no
	// network: the isolated worktree is cut from the local canonical clone and
	// verification is skipped explicitly.
	hierarchicalSpec := filepath.Join(t.TempDir(), "hierarchical.hcl")
	if err := os.WriteFile(hierarchicalSpec, []byte(cwCovHierarchicalMigrationSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	canonicalRoot := t.TempDir()
	cwCovCloneWithOrigin(t, filepath.Join(t.TempDir()), "sample", filepath.Join(canonicalRoot, "acme", "sample"))
	reportDir := filepath.Join(t.TempDir(), "campaign")
	if code := cwCovCaptureStdoutInt(t, func() int {
		return runHierarchicalMigration(&invocation{}, hierarchicalSpec, []string{root}, hierarchicalMigrationOptions{
			githubDir: canonicalRoot, ref: "main", format: "markdown", reportDir: reportDir,
			noVerify: true, progressOut: &bytes.Buffer{},
		})
	}); code != 0 {
		t.Fatalf("hierarchical campaign exit = %d, want 0", code)
	}
	for _, name := range []string{"campaign.md", "campaign.yaml"} {
		if _, err := os.Stat(filepath.Join(reportDir, name)); err != nil {
			t.Errorf("campaign report %s was not written: %v", name, err)
		}
	}
}

// --github-dir defaults to --projects-root when the flag is omitted; every
// other hierarchical-migration test above passes --github-dir explicitly, so
// this is the only one that exercises the fallback itself.
func TestCwCovRunHierarchicalMigrationDefaultsGithubDirToProjectsRoot(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "migration.hcl")
	if err := os.WriteFile(specPath, []byte(cwCovMigrationSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	projectsRoot := t.TempDir()
	code := cwCovCaptureStdoutInt(t, func() int {
		return runHierarchicalMigration(&invocation{projectsRoot: projectsRoot}, specPath, nil, hierarchicalMigrationOptions{format: "json"})
	})
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (hierarchical requires exactly one source root)", code)
	}
}

// TestCwCovRunHierarchicalMigrationDefaultedGithubDirActuallyResolvesUnderProjectsRoot
// gives the defaulting a positive, root-dependent assertion (mutation M18,
// sneat-dev/wb#760 review B5): the test above never supplies a source root,
// so it exits before githubDir is used for anything observable and a
// dropped/empty projectsRoot (githubDir = "") would exit 2 there too. With
// exactly one (unusable) source root, execution reaches
// wbhome.EnsureRoot(githubDir) before the campaign itself fails, so the
// resulting ".wb" directory's location is direct proof of which root
// --github-dir actually defaulted to.
func TestCwCovRunHierarchicalMigrationDefaultedGithubDirActuallyResolvesUnderProjectsRoot(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "migration.hcl")
	if err := os.WriteFile(specPath, []byte(cwCovMigrationSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	projectsRoot := t.TempDir()
	source := cwCovMigrationSource(t)
	var stderr bytes.Buffer
	_ = cwCovCaptureStdoutInt(t, func() int {
		return runHierarchicalMigration(&invocation{projectsRoot: projectsRoot}, specPath, []string{source},
			hierarchicalMigrationOptions{format: "json", progressOut: &stderr})
	})
	if _, err := os.Stat(filepath.Join(projectsRoot, ".wb")); err != nil {
		t.Fatalf("--github-dir defaulted somewhere other than projectsRoot: %v (stderr=%s)", err, stderr.String())
	}
}

// TestCwCovMigrateCommandHierarchicalFlagDispatchesToHierarchicalMigration
// proves that "wb migrate --hierarchical" reaches runHierarchicalMigration
// through the real command tree, not just through direct unit calls.
func TestCwCovMigrateCommandHierarchicalFlagDispatchesToHierarchicalMigration(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "migration.hcl")
	if err := os.WriteFile(specPath, []byte(cwCovMigrationSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	root := cwCovMigrationSource(t)
	githubDir := t.TempDir()
	t.Setenv("WB_HOME", t.TempDir())

	_, stderr, err := cwCovExec(t, t.TempDir(), func() *cobra.Command { return newMigrateCmd(&invocation{}) },
		specPath, root, "--hierarchical", "--cleanup", "--github-dir", githubDir)
	if err == nil {
		t.Fatal("--hierarchical --cleanup with a source root must be refused")
	}
	if exitCodeOf(t, err) != exitUsage {
		t.Fatalf("exit code = %v, want exitUsage\nstderr: %s", err, stderr)
	}
}

func TestCwCovLifecycleCheckoutUpdatedWarnsInsteadOfFailing(t *testing.T) {
	// A checkout that is not a repository has no identity; the hook dispatch
	// must warn and return rather than fail the caller's update. The
	// dispatch func is a fake: lifecycleCheckoutUpdated must never reach the
	// real lifecyclehooks.Dispatch (and so never the real config/state/
	// receipt paths or the real detached-worker launcher) from a test
	// binary (#620).
	var out bytes.Buffer
	var dispatchCalls int
	dispatch := func(context.Context, []lifecyclehooks.Event) (lifecyclehooks.Report, error) {
		dispatchCalls++
		return lifecyclehooks.Report{}, nil
	}
	handler := lifecycleCheckoutUpdatedWith(&out, dispatch)
	handler(t.Context(), orchestrate.CheckoutUpdate{Checkout: filepath.Join(t.TempDir(), "absent")})
	if !strings.Contains(out.String(), "lifecycle hooks were not dispatched") {
		t.Fatalf("missing-identity warning = %q", out.String())
	}
	if dispatchCalls != 0 {
		t.Fatalf("dispatch calls = %d, want 0: an unidentifiable checkout must return before dispatching", dispatchCalls)
	}

	// A real repository is identified and the dispatch runs; with no hooks
	// configured there is nothing to warn about.
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "wb@example.test")
	runGit(t, repo, "config", "user.name", "WB Test")
	runGit(t, repo, "remote", "add", "origin", "https://github.com/acme/app.git")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "init")
	identity, identityErr := lifecyclehooks.RepositoryIdentity(repo)
	if identityErr != nil || identity != "github.com/acme/app" {
		t.Fatalf("fixture identity = (%q, %v), want github.com/acme/app", identity, identityErr)
	}
	out.Reset()
	handler(t.Context(), orchestrate.CheckoutUpdate{
		Checkout: repo, OldSHA: "old", NewSHA: "new", Cause: "test",
	})
	if strings.Contains(out.String(), "identify updated checkout") {
		t.Fatalf("a real repository should be identified:\n%s", out.String())
	}
	if dispatchCalls != 1 {
		t.Fatalf("dispatch calls = %d, want 1: an identified checkout must dispatch exactly once", dispatchCalls)
	}
}

// TestLifecycleCheckoutUpdatedDefaultsToRealDispatch confirms the production
// wiring: lifecycleCheckoutUpdated (used unmodified by pr.go, pr_create.go
// and worktree_merge.go) still builds its handler from the real
// lifecyclehooks.Dispatch. The returned handler is never invoked here --
// invoking it would call the real Dispatch and touch the developer's real
// config/state/receipt paths (#620); the handler's own behavior is covered
// through a fake dispatch by TestCwCovLifecycleCheckoutUpdatedWarnsInsteadOfFailing.
func TestLifecycleCheckoutUpdatedDefaultsToRealDispatch(t *testing.T) {
	if handler := lifecycleCheckoutUpdated(io.Discard); handler == nil {
		t.Fatal("lifecycleCheckoutUpdated returned a nil handler")
	}
}

func TestCwCovBrowserTargetAndCommand(t *testing.T) {
	for _, target := range []string{"https://example.test/x", "http://example.test"} {
		resolved, err := browserTarget(target)
		if err != nil || resolved != target {
			t.Errorf("browserTarget(%q) = (%q, %v), want it unchanged", target, resolved, err)
		}
	}
	relative, err := browserTarget("some/relative/path")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(relative) {
		t.Errorf("browserTarget(relative) = %q, want an absolute path", relative)
	}

	darwinName, darwinArgs, err := browserCommand("darwin", "/x")
	if err != nil || darwinName != "open" || len(darwinArgs) != 1 {
		t.Errorf("darwin browser = (%q, %v, %v)", darwinName, darwinArgs, err)
	}
	linuxName, _, err := browserCommand("linux", "/x")
	if err != nil || linuxName != "xdg-open" {
		t.Errorf("linux browser = (%q, %v)", linuxName, err)
	}
	windowsName, windowsArgs, err := browserCommand("windows", "/x")
	if err != nil || windowsName != "rundll32" || len(windowsArgs) != 2 {
		t.Errorf("windows browser = (%q, %v, %v)", windowsName, windowsArgs, err)
	}
	if _, _, err := browserCommand("plan9", "/x"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("unsupported GOOS error = %v", err)
	}

	// A launcher failure to start is reported, never swallowed. Injected via
	// runnertest.Fake (task-8): openBrowser no longer starts a real process,
	// so this exercises the same propagation path without touching PATH.
	launchErr := errors.New("boom")
	fake := runnertest.New(t)
	fake.Expect(func(runnertest.Call) bool { return true }, runner.Result{}, launchErr)
	if err := openBrowser(fake, t.TempDir()); !errors.Is(err, launchErr) {
		t.Errorf("openBrowser error = %v, want %v", err, launchErr)
	}
}

func TestCwCovRepoIgnoreCommandTogglesTheMarker(t *testing.T) {
	repo := scratchRepo(t)
	var err error
	stdout := cwCovCaptureStdout(t, func() {
		_, _, err = cwCovExec(t, "", newRepoIgnoreCmd, repo)
	})
	if err != nil {
		t.Fatalf("repo ignore: %v", err)
	}
	if !strings.Contains(stdout, "ignored by wb sync") {
		t.Errorf("repo ignore output on stdout = %q", stdout)
	}
	value, ok := markerValue(t, repo)
	if !ok || value != "true" {
		t.Fatalf("marker = %q (present=%v), want true", value, ok)
	}

	stdout = cwCovCaptureStdout(t, func() {
		_, _, err = cwCovExec(t, "", newRepoIgnoreCmd, repo, "--unset")
	})
	if err != nil {
		t.Fatalf("repo ignore --unset: %v", err)
	}
	if !strings.Contains(stdout, "wb sync re-enabled") {
		t.Errorf("repo ignore --unset output = %q", stdout)
	}
	if _, ok := markerValue(t, repo); ok {
		t.Fatal("marker survived --unset")
	}

	// A directory that is not a repository is an error.
	if _, _, err := cwCovExec(t, "", newRepoIgnoreCmd, filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("repo ignore accepted a directory that is not a repository")
	}
}

func TestCwCovSessionPruneCommandRemovesOnlyExitedRecords(t *testing.T) {
	// sessionDir and the prune command must resolve the same state home, which
	// now derives from the projects root, so both are given the same root.
	root := t.TempDir()
	dir, err := sessionDir(&invocation{projectsRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	// A PID that cannot be running: the record is derived as "gone".
	if _, err := session.Register(dir, session.Record{PID: 1 << 30, Runtime: "test"}); err != nil {
		t.Fatal(err)
	}
	// The test process itself is live and must survive the prune.
	if _, err := session.Register(dir, session.Record{PID: os.Getpid(), Runtime: "test"}); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newSessionPruneCmd(&invocation{projectsRoot: root}) })
	if err != nil {
		t.Fatalf("session prune: %v", err)
	}
	if !strings.Contains(stdout, "removed 1 exited session record(s)") {
		t.Fatalf("session prune output = %q", stdout)
	}
	views, err := session.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].PID != os.Getpid() {
		t.Fatalf("remaining sessions = %+v, want only the live one", views)
	}

	// A second prune is a no-op.
	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return newSessionPruneCmd(&invocation{projectsRoot: root}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "removed 0 exited session record(s)") {
		t.Fatalf("second prune output = %q", stdout)
	}
}
