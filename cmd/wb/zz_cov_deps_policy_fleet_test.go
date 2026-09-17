package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/policy"
	"github.com/sneat-dev/wb/internal/testenv"
)

const cwCovPermissivePolicy = `
groups:
  - {name: everything, match: ["..."]}
types:
  - name: extension-implementation
    detect: ["github.com/acme/*/backend"]
    scopes:
      source: {allow: [everything]}
      tests:  {allow: [everything]}
layers:
  mode: report
  roles: {}
  order: []
`

// cwCovViolatingModule writes a Go module with one forbidden cross-repo import
// (blocking) and one layer inversion (report mode only).
func cwCovViolatingModule(t *testing.T, modulePath string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":               "module " + modulePath + "\n\ngo 1.26\n",
		"facade4app/facade.go": "package facade4app\n\nimport \"github.com/acme/other/backend/dbo\"\n",
		"dal4app/repo.go":      "package dal4app\n\nimport \"github.com/acme/app/backend/api4app\"\n",
		"api4app/http.go":      "package api4app\n",
	}
	for name, body := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func cwCovWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// cwCovRunDepsPolicy drives `deps policy` in-process. The subcommands read the
// shared projectsRoot global rather than declaring a --projects-root flag, so
// the global is pointed at the fixture for the duration of the call.
func cwCovRunDepsPolicy(t *testing.T, projects string, args ...string) (string, error) {
	t.Helper()
	testenv.Isolate(t)
	previousRoot, previousFilter := projectsRoot, filterFlag
	projectsRoot, filterFlag = projects, ""
	t.Cleanup(func() { projectsRoot, filterFlag = previousRoot, previousFilter })
	command := newDepsPolicyCmd()
	command.SilenceUsage = true
	command.SilenceErrors = true
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), err
}

func TestCwCovSweepGovernsAndReportsUngovernedModules(t *testing.T) {
	governed := cwCovViolatingModule(t, "github.com/acme/app/backend")
	policyPath := writeTestPolicy(t)

	outcomes := sweep([]deps.Repository{{Slug: "acme/app", Path: governed}}, policyPath)
	if len(outcomes) != 1 {
		t.Fatalf("outcomes = %+v, want one module", outcomes)
	}
	outcome := outcomes[0]
	if !outcome.Governed {
		t.Fatalf("outcome = %+v, want governed", outcome)
	}
	if outcome.Repository != "acme/app" || outcome.Module != "github.com/acme/app/backend" {
		t.Fatalf("identity = %+v", outcome)
	}
	if outcome.PolicyRef != policyPath {
		t.Errorf("policy ref = %q, want %q", outcome.PolicyRef, policyPath)
	}
	if outcome.Type != "extension-implementation" {
		t.Errorf("type = %q, want the detected type", outcome.Type)
	}
	if outcome.Blocking != 1 || outcome.Reported != 1 {
		t.Errorf("blocking/reported = %d/%d, want 1/1 (one enforcing import, one report-mode layer)",
			outcome.Blocking, outcome.Reported)
	}

	// A module with no policy declaration at all is recorded, not silently
	// skipped: an unwired repository is the finding.
	ungoverned := cwCovViolatingModule(t, "github.com/acme/nowire/backend")
	outcomes = sweep([]deps.Repository{{Slug: "acme/nowire", Path: ungoverned}}, "")
	if len(outcomes) != 1 || outcomes[0].Governed {
		t.Fatalf("ungoverned outcomes = %+v", outcomes)
	}
	if !strings.Contains(outcomes[0].Skipped, "no policy selected") {
		t.Errorf("skip reason = %q, want the missing-policy explanation", outcomes[0].Skipped)
	}
	if outcomes[0].Module != "github.com/acme/nowire/backend" {
		t.Errorf("ungoverned module = %q, want the scanned module path", outcomes[0].Module)
	}

	// Outcomes from several repositories are sorted by repository then dir.
	multi := sweep([]deps.Repository{
		{Slug: "zeta/app", Path: cwCovViolatingModule(t, "github.com/acme/zeta/backend")},
		{Slug: "alpha/app", Path: cwCovViolatingModule(t, "github.com/acme/alpha/backend")},
	}, policyPath)
	if len(multi) != 2 || multi[0].Repository != "alpha/app" || multi[1].Repository != "zeta/app" {
		t.Fatalf("sweep is not sorted by repository: %+v", multi)
	}
}

