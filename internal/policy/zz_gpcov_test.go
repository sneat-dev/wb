package policy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file adds behavior-asserting coverage for the policy engine's error
// handling, its rarely-taken decision branches, and its output renderers.
// Helpers are prefixed gpCov to stay clear of the package's existing helpers.

func gpCovMustLoad(t *testing.T, body string) Policy {
	t.Helper()
	loaded, err := Load(writePolicy(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return loaded
}

// TestGpCovLoadRejectsMalformedDocuments drives every rejection Load documents
// but no existing test exercised: nameless and reserved groups, empty match
// lists, unknown scopes, misplaced expectations, and an unusable layer policy.
func TestGpCovLoadRejectsMalformedDocuments(t *testing.T) {
	t.Parallel()
	const goodGroups = "groups:\n  - {name: g, match: [\"...\"]}\n"
	const goodTypes = "types:\n  - name: t\n    detect: [\"x/y\"]\n    scopes: {source: {allow: [g]}}\n"

	cases := []struct {
		name string
		body string
		want string
	}{
		{"group with no name", "groups:\n  - {match: [\"...\"]}\n" + goodTypes, "has no name"},
		{"group named stdlib", "groups:\n  - {name: stdlib, match: [\"...\"]}\n" + goodTypes, "reserved group name"},
		{"group with no match patterns", "groups:\n  - {name: g}\n" + goodTypes, "no match patterns"},
		{"group pattern that cannot compile", "groups:\n  - {name: g, match: [\"a/.../b\"]}\n" + goodTypes, "only allowed as the final segment"},
		{"type with no name", goodGroups + "types:\n  - detect: [\"x/y\"]\n    scopes: {source: {allow: [g]}}\n", "a type has no name"},
		{"type detect pattern that cannot compile", goodGroups + "types:\n  - name: t\n    detect: [\"x/[/...\"]\n    scopes: {source: {allow: [g]}}\n", "bad segment"},
		{"unknown scope", goodGroups + "types:\n  - name: t\n    detect: [\"x/y\"]\n    scopes: {sideways: {allow: [g]}}\n", "declares unknown scope"},
		{"type with no source scope", goodGroups + "types:\n  - name: t\n    detect: [\"x/y\"]\n    scopes: {tests: {allow: [g]}}\n", "has no \"source\" scope"},
		{"unknown-role that is neither ignore nor error", goodGroups + goodTypes + "layers:\n  unknown-role: explode\n", "unknown-role must be"},
		{"layer role pattern that cannot compile", goodGroups + goodTypes + "layers:\n  roles: {api: [\"api/.../x\"]}\n  order: [[api]]\n", "only allowed as the final segment"},
		{"forbid edge naming only from", goodGroups + goodTypes + "layers:\n  roles: {api: [\"api4*\"], dal: [\"dal4*\"]}\n  order: [[api],[dal]]\n  forbid: [{from: api}]\n", "must name both from and to"},
		{"forbid edge naming a role with no patterns", goodGroups + goodTypes + "layers:\n  roles: {api: [\"api4*\"]}\n  order: [[api]]\n  forbid: [{from: api, to: ghost}]\n", "which has no patterns"},
		{"expect naming both import and module", goodGroups + goodTypes + "expect:\n  - {import: a/b, module: c/d, group: g, type: t}\n", "exactly one of import or module"},
		{"expect on an import without a group", goodGroups + goodTypes + "expect:\n  - {import: a/b}\n", "must state the expected group"},
		{"expect on a module without a type", goodGroups + goodTypes + "expect:\n  - {module: a/b}\n", "must state the expected type"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load(writePolicy(t, testCase.body))
			if err == nil {
				t.Fatal("Load accepted an unusable document")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %q, want it to mention %q", err, testCase.want)
			}
		})
	}
}

// TestGpCovLoadReadErrors proves a policy file that cannot be read at all is
// reported rather than treated as an empty policy.
func TestGpCovLoadReadErrors(t *testing.T) {
	t.Parallel()
	if _, err := Load(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("Load accepted a missing file")
	}
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("Load accepted a directory as a policy file")
	}
}

