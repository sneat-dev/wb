package integration

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

	"github.com/sneat-dev/wb/internal/cli/cmddeps"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/depsrun"
	"github.com/sneat-dev/wb/internal/policy"
)

const testPolicyDocument = `
groups:
  - {name: own-repo,                 match: ["<self>/..."]}
  - {name: extension-contract,       match: ["github.com/acme/ext-*/..."]}
  - {name: extension-implementation, match: ["github.com/acme/*/..."]}
  - {name: dalgo-adapter,            match: ["github.com/dal-go/dalgo{2,4}*/..."]}
  - {name: dalgo-core,               match: ["github.com/dal-go/..."]}
  - {name: third-party,              match: ["..."]}
types:
  - name: extension-contract
    detect: ["github.com/acme/ext-*/backend"]
    scopes: {source: {allow: [own-repo, extension-contract, third-party]}}
  - name: extension-implementation
    detect: ["github.com/acme/*/backend"]
    scopes:
      source: {allow: [own-repo, extension-contract, dalgo-core, third-party]}
      tests:  {allow: [own-repo, extension-contract, dalgo-core, dalgo-adapter, third-party]}
layers:
  mode: report
  roles: {api: ["api4*"], facade: ["facade4*"], dal: ["dal4*"]}
  order: [[api], [facade], [dal]]
expect:
  - {import: "github.com/acme/ext-x/backend/dto", group: extension-contract}
  - {module: "github.com/acme/cal/backend",       type: extension-implementation}
`