func TestCwCovFindingKeyCoversEveryRuleShape(t *testing.T) {
	if got := findingKey(policy.Finding{Rule: policy.RuleLayer, FromRole: "dal", ToRole: "api"}); got != "dal -> api" {
		t.Errorf("layer key = %q", got)
	}
	if got := findingKey(policy.Finding{Rule: policy.RuleImport, Group: "third-party", Scope: "source"}); got != "third-party (source)" {
		t.Errorf("import key = %q", got)
	}
	if got := findingKey(policy.Finding{Rule: "something-else"}); got != "something-else" {
		t.Errorf("default key = %q", got)
	}
}

func TestCwCovWriteReportTextBucketsAndUngovernedList(t *testing.T) {
	mk := func(repository string, mode policy.Mode) moduleOutcome {
		return moduleOutcome{
			Repository: repository,
			Module:     "github.com/acme/" + repository + "/backend",
			Governed:   true,
			Blocking:   1,
			Findings: []policy.Finding{{
				Rule: policy.RuleImport, Mode: mode, Group: "third-party", Scope: "source",
			}},
		}
	}
	outcomes := []moduleOutcome{
		mk("acme/one", policy.ModeEnforce),
		mk("acme/two", policy.ModeEnforce),
		mk("acme/three", policy.ModeReport),
		mk("acme/four", policy.ModeReport),
		mk("beta/five", policy.ModeReport),
		mk("beta/six", policy.ModeReport),
		{Repository: "acme/clean", Module: "github.com/acme/clean/backend", Governed: true},
		{Repository: "acme/unwired", Module: "github.com/acme/unwired/backend", Skipped: "no policy selected"},
	}

	var out bytes.Buffer
	writeReportText(&out, outcomes)
	text := out.String()
	for _, want := range []string{
		"7 module(s) governed, 1 clean, 1 not governed",
		"enforcing",
		"third-party (source)",
		"report only",
		"not governed",
		"acme/unwired",
		"no policy selected",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report text missing %q:\n%s", want, text)
		}
	}
	// More than three repositories are collapsed to a count rather than a list.
	if !strings.Contains(text, "4 repositories") {
		t.Errorf("report should collapse a bucket spread over >3 repos:\n%s", text)
	}
	// The enforcing bucket must be reported before the report-only bucket.
	if strings.Index(text, "enforcing") > strings.Index(text, "report only") {
		t.Errorf("bucket order is wrong:\n%s", text)
	}

	// An all-governed, all-clean sweep prints no bucket headings at all.
	var clean bytes.Buffer
	writeReportText(&clean, []moduleOutcome{{Repository: "acme/clean", Governed: true}})
	if strings.Contains(clean.String(), "enforcing") || strings.Contains(clean.String(), "report only") {
		t.Errorf("empty buckets must not render headings:\n%s", clean.String())
	}
	if !strings.Contains(clean.String(), "1 module(s) governed, 1 clean, 0 not governed") {
		t.Errorf("clean report = %q", clean.String())
	}

	// writeBuckets returns immediately on an empty map, and writeJSONTo emits
	// indented JSON.
	var empty bytes.Buffer
	writeBuckets(&empty, "enforcing", map[string]*findingBucket{})
	if empty.Len() != 0 {
		t.Errorf("empty bucket map wrote %q", empty.String())
	}
	var payload bytes.Buffer
	if err := writeJSONTo(&payload, map[string]int{"modules": 2}); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]int
	if err := json.Unmarshal(payload.Bytes(), &decoded); err != nil {
		t.Fatalf("writeJSONTo did not emit JSON: %v\n%s", err, payload.String())
	}
	if decoded["modules"] != 2 {
		t.Errorf("decoded = %+v", decoded)
	}
}