// TestGpCovParseRejectsNonDocumentYAML pins that a policy that is not a
// mapping is refused at decode time.
func TestGpCovParseRejectsNonDocumentYAML(t *testing.T) {
	t.Parallel()
	if _, err := Parse([]byte("- one\n- two\n"), "inline"); err == nil {
		t.Fatal("Parse accepted a YAML sequence as a policy")
	}
	if _, err := Parse([]byte("---\n"), "inline"); err == nil {
		t.Fatal("Parse accepted a document that declares nothing")
	}
}

// TestGpCovValidateReportsUnshadowedPatterns proves Validate stays quiet when
// a later pattern is genuinely reachable, for groups and for types alike.
func TestGpCovValidateReportsUnshadowedPatterns(t *testing.T) {
	t.Parallel()
	body := `
groups:
  - {name: acme,   match: ["github.com/acme/..."]}
  - {name: gitlab, match: ["gitlab.com/acme/..."]}
types:
  - name: acme
    detect: ["github.com/acme/*/backend"]
    scopes: {source: {allow: [acme]}}
  - name: gitlab
    detect: ["gitlab.com/acme/*/backend"]
    scopes: {source: {allow: [gitlab]}}
`
	diagnostics := Validate(gpCovMustLoad(t, body))
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, "unreachable") {
			t.Fatalf("Validate invented a shadow: %q", diagnostic.Message)
		}
	}
}

// TestGpCovSuggestProposesANearMiss pins the typo correction: a group name one
// edit away from a declared group is offered back to the author.
func TestGpCovSuggestProposesANearMiss(t *testing.T) {
	t.Parallel()
	body := "groups:\n  - {name: extension-contract, match: [\"github.com/acme/ext-*/...\"]}\n" +
		"types:\n  - name: t\n    detect: [\"x/y\"]\n    scopes: {source: {allow: [extension-contrat]}}\n"
	_, err := Load(writePolicy(t, body))
	if err == nil {
		t.Fatal("Load accepted an allow list naming an undeclared group")
	}
	if !strings.Contains(err.Error(), `did you mean "extension-contract"?`) {
		t.Fatalf("error = %q, want the near-miss suggestion", err)
	}
}

// TestGpCovEditDistanceWithin pins the edit-distance bound directly.
func TestGpCovEditDistanceWithin(t *testing.T) {
	t.Parallel()
	if !editDistanceWithin("same", "same", 0) {
		t.Fatal("identical strings should be within any limit")
	}
	if !editDistanceWithin("kitten", "sitten", 1) {
		t.Fatal("one substitution should be within limit 1")
	}
	if editDistanceWithin("kitten", "sitting", 2) {
		t.Fatal("three edits should exceed limit 2")
	}
	if editDistanceWithin("a", "abcdefgh", 2) {
		t.Fatal("a length gap beyond the limit should be rejected without computing")
	}
}

// TestGpCovClassifyScopeAndOverlap pins the stdlib shortcut on Scope.Allows,
// the stdlib shortcut on Classify, and a same-group overlap that must not be
// reported as a competing match.
func TestGpCovClassifyScopeAndOverlap(t *testing.T) {
	t.Parallel()
	if !(Scope{}).Allows(GroupStdlib) {
		t.Fatal("every scope must permit the standard library")
	}
	if IsStdlib("") {
		t.Fatal("an empty import path is not a standard-library import")
	}
	if !IsStdlib("fmt") || IsStdlib("fmt.example/x") {
		t.Fatal("IsStdlib misclassified a dotted first segment")
	}

	policy := gpCovMustLoad(t, `
groups:
  - {name: g, match: ["github.com/acme/...", "github.com/acme/mod/..."]}
types:
  - name: t
    detect: ["github.com/acme/mod/x"]
    scopes: {source: {allow: [g]}}
`)
	classification := policy.Classify("github.com/acme/mod/x", "")
	if classification.Group != "g" {
		t.Fatalf("group = %q, want g", classification.Group)
	}
	if len(classification.AlsoMatched) != 0 {
		t.Fatalf("a same-group second match was reported as a competing group: %+v", classification.AlsoMatched)
	}
}

