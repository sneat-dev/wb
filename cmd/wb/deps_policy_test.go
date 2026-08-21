package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// violatingModule has one forbidden cross-repo import and one layer inversion.
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

func runPolicy(t *testing.T, args ...string) (string, error) {
	t.Helper()
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

func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return exitOK
	}
	var coded *exitError
	if errors.As(err, &coded) {
		return coded.code
	}
	t.Fatalf("expected an exitError, got %T: %v", err, err)
	return -1
}

func TestPolicyCheckExitsOneOnBlockingViolation(t *testing.T) {
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
	out, err := runPolicy(t, "check", violatingModule(t), "--policy", writeTestPolicy(t), "--strict")
	if exitCodeOf(t, err) != exitFindings {
		t.Fatalf("expected findings\n%s", out)
	}
	if !strings.Contains(out, "2 blocking, 0 reported") {
		t.Fatalf("--strict should promote the layer finding:\n%s", out)
	}
}

func TestPolicyCheckExitsZeroWhenClean(t *testing.T) {
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
	out, err := runPolicy(t, "check", violatingModule(t))
	if got := exitCodeOf(t, err); got != exitUsage {
		t.Fatalf("exit = %d, want %d (%s)\n%s", got, exitUsage, err, out)
	}
	if !strings.Contains(err.Error(), policy.ConfigFileName) {
		t.Fatalf("error should say how to select a policy: %v", err)
	}
}

func TestPolicyCheckRejectsAPinnedPolicyRelease(t *testing.T) {
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
	out, err := runPolicy(t, "explain", "github.com/dal-go/dalgo2firestore", violatingModule(t), "--policy", writeTestPolicy(t))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "source  FORBIDDEN") || !strings.Contains(out, "tests   ALLOWED") {
		t.Fatalf("scopes should differ:\n%s", out)
	}
}

func TestPolicyShowPrintsEffectiveRules(t *testing.T) {
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
	out, err := runPolicy(t, "test", writeTestPolicy(t))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "2 assertion(s), 2 passed, 0 failed") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestPolicyTestFailsWhenAPolicyDeclaresNoAssertions(t *testing.T) {
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

func TestPolicySubcommandsAndFlagsArePresent(t *testing.T) {
	command := newDepsPolicyCmd()
	wanted := map[string][]string{
		"check":    {"policy", "type", "format", "strict"},
		"explain":  {"policy", "type"},
		"show":     {"policy", "type"},
		"validate": nil,
		"test":     nil,
		"init":     {"policy"},
		"report":   {"match", "regex", "policy", "format"},
		"drift":    {"match", "regex", "policy", "format"},
		"impact":   {"match", "regex", "format"},
	}
	for name, flags := range wanted {
		sub, _, err := command.Find([]string{name})
		if err != nil || sub == command {
			t.Errorf("deps policy %s is missing", name)
			continue
		}
		for _, flag := range flags {
			if sub.Flags().Lookup(flag) == nil {
				t.Errorf("deps policy %s is missing --%s", name, flag)
			}
		}
	}
}