// cwCovPolicyFleetRoot builds a projects root with one governed module, one
// module whose declared type disagrees with detection, and one unwired module.
func cwCovPolicyFleetRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	cwCovWriteFile(t, filepath.Join(root, "acme", "policy", "backend.yaml"), testPolicyDocument)
	cwCovWriteFile(t, filepath.Join(root, "acme", "permissive.yaml"), cwCovPermissivePolicy)

	app := filepath.Join(root, "acme", "app")
	initTestRepository(t, app)
	cwCovWriteFile(t, filepath.Join(app, "go.mod"), "module github.com/acme/app/backend\n\ngo 1.26\n")
	cwCovWriteFile(t, filepath.Join(app, "facade4app", "facade.go"),
		"package facade4app\n\nimport \"github.com/acme/other/backend/dbo\"\n")
	cwCovWriteFile(t, filepath.Join(app, "dal4app", "repo.go"),
		"package dal4app\n\nimport \"github.com/acme/app/backend/api4app\"\n")
	cwCovWriteFile(t, filepath.Join(app, "api4app", "http.go"), "package api4app\n")
	cwCovWriteFile(t, filepath.Join(app, ".wb-deps-policy.yaml"), "policy: acme/policy//backend.yaml\n")

	mismatch := filepath.Join(root, "acme", "mismatch")
	initTestRepository(t, mismatch)
	cwCovWriteFile(t, filepath.Join(mismatch, "go.mod"), "module github.com/acme/mismatch/backend\n\ngo 1.26\n")
	cwCovWriteFile(t, filepath.Join(mismatch, ".wb-deps-policy.yaml"),
		"policy: acme/policy//backend.yaml\ntype: extension-contract\n")

	unwired := filepath.Join(root, "acme", "unwired")
	initTestRepository(t, unwired)
	cwCovWriteFile(t, filepath.Join(unwired, "go.mod"), "module github.com/acme/unwired/backend\n\ngo 1.26\n")
	return root
}

func TestCwCovDepsPolicyReportCommand(t *testing.T) {
	root := cwCovPolicyFleetRoot(t)
	cwCovFakeGH(t, "cwcov-user", nil, `[]`)

	stdout, err := cwCovRunDepsPolicy(t, root, "report")
	if code := exitCodeOf(t, err); code != exitFindings {
		t.Fatalf("report exit = %d, want findings\nstdout: %s", code, stdout)
	}
	for _, want := range []string{"module(s) governed", "enforcing", "not governed", "acme/unwired"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("report output missing %q:\n%s", want, stdout)
		}
	}

	stdout, err = cwCovRunDepsPolicy(t, root, "report", "--format", "json")
	if code := exitCodeOf(t, err); code != exitFindings {
		t.Fatalf("json report exit = %d", code)
	}
	var outcomes []moduleOutcome
	if err := json.Unmarshal([]byte(stdout), &outcomes); err != nil {
		t.Fatalf("report JSON: %v\n%s", err, stdout)
	}
	if len(outcomes) != 3 {
		t.Fatalf("outcomes = %+v, want three modules", outcomes)
	}

	// A filter that matches nothing is refused rather than reported clean.
	stdout, err = cwCovRunDepsPolicy(t, root, "report", "--match", "nothing/*")
	if code := exitCodeOf(t, err); code != exitUsage {
		t.Fatalf("empty match exit = %d, want usage\nstdout: %s", code, stdout)
	}
}