// gpCovLayeredReportPolicy declares every layer shape the checks below need:
// a report-mode layer rule, unknown-role handling set to error, and an allow
// list that admits internal imports but not unrelated third parties.
const gpCovLayeredReportPolicy = `
groups:
  - {name: own,   match: ["<self>/..."]}
  - {name: other, match: ["..."]}
types:
  - name: t
    detect: ["github.com/acme/cal/backend"]
    scopes: {source: {allow: [own]}}
layers:
  mode: report
  unknown-role: error
  roles:
    api: ["api4*"]
    dal: ["dal4*"]
  order: [[api],[dal]]
`

func gpCovCheckResult(t *testing.T, body string, module Module, declaredType string) Result {
	t.Helper()
	result, err := Check(gpCovMustLoad(t, body), module, declaredType)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return result
}

// TestGpCovCheckOrdersMixedSeveritiesAndLines proves the finding sort puts
// blocking findings first, then orders by file and line.
func TestGpCovCheckOrdersMixedSeveritiesAndLines(t *testing.T) {
	t.Parallel()
	module := Module{Path: "github.com/acme/cal/backend", References: []Reference{
		// A forbidden third-party import in the source scope: blocking.
		{Import: "github.com/other/thing", File: "app.go", Line: 9, Package: "", Scope: ScopeSource},
		{Import: "github.com/other/thing2", File: "app.go", Line: 3, Package: "", Scope: ScopeSource},
		// A layer inversion inside the module: report mode.
		{Import: "github.com/acme/cal/backend/api4x", File: "dal4x/dal.go", Line: 5, Package: "dal4x", Scope: ScopeSource},
		// An import of a package matching no role: report mode.
		{Import: "github.com/acme/cal/backend/misc4x", File: "api4x/api.go", Line: 7, Package: "api4x", Scope: ScopeSource},
	}}
	result := gpCovCheckResult(t, gpCovLayeredReportPolicy, module, "")

	if result.Blocking() != 2 || result.Reported() != 2 {
		t.Fatalf("blocking/reported = %d/%d, want 2/2: %+v", result.Blocking(), result.Reported(), result.Findings)
	}
	if result.Findings[0].Mode != ModeEnforce || result.Findings[1].Mode != ModeEnforce {
		t.Fatalf("blocking findings are not sorted first: %+v", result.Findings)
	}
	// The two blocking findings share a file; the earlier line must come first.
	if result.Findings[0].Line != 3 || result.Findings[1].Line != 9 {
		t.Fatalf("same-file findings are not ordered by line: %+v", result.Findings)
	}
	var sawLayer, sawRole bool
	for _, finding := range result.Findings {
		sawLayer = sawLayer || finding.Rule == RuleLayer
		sawRole = sawRole || finding.Rule == RuleRole
	}
	if !sawLayer || !sawRole {
		t.Fatalf("expected a layer finding and an unknown-role finding: %+v", result.Findings)
	}
}

// TestGpCovCheckDetectFailureIsReported proves a module no type detects is an
// error rather than a silent empty result.
func TestGpCovCheckDetectFailureIsReported(t *testing.T) {
	t.Parallel()
	policy := gpCovMustLoad(t, `
groups:
  - {name: g, match: ["..."]}
types:
  - name: t
    detect: ["github.com/acme/cal/backend"]
    scopes: {source: {allow: [g]}}
`)
	_, err := Check(policy, Module{Path: "github.com/elsewhere/other"}, "")
	if err == nil {
		t.Fatal("Check accepted a module no declared type detects")
	}
	if !strings.Contains(err.Error(), "no type") {
		t.Fatalf("error = %q, want the detection failure", err)
	}
}

