package policy

import (
	"strings"
	"testing"
)

func TestExplainShowsWinningPatternAndPerScopeVerdict(t *testing.T) {
	loaded := loadSample(t)
	explanation, err := Explain(loaded, "github.com/acme/cal/backend", "", "github.com/dal-go/dalgo2firestore")
	if err != nil {
		t.Fatal(err)
	}
	if explanation.RepoType != "extension-implementation" || !explanation.TypeDetected {
		t.Fatalf("type = %q detected = %v", explanation.RepoType, explanation.TypeDetected)
	}
	if explanation.Classification.Group != "dalgo-adapter" {
		t.Fatalf("group = %q", explanation.Classification.Group)
	}
	if explanation.Classification.Pattern == "" || explanation.Classification.PatternNumber == 0 {
		t.Fatalf("explanation must name the winning pattern: %+v", explanation.Classification)
	}
	verdicts := map[string]bool{}
	for _, verdict := range explanation.Scopes {
		verdicts[verdict.Scope] = verdict.Allowed
	}
	if verdicts[ScopeSource] {
		t.Fatal("dalgo-adapter must be forbidden in source")
	}
	if !verdicts[ScopeTests] {
		t.Fatal("dalgo-adapter must be allowed in tests")
	}
}

func TestExplainRejectsUnknownType(t *testing.T) {
	loaded := loadSample(t)
	if _, err := Explain(loaded, "github.com/acme/cal/backend", "ghost", "fmt"); err == nil {
		t.Fatal("expected an error for an undeclared type")
	}
}

func TestRunExpectations(t *testing.T) {
	loaded := loadSample(t)
	results := RunExpectations(loaded)
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	for _, result := range results {
		if !result.Passed {
			t.Fatalf("expectation failed: %s want %q got %q %s", result.Subject, result.Want, result.Got, result.Err)
		}
	}
}

func TestRunExpectationsReportsAMismatch(t *testing.T) {
	body := strings.Replace(samplePolicy,
		`  - {import: "github.com/acme/ext-cal/backend/facade", group: extension-contract}`,
		`  - {import: "github.com/acme/ext-cal/backend/facade", group: extension-implementation}`, 1)
	loaded, err := Load(writePolicy(t, body))
	if err != nil {
		t.Fatal(err)
	}
	results := RunExpectations(loaded)
	if results[0].Passed {
		t.Fatal("a wrong expectation should fail")
	}
	if results[0].Got != "extension-contract" {
		t.Fatalf("got = %q", results[0].Got)
	}
}

func TestDescribeResolvesEffectiveRules(t *testing.T) {
	loaded := loadSample(t)
	effective, err := Describe(loaded, "github.com/acme/cal/backend", "", "/repo/.wb-deps-policy.yaml", false)
	if err != nil {
		t.Fatal(err)
	}
	if effective.RepoType != "extension-implementation" {
		t.Fatalf("type = %q", effective.RepoType)
	}
	if effective.LayerMode != ModeReport {
		t.Fatalf("layer mode = %q, want the policy's report", effective.LayerMode)
	}
	if effective.LayerOrder == "" {
		t.Fatal("layer order should be described")
	}
	var tests ScopeVerdict
	for _, scope := range effective.Scopes {
		if scope.Scope == ScopeTests {
			tests = scope
		}
	}
	if !containsString(tests.Allow, "dalgo-adapter") {
		t.Fatalf("tests allow = %v", tests.Allow)
	}
}

func TestDescribeStrictPromotesLayerMode(t *testing.T) {
	loaded := loadSample(t)
	effective, err := Describe(loaded, "github.com/acme/cal/backend", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if effective.LayerMode != ModeEnforce {
		t.Fatalf("strict should promote report to enforce, got %q", effective.LayerMode)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