func TestCwCovDepsPolicyDriftCommand(t *testing.T) {
	root := cwCovPolicyFleetRoot(t)
	cwCovFakeGH(t, "cwcov-user", nil, `[]`)

	stdout, err := cwCovRunDepsPolicy(t, root, "drift")
	if code := exitCodeOf(t, err); code != exitFindings {
		t.Fatalf("drift exit = %d, want findings\nstdout: %s", code, stdout)
	}
	for _, want := range []string{
		"acme/unwired", "no policy:",
		"acme/mismatch", `declared "extension-contract" but detection chooses "extension-implementation"`,
		"needing attention",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("drift output missing %q:\n%s", want, stdout)
		}
	}

	stdout, err = cwCovRunDepsPolicy(t, root, "drift", "--format", "json")
	if code := exitCodeOf(t, err); code != exitFindings {
		t.Fatalf("json drift exit = %d", code)
	}
	var rows []struct {
		Repository string `json:"repository"`
		Module     string `json:"module"`
		Policy     string `json:"policy"`
		Declared   string `json:"declaredType"`
		Detected   string `json:"detectedType"`
		Issue      string `json:"issue"`
	}
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("drift JSON: %v\n%s", err, stdout)
	}
	if len(rows) != 3 {
		t.Fatalf("drift rows = %+v, want three modules", rows)
	}
	var sawMismatch bool
	for _, row := range rows {
		if row.Repository == "acme/mismatch" {
			sawMismatch = true
			if row.Declared != "extension-contract" || row.Detected != "extension-implementation" {
				t.Errorf("mismatch row = %+v", row)
			}
		}
	}
	if !sawMismatch {
		t.Errorf("drift rows lost the mismatch repository: %+v", rows)
	}
}

func TestCwCovDepsPolicyImpactCommand(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "acme", "app")
	initTestRepository(t, app)
	cwCovWriteFile(t, filepath.Join(app, "go.mod"), "module github.com/acme/app/backend\n\ngo 1.26\n")
	cwCovWriteFile(t, filepath.Join(app, "facade4app", "facade.go"),
		"package facade4app\n\nimport \"github.com/acme/other/backend/dbo\"\n")
	cwCovWriteFile(t, filepath.Join(app, "permissive.yaml"), cwCovPermissivePolicy)
	cwCovWriteFile(t, filepath.Join(app, ".wb-deps-policy.yaml"), "policy: permissive.yaml\n")
	cwCovFakeGH(t, "cwcov-user", nil, `[]`)

	candidate := filepath.Join(t.TempDir(), "candidate.yaml")
	cwCovWriteFile(t, candidate, testPolicyDocument)

	stdout, err := cwCovRunDepsPolicy(t, root, "impact", candidate)
	if code := exitCodeOf(t, err); code != exitFindings {
		t.Fatalf("impact exit = %d, want findings\nstdout: %s", code, stdout)
	}
	for _, want := range []string{"newly failing", "newly passing", "unchanged", "0 -> 1 violations"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("impact output missing %q:\n%s", want, stdout)
		}
	}

	stdout, err = cwCovRunDepsPolicy(t, root, "impact", candidate, "--format", "json")
	if code := exitCodeOf(t, err); code != exitFindings {
		t.Fatalf("json impact exit = %d", code)
	}
	var payload struct {
		Candidate    string `json:"candidate"`
		NewlyFailing []struct {
			Repository string `json:"repository"`
			Before     int    `json:"before"`
			After      int    `json:"after"`
		} `json:"newlyFailing"`
		Unchanged int `json:"unchanged"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("impact JSON: %v\n%s", err, stdout)
	}
	if len(payload.NewlyFailing) != 1 || payload.NewlyFailing[0].Repository != "acme/app" ||
		payload.NewlyFailing[0].Before != 0 || payload.NewlyFailing[0].After != 1 {
		t.Fatalf("impact payload = %+v", payload)
	}

	// A candidate that does not load is a usage error, never a silent pass.
	stdout, err = cwCovRunDepsPolicy(t, root, "impact", filepath.Join(t.TempDir(), "absent.yaml"))
	if code := exitCodeOf(t, err); code != exitUsage {
		t.Fatalf("missing candidate exit = %d, want usage\nstdout: %s", code, stdout)
	}
}

func TestCwCovFleetRepositoriesReportsUsageErrors(t *testing.T) {
	if _, err := fleetRepositories("", "("); err == nil {
		t.Fatal("an invalid --regex must be refused")
	} else if exitCodeOf(t, err) != exitUsage {
		t.Fatalf("exit = %d, want usage", exitCodeOf(t, err))
	}
}