// TestGpCovLayerFindingsWithoutOrder proves a policy with no layer order
// produces no layer findings at all.
func TestGpCovLayerFindingsWithoutOrder(t *testing.T) {
	t.Parallel()
	policy := gpCovMustLoad(t, `
groups:
  - {name: g, match: ["..."]}
types:
  - name: t
    detect: ["github.com/acme/cal/backend"]
    scopes: {source: {allow: [g]}}
`)
	module := Module{Path: "github.com/acme/cal/backend", References: []Reference{
		{Import: "github.com/acme/cal/backend/api4x", File: "dal4x/dal.go", Line: 1, Package: "dal4x", Scope: ScopeSource},
	}}
	result, err := Check(policy, module, "")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	for _, finding := range result.Findings {
		if finding.Rule == RuleLayer {
			t.Fatalf("a policy with no layer order produced a layer finding: %+v", finding)
		}
	}
}

// TestGpCovInternalPackageAndGroupWording pins the small helpers behind
// internal-import detection and refusal wording.
func TestGpCovInternalPackageAndGroupWording(t *testing.T) {
	t.Parallel()
	if target, ok := internalPackage("github.com/a/b", "github.com/a/b"); !ok || target != "" {
		t.Fatalf("internalPackage(module, module) = (%q, %v), want (\"\", true)", target, ok)
	}
	if _, ok := internalPackage("github.com/a/b", "github.com/a/c"); ok {
		t.Fatal("internalPackage accepted a different module")
	}
	if got := describeGroup(GroupUnclassified); !strings.Contains(got, "no group in the policy classifies") {
		t.Fatalf("describeGroup(unclassified) = %q", got)
	}
	if got := describeGroup("third-party"); got != "third-party" {
		t.Fatalf("describeGroup(third-party) = %q, want the group name", got)
	}
}

// TestGpCovSuggestFixFallbacks pins the three shapes of the remedy line: an
// unclassified import, a scope that permits nothing, and a scope whose
// permitted groups are not contracts.
func TestGpCovSuggestFixFallbacks(t *testing.T) {
	t.Parallel()
	policy := gpCovMustLoad(t, `
groups:
  - {name: third-party, match: ["..."]}
types:
  - name: t
    detect: ["x/y"]
    scopes:
      source: {allow: [third-party]}
      tests:  {allow: [third-party]}
`)
	repoType, _ := policy.Type("t")

	if got := suggestFix(policy, repoType, ScopeSource, GroupUnclassified); !strings.Contains(got, "classify it in the policy") {
		t.Fatalf("unclassified fix = %q", got)
	}
	if got := suggestFix(policy, RepoType{}, ScopeSource, "third-party"); got != "" {
		t.Fatalf("fix for a scope permitting nothing = %q, want empty", got)
	}
	if got := suggestFix(policy, repoType, ScopeSource, "third-party"); got != "permitted here: third-party" {
		t.Fatalf("fix = %q, want the permitted-here fallback", got)
	}
}

// gpCovContractPolicy is a policy whose source scope permits a contract group
// beside a third-party group, so the remedy can name the contract import.
const gpCovContractPolicy = `
groups:
  - {name: extension-contract, match: ["github.com/acme/ext-*/..."]}
  - {name: third-party,        match: ["..."]}
types:
  - name: t
    detect: ["github.com/acme/cal/backend"]
    scopes: {source: {allow: [extension-contract]}}
`

// TestGpCovSuggestFixNamesTheContractImport pins the common remedy: an
// implementation import should have been a contract import.
func TestGpCovSuggestFixNamesTheContractImport(t *testing.T) {
	t.Parallel()
	policy := gpCovMustLoad(t, gpCovContractPolicy)
	repoType, _ := policy.Type("t")
	got := suggestFix(policy, repoType, ScopeSource, "extension-implementation")
	if !strings.Contains(got, "github.com/acme/ext-*/...") {
		t.Fatalf("fix = %q, want the contract pattern", got)
	}
}

