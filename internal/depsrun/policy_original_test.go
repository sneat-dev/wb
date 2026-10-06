package depsrun

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/filewrite"
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

type policyTestError struct{ message string }

func (e *policyTestError) Error() string { return e.message }
func newPolicyTestService() *PolicyService {
	return NewPolicy(DefaultPolicyDependencies(io.Discard), func(message string) error { return &policyTestError{message} })
}
func exitCodeOfSafe(err error) int {
	if err == nil {
		return 0
	}
	var coded *policyTestError
	if errors.As(err, &coded) {
		return 2
	}
	return 1
}

const exitUsage = 2

var errBoomPR9 = errors.New("pr9 boom")

func TestSweepAppliesStrictConfigPromotingReportFindings(t *testing.T) {
	t.Parallel()
	root := violatingModule(t)
	policyPath := writeTestPolicy(t)
	config := "policy: " + policyPath + "\nstrict: true\n"
	if err := os.WriteFile(filepath.Join(root, ".wb-deps-policy.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	projectsRoot := ""
	outcomes := newPolicyTestService().sweep(projectsRoot, []deps.Repository{{Slug: "acme/cal", Path: root}}, "")
	if len(outcomes) != 1 {
		t.Fatalf("outcomes = %+v, want exactly one module", outcomes)
	}
	outcome := outcomes[0]
	if !outcome.Governed {
		t.Fatalf("outcome not governed: %+v", outcome)
	}
	if outcome.Blocking == 0 {
		t.Fatalf("strict config did not promote any finding to blocking: %+v", outcome)
	}
}
func TestSweepSortsOutcomesByRepositoryThenDirectory(t *testing.T) {
	t.Parallel()
	rootA := t.TempDir()
	rootB := t.TempDir()
	// rootA/rootB sort however t.TempDir() names them; force a known order
	// by nesting one further so its absolute path always sorts first.
	first, second := rootA, rootB
	if first > second {
		first, second = second, first
	}
	for _, root := range []string{first, second} {
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/dup\n\ngo 1.26\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	projectsRoot := ""
	// Neither module has a policy declaration, so each is recorded with
	// Skipped set — sweep never needs a real policy fixture to populate
	// Repository and Directory, which is all this sort exercises.
	outcomes := newPolicyTestService().sweep(projectsRoot, []deps.Repository{
		{Slug: "acme/dup", Path: second},
		{Slug: "acme/dup", Path: first},
	}, "")
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %+v, want two modules", outcomes)
	}
	if outcomes[0].Directory != first || outcomes[1].Directory != second {
		t.Fatalf("outcomes not sorted by directory: %+v", outcomes)
	}
}
func TestCwDepsFetchPolicyOverHTTP(t *testing.T) {
	t.Parallel()
	document := testPolicyDocument
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/policy.yaml":
			writer.Header().Set("Content-Type", "application/yaml")
			_, _ = writer.Write([]byte(document))
		case "/missing.yaml":
			writer.WriteHeader(http.StatusNotFound)
		default:
			writer.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	path, err := fetchPolicy(server.URL + "/policy.yaml")
	if err != nil {
		t.Fatalf("fetchPolicy: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read fetched policy: %v", readErr)
	}
	if string(raw) != document {
		t.Errorf("fetched policy = %q, want the served document", string(raw))
	}
	if !strings.HasPrefix(filepath.Base(path), "wb-policy-") {
		t.Errorf("fetched policy path = %q, want a wb-policy temp file", path)
	}
	// A non-200 response surfaces the status rather than an empty document.
	if _, err := fetchPolicy(server.URL + "/missing.yaml"); err == nil ||
		!strings.Contains(err.Error(), "404") {
		t.Fatalf("non-200 fetch = %v", err)
	}
	// A transport failure names the URL it could not reach.
	if _, err := fetchPolicy("http://127.0.0.1:1/policy.yaml"); err == nil ||
		!strings.Contains(err.Error(), "fetch policy") {
		t.Fatalf("unreachable fetch = %v", err)
	}
}
func TestCwDepsResolvePolicyRefusesAnUnfetchableURLSource(t *testing.T) {
	t.Parallel()
	module := t.TempDir()
	cwCovWriteFile(t, filepath.Join(module, "go.mod"), "module github.com/acme/app/backend\n\ngo 1.26\n")
	// An https policy source takes the fetch branch; an unreachable one is a
	// usage error naming the fetch, never a silent fallback to no policy.
	_, err := newPolicyTestService().resolvePolicy("", module, "https://127.0.0.1:1/policy.yaml")
	if exitCodeOfSafe(err) != exitUsage || !strings.Contains(err.Error(), "fetch policy") {
		t.Fatalf("unfetchable policy source = %v", err)
	}
	// An http source is refused before any request is attempted, because a
	// policy fetched in the clear is not the policy a repository agreed to.
	if _, err := newPolicyTestService().resolvePolicy("", module, "http://example.test/policy.yaml"); exitCodeOfSafe(err) != exitUsage ||
		!strings.Contains(err.Error(), "must use https") {
		t.Fatalf("insecure policy source = %v", err)
	}
}
func TestCwDepsPolicySearchRootsAndModuleDir(t *testing.T) {
	t.Parallel()
	if roots := policySearchRoots(""); roots != nil {
		t.Errorf("policySearchRoots with no projects root = %v, want nil", roots)
	}
	projectsRoot := t.TempDir()
	if roots := policySearchRoots(projectsRoot); len(roots) != 1 || roots[0] != projectsRoot {
		t.Errorf("policySearchRoots = %v", roots)
	}

	// findModuleDir walks up to the owning go.mod.
	root := t.TempDir()
	cwCovWriteFile(t, filepath.Join(root, "go.mod"), "module github.com/acme/app\n\ngo 1.26\n")
	nested := filepath.Join(root, "backend", "internal", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	found, err := findModuleDir(nested)
	if err != nil || found != root {
		t.Fatalf("findModuleDir = %q, %v", found, err)
	}
	// A directory under no module reports the path it started from.
	orphan := t.TempDir()
	if _, err := findModuleDir(orphan); err == nil ||
		!strings.Contains(err.Error(), "no go.mod found at or above") {
		t.Fatalf("findModuleDir outside a module = %v", err)
	}
}
func TestCwCovSweepGovernsAndReportsUngovernedModules(t *testing.T) {
	t.Parallel()
	governed := cwCovViolatingModule(t, "github.com/acme/app/backend")
	policyPath := writeTestPolicy(t)

	outcomes := newPolicyTestService().sweep("", []deps.Repository{{Slug: "acme/app", Path: governed}}, policyPath)
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
	outcomes = newPolicyTestService().sweep("", []deps.Repository{{Slug: "acme/nowire", Path: ungoverned}}, "")
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
	multi := newPolicyTestService().sweep("", []deps.Repository{
		{Slug: "zeta/app", Path: cwCovViolatingModule(t, "github.com/acme/zeta/backend")},
		{Slug: "alpha/app", Path: cwCovViolatingModule(t, "github.com/acme/alpha/backend")},
	}, policyPath)
	if len(multi) != 2 || multi[0].Repository != "alpha/app" || multi[1].Repository != "zeta/app" {
		t.Fatalf("sweep is not sorted by repository: %+v", multi)
	}
}
func TestFetchPolicyInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(testPolicyDocument))
	}))
	// t.Cleanup, not defer: the subtests below run t.Parallel(), which
	// pauses them until this outer function's synchronous body returns --
	// a deferred server.Close() would already have fired by then, and every
	// subtest would see a closed server instead of the injected failure.
	// t.Cleanup runs only once this test and every paused parallel subtest
	// have actually finished.
	t.Cleanup(server.Close)
	for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepWrite} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			inj := &filewrite.Injector{Step: step, Err: errBoomPR9}
			path, err := fetchPolicyInjected(server.URL+"/policy.yaml", inj)
			if path != "" || !errors.Is(err, errBoomPR9) {
				t.Fatalf("fetchPolicyInjected(%s failure) = (%q, %v), want (\"\", errBoomPR9)", step, path, err)
			}
		})
	}
}