func writeTestPolicy(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte(testPolicyDocument), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
func violatingModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":               "module github.com/acme/cal/backend\n\ngo 1.26\n",
		"facade4cal/facade.go": "package facade4cal\n\nimport \"github.com/acme/other/backend/dbo\"\n",
		"dal4cal/repo.go":      "package dal4cal\n\nimport \"github.com/acme/cal/backend/api4cal\"\n",
		"api4cal/http.go":      "package api4cal\n",
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
func writeRwi01TestPolicyWithLayerForbid(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte(testPolicyDocumentWithLayerForbid), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const testPolicyDocumentWithLayerForbid = `
groups:
  - {name: own-repo,                 match: ["<self>/..."]}
  - {name: extension-contract,       match: ["github.com/acme/ext-*/..."]}
  - {name: extension-implementation, match: ["github.com/acme/*/..."]}
  - {name: dalgo-adapter,            match: ["github.com/dal-go/dalgo{2,4}*/..."]}
  - {name: dalgo-core,               match: ["github.com/dal-go/..."]}
  - {name: third-party,              match: ["..."]}
types:
  - name: extension-contract
    detect: ["github.com/acme/ext-*/backend"]
    scopes: {source: {allow: [own-repo, extension-contract, third-party]}}
  - name: extension-implementation
    detect: ["github.com/acme/*/backend"]
    scopes:
      source: {allow: [own-repo, extension-contract, dalgo-core, third-party]}
      tests:  {allow: [own-repo, extension-contract, dalgo-core, dalgo-adapter, third-party]}
layers:
  mode: report
  roles: {api: ["api4*"], facade: ["facade4*"], dal: ["dal4*"]}
  order: [[api], [facade], [dal]]
  forbid:
    - {from: dal, to: api, reason: "data access must not call back into the api layer"}
expect:
  - {import: "github.com/acme/ext-x/backend/dto", group: extension-contract}
  - {module: "github.com/acme/cal/backend",       type: extension-implementation}
`

const rwi01ExpectationFailurePolicy = `
groups:
  - {name: own-repo,    match: ["github.com/acme/own/..."]}
  - {name: third-party, match: ["..."]}
types:
  - name: service
    detect: ["github.com/acme/svc-x/backend"]
    scopes: {source: {allow: [own-repo, third-party]}}
expect:
  - {import: "github.com/acme/other/pkg",              group: own-repo}
  - {module: "github.com/acme/does-not-match/backend", type: service}
`

// TestPolicyTestReportsFailedAssertions drives `wb deps policy test`
// with a policy whose own assertions fail, covering the failure side of
// RunExpectations' report loop (both with and without a detection error)
// and the final non-zero-failures refusal, none of which a policy that
// only ever passes its own assertions reaches.
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

type moduleOutcome = depsrun.PolicyModuleOutcome

const (
	exitOK    = 0
	exitUsage = 2
)

func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var coded *codedError
	if errors.As(err, &coded) {
		return coded.code
	}
	t.Fatalf("expected coded error, got %T: %v", err, err)
	return -1
}
func runPolicy(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return cwCovRunDepsPolicy(t, "", args...)
}
func cwCovRunDepsPolicy(t *testing.T, projects string, args ...string) (string, error) {
	t.Helper()
	runtime := testDirectiveRuntime(shared.Flags{ProjectsRoot: projects})
	service := depsrun.NewPolicy(depsrun.DefaultPolicyDependencies(io.Discard), func(message string) error { return runtime.ExitError(shared.ExitUsage, message) })
	command := cmddeps.NewPolicy(runtime, cmddeps.PolicyOperations(service))
	command.SilenceUsage = true
	command.SilenceErrors = true
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), err
}
func TestPolicyCheckExitsOneOnBlockingViolation(t *testing.T) {
	t.Parallel()
	out, err := runPolicy(t, "check", violatingModule(t), "--policy", writeTestPolicy(t))
	if got := exitCodeOf(t, err); got != exitFindings {
		t.Fatalf("exit = %d, want %d\n%s", got, exitFindings, out)
	}
	if !strings.Contains(out, "facade4cal/facade.go:3") {
		t.Fatalf("output should name the file and line:\n%s", out)
	}
	// The layer inversion is report-mode in this policy, so it prints but must
	// not be what fails the command.
	if !strings.Contains(out, "report only") {
		t.Fatalf("report-mode finding should be labelled:\n%s", out)
	}
	if !strings.Contains(out, "1 blocking, 1 reported") {
		t.Fatalf("counts are wrong:\n%s", out)
	}
}
func TestPolicyCheckStrictPromotesReportFindings(t *testing.T) {
	t.Parallel()
	out, err := runPolicy(t, "check", violatingModule(t), "--policy", writeTestPolicy(t), "--strict")
	if exitCodeOf(t, err) != exitFindings {
		t.Fatalf("expected findings\n%s", out)
	}
	if !strings.Contains(out, "2 blocking, 0 reported") {
		t.Fatalf("--strict should promote the layer finding:\n%s", out)
	}
}
func TestPolicyCheckExitsZeroWhenClean(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/acme/cal/backend\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runPolicy(t, "check", root, "--policy", writeTestPolicy(t))
	if err != nil {
		t.Fatalf("expected success, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "no violations") {
		t.Fatalf("output:\n%s", out)
	}
}
func TestPolicyCheckJSONIsParseable(t *testing.T) {
	t.Parallel()
	out, _ := runPolicy(t, "check", violatingModule(t), "--policy", writeTestPolicy(t), "--format", "json")
	var payload struct {
		Module   string `json:"module"`
		Type     string `json:"type"`
		Blocking int    `json:"blocking"`
		Findings []struct {
			File string `json:"file"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if payload.Module != "github.com/acme/cal/backend" || payload.Blocking != 1 || len(payload.Findings) != 2 {
		t.Fatalf("payload = %+v", payload)
	}
}
func TestPolicyCheckWithoutAPolicyIsAUsageError(t *testing.T) {
	t.Parallel()
	out, err := runPolicy(t, "check", violatingModule(t))
	if got := exitCodeOf(t, err); got != exitUsage {
		t.Fatalf("exit = %d, want %d (%s)\n%s", got, exitUsage, err, out)
	}
	if !strings.Contains(err.Error(), policy.ConfigFileName) {
		t.Fatalf("error should say how to select a policy: %v", err)
	}
}
func TestPolicyCheckRejectsAPinnedPolicyRelease(t *testing.T) {
	t.Parallel()
	root := violatingModule(t)
	if err := os.WriteFile(filepath.Join(root, policy.ConfigFileName), []byte("policy: acme/cicd//p.yaml@v1.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runPolicy(t, "check", root)
	if got := exitCodeOf(t, err); got != exitUsage {
		t.Fatalf("exit = %d, want %d", got, exitUsage)
	}
	if !strings.Contains(err.Error(), "release") {
		t.Fatalf("error should explain why pinning is refused: %v", err)
	}
}
func TestPolicyCheckRejectsARepositoryTryingToLoosen(t *testing.T) {
	t.Parallel()
	root := violatingModule(t)
	body := "policy: " + writeTestPolicy(t) + "\nallow: [dalgo-adapter]\n"
	if err := os.WriteFile(filepath.Join(root, policy.ConfigFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runPolicy(t, "check", root)
	if got := exitCodeOf(t, err); got != exitUsage {
		t.Fatalf("exit = %d, want %d", got, exitUsage)
	}
	if !strings.Contains(err.Error(), "tighten") {
		t.Fatalf("error should state the tighten-never-loosen rule: %v", err)
	}
}
func TestPolicyExplainShowsPatternPrecedenceAndShadowing(t *testing.T) {
	t.Parallel()
	out, err := runPolicy(t, "explain", "github.com/acme/ext-cal/backend/dto", violatingModule(t), "--policy", writeTestPolicy(t))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"group   extension-contract",
		"pattern #2",
		"would also match",
		"ALLOWED",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("explain output missing %q:\n%s", want, out)
		}
	}
}
func TestPolicyExplainSeparatesScopeVerdicts(t *testing.T) {
	t.Parallel()
	out, err := runPolicy(t, "explain", "github.com/dal-go/dalgo2firestore", violatingModule(t), "--policy", writeTestPolicy(t))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "source  FORBIDDEN") || !strings.Contains(out, "tests   ALLOWED") {
		t.Fatalf("scopes should differ:\n%s", out)
	}
}
func TestPolicyShowPrintsEffectiveRules(t *testing.T) {
	t.Parallel()
	out, err := runPolicy(t, "show", violatingModule(t), "--policy", writeTestPolicy(t))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"type     extension-implementation", "source.allow", "tests.allow", "layers   report"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show output missing %q:\n%s", want, out)
		}
	}
}
func TestPolicyValidateAcceptsAGoodPolicyAndRejectsAShadowedOne(t *testing.T) {
	t.Parallel()
	out, err := runPolicy(t, "validate", writeTestPolicy(t))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "no problems found") {
		t.Fatalf("output:\n%s", out)
	}

	shadowed := filepath.Join(t.TempDir(), "bad.yaml")
	body := strings.Replace(testPolicyDocument,
		`  - {name: extension-contract,       match: ["github.com/acme/ext-*/..."]}
  - {name: extension-implementation, match: ["github.com/acme/*/..."]}`,
		`  - {name: extension-implementation, match: ["github.com/acme/*/..."]}
  - {name: extension-contract,       match: ["github.com/acme/ext-*/..."]}`, 1)
	if err := os.WriteFile(shadowed, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = runPolicy(t, "validate", shadowed)
	if got := exitCodeOf(t, err); got != exitFindings {
		t.Fatalf("exit = %d, want %d\n%s", got, exitFindings, out)
	}
	if !strings.Contains(out, "unreachable") {
		t.Fatalf("validate should catch the ordering mistake:\n%s", out)
	}
}
func TestPolicyTestRunsAssertions(t *testing.T) {
	t.Parallel()
	out, err := runPolicy(t, "test", writeTestPolicy(t))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "2 assertion(s), 2 passed, 0 failed") {
		t.Fatalf("output:\n%s", out)
	}
}
func TestPolicyTestFailsWhenAPolicyDeclaresNoAssertions(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "bare.yaml")
	body := testPolicyDocument[:strings.Index(testPolicyDocument, "expect:")]
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runPolicy(t, "test", path)
	if got := exitCodeOf(t, err); got != exitFindings {
		t.Fatalf("exit = %d, want %d\n%s", got, exitFindings, out)
	}
}
func TestPolicyInitWritesTheDeclarationAndThenChecks(t *testing.T) {
	t.Parallel()
	root := violatingModule(t)
	policyPath := writeTestPolicy(t)
	out, err := runPolicy(t, "init", root, "--policy", policyPath)
	if got := exitCodeOf(t, err); got != exitFindings {
		t.Fatalf("init should surface the repository's real state, exit = %d\n%s", got, out)
	}
	written, readErr := os.ReadFile(filepath.Join(root, policy.ConfigFileName))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(written), "policy: "+policyPath) {
		t.Fatalf("config file = %q", written)
	}
	if !strings.Contains(out, "detected type: extension-implementation") {
		t.Fatalf("init should report the detected type:\n%s", out)
	}
}
func TestPolicyInitRefusesToOverwrite(t *testing.T) {
	t.Parallel()
	root := violatingModule(t)
	policyPath := writeTestPolicy(t)
	if _, err := runPolicy(t, "init", root, "--policy", policyPath); err == nil {
		t.Fatal("precondition: first init should report findings")
	}
	_, err := runPolicy(t, "init", root, "--policy", policyPath)
	if got := exitCodeOf(t, err); got != exitUsage {
		t.Fatalf("exit = %d, want %d", got, exitUsage)
	}
}
func TestPolicyShowPrintsConfigPathStrictAndLayerForbidReason(t *testing.T) {
	t.Parallel()
	root := violatingModule(t)
	policyPath := writeRwi01TestPolicyWithLayerForbid(t)
	configPath := filepath.Join(root, ".wb-deps-policy.yaml")
	configBody := "policy: " + policyPath + "\nstrict: true\n"
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runPolicy(t, "show", root)
	if err != nil {
		t.Fatalf("wb deps policy show with a strict config: %v\n%s", err, out)
	}
	for _, want := range []string{
		"config   " + configPath,
		"strict   on",
		"forbidden: dal -> api — data access must not call back into the api layer",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("show output missing %q:\n%s", want, out)
		}
	}
}
func TestPolicyTestReportsFailedAssertions(t *testing.T) {
	t.Parallel()
	policyPath := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(policyPath, []byte(rwi01ExpectationFailurePolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runPolicy(t, "test", policyPath)
	if err == nil {
		t.Fatalf("wb deps policy test with failing assertions: want an error, got nil\n%s", out)
	}
	if exitCodeOf(t, err) != exitFindings {
		t.Fatalf("wb deps policy test with failing assertions: exit code = %d, want exitFindings", exitCodeOf(t, err))
	}
	if !strings.Contains(out, "FAIL  github.com/acme/other/pkg: want own-repo, got third-party") {
		t.Fatalf("test output missing the group-mismatch FAIL line:\n%s", out)
	}
	if !strings.Contains(out, "FAIL  github.com/acme/does-not-match/backend:") {
		t.Fatalf("test output missing the detection-error FAIL line:\n%s", out)
	}
	if !strings.Contains(out, "2 assertion(s), 0 passed, 2 failed") {
		t.Fatalf("test output missing the summary line:\n%s", out)
	}
}
func TestCwCovDepsPolicyReportCommand(t *testing.T) { //nolint:paralleltest // Actual GitHub PATH fixture modifies process environment.
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
func TestCwCovDepsPolicyDriftCommand(t *testing.T) { //nolint:paralleltest // Actual GitHub PATH fixture modifies process environment.
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
func TestCwCovDepsPolicyImpactCommand(t *testing.T) { //nolint:paralleltest // Actual GitHub PATH fixture modifies process environment.
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
	t.Parallel()
	if _, err := depsrun.NewPolicy(depsrun.DefaultPolicyDependencies(io.Discard), func(message string) error { return &codedError{2, message} }).Report(context.Background(), depsrun.PolicyFleetRequest{Selection: depsrun.Selection{Fleet: true, Parallel: 1, Regex: "("}}); err == nil {
		t.Fatal("an invalid --regex must be refused")
	} else if exitCodeOf(t, err) != exitUsage {
		t.Fatalf("exit = %d, want usage", exitCodeOf(t, err))
	}
}