// TestGpCovLayerRoleAndForbidHelpers pins role resolution and the explicit
// forbidden-edge lookup directly.
func TestGpCovLayerRoleAndForbidHelpers(t *testing.T) {
	t.Parallel()
	layers := Layers{
		Roles: []RoleRule{
			{Role: "api", Patterns: mustCompilePatterns(t, "api4*")},
			{Role: "dal", Patterns: mustCompilePatterns(t, "dal4*")},
		},
		Order:  [][]string{{"api"}, {"dal"}},
		Forbid: []ForbidEdge{{From: "api", To: "dal", Reason: "delivery must go through the facade"}},
	}
	if role, ok := layers.roleOf(""); ok || role != "" {
		t.Fatalf("roleOf(\"\") = (%q, %v), want no role", role, ok)
	}
	if role, ok := layers.roleOf("api4x/nested"); !ok || role != "api" {
		t.Fatalf("roleOf(api4x/nested) = (%q, %v), want api", role, ok)
	}
	if _, ok := layers.roleOf("misc4x"); ok {
		t.Fatal("roleOf matched a package matching no declared role")
	}
	reason, forbidden := layers.forbidden("api", "dal")
	if !forbidden || reason != "delivery must go through the facade" {
		t.Fatalf("forbidden(api, dal) = (%q, %v)", reason, forbidden)
	}
	if _, forbidden := layers.forbidden("dal", "api"); forbidden {
		t.Fatal("forbidden reported an edge the policy does not declare")
	}
}

func mustCompilePatterns(t *testing.T, raw ...string) []Pattern {
	t.Helper()
	patterns, err := compilePatterns(raw)
	if err != nil {
		t.Fatalf("compilePatterns(%q): %v", raw, err)
	}
	return patterns
}

// TestGpCovPatternEdgeCases pins the compile-time and match-time shapes that
// must be refused or must match nothing.
func TestGpCovPatternEdgeCases(t *testing.T) {
	t.Parallel()
	if _, err := CompilePattern("a//b"); err == nil {
		t.Fatal("CompilePattern accepted an empty path segment")
	}
	if _, err := CompilePattern("github.com/acme/[/..."); err == nil {
		t.Fatal("CompilePattern accepted a malformed glob segment")
	}
	if _, err := CompilePattern("a}b"); err == nil {
		t.Fatal("CompilePattern accepted an unmatched closing brace")
	}
	if _, err := CompilePattern("{a,{b}"); err == nil {
		t.Fatal("CompilePattern accepted an unmatched nested opening brace")
	}

	pattern, err := CompilePattern("github.com/acme/...")
	if err != nil {
		t.Fatal(err)
	}
	if pattern.Match("", "") {
		t.Fatal("a pattern matched an empty import path")
	}
	if (Pattern{}).Covers(pattern) {
		t.Fatal("an empty pattern claimed to cover a real one")
	}
	if pattern.Covers(Pattern{}) {
		t.Fatal("a pattern claimed to cover a pattern with no alternatives")
	}
}

// TestGpCovPatternCoversASelfAlternative proves Covers builds a representative
// path for a <self> pattern rather than treating the token literally.
func TestGpCovPatternCoversASelfAlternative(t *testing.T) {
	t.Parallel()
	broad, err := CompilePattern("...")
	if err != nil {
		t.Fatal(err)
	}
	selfPattern, err := CompilePattern("<self>/...")
	if err != nil {
		t.Fatal(err)
	}
	if !broad.Covers(selfPattern) {
		t.Fatal("the catch-all pattern should cover a <self> pattern")
	}
	narrow, err := CompilePattern("github.com/acme/...")
	if err != nil {
		t.Fatal(err)
	}
	if narrow.Covers(selfPattern) {
		t.Fatal("a non-catch-all pattern must not claim to cover <self>")
	}
}

// TestGpCovRepoConfigErrorBranches drives every rejection LoadRepoConfig
// documents for a config that cannot be read, parsed or believed.
func TestGpCovRepoConfigErrorBranches(t *testing.T) {
	t.Parallel()
	writeConfig := func(t *testing.T, body string) string {
		t.Helper()
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ConfigFileName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return root
	}

	cases := []struct {
		name string
		body string
		want string
	}{
		{"unparsable yaml", "policy: [unclosed\n", "parse"},
		{"policy that is not a string", "policy: [a, b]\n", "\"policy\" must be a string"},
		{"type that is not a string", "type: [a]\n", "\"type\" must be a string"},
		{"strict that is not a bool", "strict: sometimes\n", "\"strict\" must be true or false"},
		{"strict false", "strict: false\n", "has no meaning"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := LoadRepoConfig(writeConfig(t, testCase.body))
			if err == nil {
				t.Fatal("LoadRepoConfig accepted an unusable config")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %q, want it to mention %q", err, testCase.want)
			}
		})
	}

	t.Run("unreadable config path", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, ConfigFileName), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadRepoConfig(root); err == nil {
			t.Fatal("LoadRepoConfig ignored a directory where a config file belongs")
		}
	})
}

// TestGpCovSourceLocateErrors pins the two references Locate refuses: a path
// that does not exist and a URL the caller must fetch itself.
func TestGpCovSourceLocateErrors(t *testing.T) {
	t.Parallel()
	pathSource, err := ParseSource("policies/none.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pathSource.Locate(t.TempDir(), nil); err == nil {
		t.Fatal("Locate accepted a missing path reference")
	}

	urlSource, err := ParseSource("https://example.test/policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := urlSource.Locate(t.TempDir(), nil); err == nil {
		t.Fatal("Locate accepted a URL reference")
	}
	if urlSource.Kind != SourceURL || urlSource.URL != "https://example.test/policy.yaml" {
		t.Fatalf("ParseSource(url) = %+v, want a URL source", urlSource)
	}
}

// TestGpCovExplainDescribeAndExpectations pins the declared-type path through
// Explain and Describe, the detection failures both report, and the empty
// summary.
func TestGpCovExplainDescribeAndExpectations(t *testing.T) {
	t.Parallel()
	policy := gpCovMustLoad(t, `
groups:
  - {name: third-party, match: ["..."]}
types:
  - name: app
    detect: ["github.com/acme/cal/backend"]
    scopes: {source: {allow: [third-party]}}
expect:
  - {import: "github.com/dal-go/dalgo", group: third-party}
  - {module: "github.com/acme/cal/backend", type: app}
`)
	// An expectation whose module no type detects must fail with the
	// detection error rather than a bare mismatch.
	policy.Expectations = append(policy.Expectations, Expectation{Module: "github.com/acme/unknown", Type: "app"})
	results := RunExpectations(policy)
	if len(results) != 3 {
		t.Fatalf("RunExpectations produced %d results, want 3", len(results))
	}
	if results[0].Err != "" || !results[0].Passed {
		t.Fatalf("import expectation = %+v, want a pass", results[0])
	}
	if results[1].Err != "" || !results[1].Passed {
		t.Fatalf("module expectation = %+v, want a pass", results[1])
	}
	if results[2].Err == "" {
		t.Fatalf("undetectable module expectation = %+v, want a detection error", results[2])
	}

	explanation, err := Explain(policy, "github.com/acme/cal/backend", "app", "github.com/dal-go/dalgo")
	if err != nil {
		t.Fatalf("Explain with a declared type: %v", err)
	}
	if explanation.RepoType != "app" || explanation.TypeDetected {
		t.Fatalf("Explain = %+v, want the declared type with TypeDetected false", explanation)
	}
	if _, err := Explain(policy, "github.com/acme/unknown", "", "github.com/dal-go/dalgo"); err == nil {
		t.Fatal("Explain accepted a module no type detects")
	}

	effective, err := Describe(policy, "github.com/acme/cal/backend", "app", "config.yaml", false)
	if err != nil {
		t.Fatalf("Describe with a declared type: %v", err)
	}
	if effective.RepoType != "app" || effective.TypeDetected || effective.ConfigPath != "config.yaml" {
		t.Fatalf("Describe = %+v, want the declared type and config path", effective)
	}
	if _, err := Describe(policy, "github.com/acme/cal/backend", "ghost", "config.yaml", false); err == nil {
		t.Fatal("Describe accepted an undeclared type")
	}
	if _, err := Describe(policy, "github.com/acme/unknown", "", "config.yaml", false); err == nil {
		t.Fatal("Describe accepted a module no type detects")
	}

	if got := (Result{}).Summary(); got != "no violations" {
		t.Fatalf("Summary with no findings = %q, want %q", got, "no violations")
	}
}

// gpCovFailAfterWriter succeeds for its first `remaining` writes and then
// fails, so a renderer's error handling can be exercised at every write.
type gpCovFailAfterWriter struct{ remaining int }

func (w *gpCovFailAfterWriter) Write(p []byte) (int, error) {
	if w.remaining <= 0 {
		return 0, errors.New("gpCov: writer exhausted")
	}
	w.remaining--
	return len(p), nil
}

// gpCovFormatResult is one check result carrying every finding shape the
// renderers distinguish: a report-mode import with a fix, an enforce-mode
// layer inversion, and a finding with no line and no import.
func gpCovFormatResult() Result {
	return Result{
		Module:       Module{Path: "github.com/acme/cal/backend", Unparseable: []string{"broken.go", "worse.go"}},
		Policy:       Policy{Source: "/policies/fleet.yaml"},
		Type:         "extension-implementation",
		TypeDetected: true,
		Findings: []Finding{
			{
				Rule: RuleImport, Mode: ModeReport, File: "api4x/api.go", Line: 12,
				Import: "github.com/acme/impl/backend", Group: "extension-implementation", Manifest: true,
				Message: "must not import extension-implementation", Fix: "import the contract instead",
			},
			{
				Rule: RuleLayer, Mode: ModeEnforce, File: "dal4x/dal.go", Line: 4,
				FromRole: "dal", ToRole: "api", Import: "github.com/acme/cal/backend/api4x",
				Message: "dal must not import api",
			},
			{
				Rule: RuleImport, Mode: ModeEnforce, File: "root.go", Line: 0,
				Group: "third-party", Message: "unclassified dependency",
			},
		},
	}
}

// TestGpCovWriteTextRendersEveryFindingShape pins the human rendering: the
// report marker, the manifest note, the layer detail, a finding with no line,
// the fix line, the unparseable warning and the counts.
func TestGpCovWriteTextRendersEveryFindingShape(t *testing.T) {
	t.Parallel()
	var builder strings.Builder
	if err := writeText(&builder, gpCovFormatResult()); err != nil {
		t.Fatalf("writeText: %v", err)
	}
	rendered := builder.String()
	for _, expected := range []string{
		"github.com/acme/cal/backend",
		"detected from the module path",
		"! must not import extension-implementation  (report only — does not fail this check)",
		"group: extension-implementation  (required in go.mod)",
		"|- dal -> api",
		"  root.go\n",
		"fix: import the contract instead",
		"2 file(s) could not be parsed and were not checked: broken.go, worse.go",
		"2 blocking, 1 reported",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("writeText output is missing %q:\n%s", expected, rendered)
		}
	}
	if strings.Contains(rendered, "root.go:0") {
		t.Fatalf("a finding with no line was rendered with a line number:\n%s", rendered)
	}

	// A clean module renders the no-violations line instead of a count.
	clean := Result{Module: Module{Path: "example.test/m"}, Policy: Policy{Source: "p.yaml"}, Type: "t"}
	var cleanOut strings.Builder
	if err := writeText(&cleanOut, clean); err != nil {
		t.Fatalf("writeText: %v", err)
	}
	if !strings.Contains(cleanOut.String(), "no violations") {
		t.Fatalf("clean output = %q, want no violations", cleanOut.String())
	}
}

// TestGpCovWriteGitHubRendersEveryFindingShape pins the annotation rendering:
// notices for report findings, the layer title, the manifest warning for
// unparseable files, and the escaping of workflow delimiters.
func TestGpCovWriteGitHubRendersEveryFindingShape(t *testing.T) {
	t.Parallel()
	result := gpCovFormatResult()
	result.Findings[0].Message = "line one\nline two :: forged"
	var builder strings.Builder
	if err := writeGitHub(&builder, result); err != nil {
		t.Fatalf("writeGitHub: %v", err)
	}
	rendered := builder.String()
	for _, expected := range []string{
		"::notice file=api4x/api.go,line=12,title=dependency policy::",
		"%0A",
		"%3A%3A",
		"::error file=dal4x/dal.go,line=4,title=layer policy::dal must not import api",
		"::warning file=broken.go::not parsed, so not checked",
		"::warning file=worse.go::not parsed, so not checked",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("writeGitHub output is missing %q:\n%s", expected, rendered)
		}
	}
}

// TestGpCovRenderersReportWriterFailures proves every rendering step aborts
// with the writer's error rather than silently truncating its output.
func TestGpCovRenderersReportWriterFailures(t *testing.T) {
	t.Parallel()
	result := gpCovFormatResult()
	for name, render := range map[string]func(*gpCovFailAfterWriter) error{
		"text":   func(w *gpCovFailAfterWriter) error { return writeText(w, result) },
		"github": func(w *gpCovFailAfterWriter) error { return writeGitHub(w, result) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// A writer that fails every write must be reported, so the very
			// first write's error path is exercised too.
			if err := render(&gpCovFailAfterWriter{remaining: 0}); err == nil {
				t.Fatal("the renderer ignored a writer that fails on every write")
			}
			for failAfter := 1; failAfter <= 50; failAfter++ {
				if err := render(&gpCovFailAfterWriter{remaining: failAfter}); err == nil {
					return
				}
			}
			t.Fatal("the renderer never completed even with a writer that cannot fail")
		})
	}
}

// TestGpCovScanModuleRejectsUnusableGoMod pins the two manifests ScanModule
// cannot work from: one that does not parse, and one that declares no module.
func TestGpCovScanModuleRejectsUnusableGoMod(t *testing.T) {
	t.Parallel()
	if _, err := ScanModule(writeModule(t, map[string]string{"go.mod": "module github.com/a/b\nrequire (\n"})); err == nil {
		t.Fatal("ScanModule accepted a go.mod that does not parse")
	}
	if _, err := ScanModule(writeModule(t, map[string]string{"go.mod": "go 1.26\n"})); err == nil {
		t.Fatal("ScanModule accepted a go.mod with no module path")
	}
}

// TestGpCovScanReadsRootPackageAndSameLineImports pins two shapes the scan
// must record faithfully: a file in the module root has an empty package
// directory, and two imports on one line are ordered by import path.
func TestGpCovScanReadsRootPackageAndSameLineImports(t *testing.T) {
	t.Parallel()
	root := writeModule(t, map[string]string{
		"go.mod":  "module github.com/acme/cal/backend\n\ngo 1.26\n",
		"root.go": "package root\n\nimport (\"os\"; \"fmt\")\n\nvar _ = fmt.Sprint\nvar _ = os.Args\n",
	})
	module, err := ScanModule(root)
	if err != nil {
		t.Fatalf("ScanModule: %v", err)
	}
	var imports []string
	for _, reference := range module.References {
		if reference.File != "root.go" {
			continue
		}
		if reference.Package != "" {
			t.Fatalf("root package directory = %q, want empty", reference.Package)
		}
		imports = append(imports, reference.Import)
	}
	if strings.Join(imports, ",") != "fmt,os" {
		t.Fatalf("same-line imports = %v, want them ordered fmt,os", imports)
	}
}

// TestGpCovScanReportsWalkErrors proves a directory the scan cannot read is
// reported rather than silently skipped, when the host enforces permissions.
func TestGpCovScanReportsWalkErrors(t *testing.T) {
	t.Parallel()
	root := writeModule(t, map[string]string{"go.mod": "module github.com/acme/cal/backend\n\ngo 1.26\n"})
	blocked := filepath.Join(root, "blocked")
	if err := os.Mkdir(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })
	if _, err := os.ReadDir(blocked); err == nil {
		// This host does not enforce the mode bits (for example a root test
		// runner), so the walk cannot fail; nothing to assert.
		t.Log("directory permissions are not enforced here; walk-error branch not exercised")
		return
	}
	if _, err := ScanModule(root); err == nil {
		t.Fatal("ScanModule ignored a directory it could not read")
	}
}

// TestGpCovScanReportsUnresolvableWorkingDirectory proves a relative module
// path is refused when the process has no readable working directory to
// resolve it against.
func TestGpCovScanReportsUnresolvableWorkingDirectory(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	if err := os.Chdir(scratch); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	if err := os.Remove(scratch); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanModule("relative-module"); err == nil {
		t.Fatal("ScanModule resolved a relative path with no working directory")
	}
}
