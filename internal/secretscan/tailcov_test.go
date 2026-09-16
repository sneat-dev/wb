package secretscan

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// tailCovExtraRulesTOML is a well-formed operator rules file with a shape
// nothing in the embedded gitleaks corpus matches.
const tailCovExtraRulesTOML = `
[[rules]]
id = "tailcov-internal-token"
description = "tailCov internal token"
regex = '''TAILCOV_TOK_[A-Z0-9]{8}'''
keywords = ["tailcov_tok_"]
`

// tailCovWriteRulesFile stages one extra rules file and returns its path.
func tailCovWriteRulesFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// tailCovFindingSources returns the rules that load from path, proving the
// rules file at that path was actually consumed.
func tailCovAssertExtraRuleLoaded(t *testing.T, scanner *Scanner, wantSource string) {
	t.Helper()
	result := scanner.Scan(Segment{Name: "body", Content: []byte("token TAILCOV_TOK_ABCD1234 here")})
	blocking := result.Blocking(nil)
	for _, finding := range blocking {
		if finding.RuleID != "tailcov-internal-token" {
			continue
		}
		if finding.Source != wantSource {
			t.Fatalf("finding.Source = %q, want %q", finding.Source, wantSource)
		}
		return
	}
	t.Fatalf("the extra rule did not fire; findings = %+v", result.Findings)
}

// TestTailCovUserRulesPathReportsUndeterminableConfigDir pins that a machine
// with no HOME (and on Unix no XDG_CONFIG_HOME) has no user-level rules path
// at all -- reported as not-found rather than a bogus relative path.
func TestTailCovUserRulesPathReportsUndeterminableConfigDir(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	path, found := UserRulesPath("")
	if found {
		t.Fatalf("UserRulesPath = (%q, true), want not found with no config directory", path)
	}
	if path != "" {
		t.Fatalf("UserRulesPath path = %q, want empty", path)
	}
}

// TestTailCovUserRulesPathUnderExplicitConfigDir pins the exact user-level
// location: <config dir>/wb/secretscan/rules.toml.
func TestTailCovUserRulesPathUnderExplicitConfigDir(t *testing.T) {
	configDir := t.TempDir()

	path, found := UserRulesPath(configDir)
	want := filepath.Join(configDir, "wb", "secretscan", "rules.toml")
	if !found || path != want {
		t.Fatalf("UserRulesPath(%q) = (%q, %v), want (%q, true)", configDir, path, found, want)
	}
}

// TestTailCovLoadDefaultReadsRulesFileFromEnvironment covers the documented
// first lookup source: WB_SECRETSCAN_RULES, used when no explicit path is
// supplied. The loaded rule must be usable, and attributed to that file.
func TestTailCovLoadDefaultReadsRulesFileFromEnvironment(t *testing.T) {
	extraPath := tailCovWriteRulesFile(t, filepath.Join(t.TempDir(), "env-rules.toml"), tailCovExtraRulesTOML)
	t.Setenv(ExtraRulesEnvVar, extraPath)

	scanner, _, err := LoadDefault(LoadOptions{})
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	tailCovAssertExtraRuleLoaded(t, scanner, extraPath)
}

// TestTailCovLoadDefaultPicksUpUserLevelRulesFile covers the second lookup
// source: the per-user default path, consulted only when the environment
// names nothing. A rules file that exists there must be loaded without any
// flag or environment variable.
func TestTailCovLoadDefaultPicksUpUserLevelRulesFile(t *testing.T) {
	configDir := t.TempDir()
	userPath := tailCovWriteRulesFile(t, filepath.Join(configDir, "wb", "secretscan", "rules.toml"), tailCovExtraRulesTOML)
	t.Setenv(ExtraRulesEnvVar, "")

	scanner, _, err := LoadDefault(LoadOptions{UserConfigDir: configDir})
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	tailCovAssertExtraRuleLoaded(t, scanner, userPath)
}

// TestTailCovLoadDefaultIgnoresUserLevelPathWhenNoRulesFileExists pins that
// the absent user-level file is the common case and not an error: the
// embedded baseline alone must load.
func TestTailCovLoadDefaultIgnoresUserLevelPathWhenNoRulesFileExists(t *testing.T) {
	t.Setenv(ExtraRulesEnvVar, "")

	scanner, skipped, err := LoadDefault(LoadOptions{UserConfigDir: t.TempDir()})
	if err != nil {
		t.Fatalf("LoadDefault with no extra rules file: %v", err)
	}
	if len(scanner.Rules()) < 100 {
		t.Fatalf("embedded baseline did not load: %d rules", len(scanner.Rules()))
	}
	for _, reason := range skipped {
		if !strings.Contains(reason, "pkcs12-file") {
			t.Fatalf("unexpected skipped rule: %v", skipped)
		}
	}
}

// TestTailCovLoadDefaultReportsUnloadableExtraRules covers an explicitly
// configured rules file that cannot be loaded: it is a hard error, not a
// silent fall back to the baseline (an operator who believes their rule is
// armed must not be wrong about it).
func TestTailCovLoadDefaultReportsUnloadableExtraRules(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent-rules.toml")

	scanner, skipped, err := LoadDefault(LoadOptions{EnvExtraRulesPath: &missing})
	if err == nil {
		t.Fatal("LoadDefault succeeded with an unreadable extra rules file")
	}
	if scanner != nil || skipped != nil {
		t.Fatalf("LoadDefault = (%v, %v, %v), want (nil, nil, err)", scanner, skipped, err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("error = %v, want it to name %s", err, missing)
	}
}

// TestTailCovLoadRulesFileReportsUnreadableFile covers the single-file
// loader's own read failure.
func TestTailCovLoadRulesFileReportsUnreadableFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "never-written.toml")

	rules, skipped, err := LoadRulesFile(missing)
	if err == nil {
		t.Fatal("LoadRulesFile succeeded for a file that does not exist")
	}
	if rules != nil || skipped != nil {
		t.Fatalf("LoadRulesFile = (%v, %v, %v), want (nil, nil, err)", rules, skipped, err)
	}
	if !strings.Contains(err.Error(), "read secret scan rules file") || !strings.Contains(err.Error(), missing) {
		t.Fatalf("error = %v, want a read failure naming %s", err, missing)
	}
}

// TestTailCovLoadDefaultReportsUnparseableEmbeddedRuleset covers the
// fail-closed half of the "the gate always works offline" promise: if the
// embedded baseline itself cannot be parsed (it is packaged data, not a Go
// constant), LoadDefault reports that as a load failure instead of returning
// a scanner that would silently match nothing. The embedded bytes are
// swapped in-process and restored immediately, so no other test observes
// them.
func TestTailCovLoadDefaultReportsUnparseableEmbeddedRuleset(t *testing.T) {
	original := embeddedGitleaksRuleset
	embeddedGitleaksRuleset = []byte("[[rules]\nid = \"broken\"\n")
	defer func() { embeddedGitleaksRuleset = original }()

	empty := ""
	scanner, skipped, err := LoadDefault(LoadOptions{EnvExtraRulesPath: &empty})
	if err == nil {
		t.Fatal("LoadDefault accepted an unparseable embedded ruleset")
	}
	if scanner != nil || skipped != nil {
		t.Fatalf("LoadDefault = (%v, %v, %v), want (nil, nil, err)", scanner, skipped, err)
	}
	if !strings.Contains(err.Error(), "load embedded secret scan ruleset") || !strings.Contains(err.Error(), "gitleaks-embedded") {
		t.Fatalf("error = %v, want it to identify the embedded ruleset", err)
	}
	if rules := EmbeddedRuleset(); string(rules) != "[[rules]\nid = \"broken\"\n" {
		t.Fatalf("EmbeddedRuleset() = %q, want the swapped bytes to be what was parsed", rules)
	}
}

// TestTailCovParseTOMLRulesetReportsUnparseableDocument pins that a malformed
// extra rules file is an error attributed to the file it came from, rather
// than an empty ruleset.
func TestTailCovParseTOMLRulesetReportsUnparseableDocument(t *testing.T) {
	rules, skipped, err := parseTOMLRuleset([]byte("[[rules]\nid = \"x\"\n"), "tailcov-broken.toml", classifyExtraRule)
	if err == nil {
		t.Fatal("parseTOMLRuleset accepted a malformed TOML document")
	}
	if rules != nil || skipped != nil {
		t.Fatalf("parseTOMLRuleset = (%v, %v, %v), want (nil, nil, err)", rules, skipped, err)
	}
	if !strings.Contains(err.Error(), "tailcov-broken.toml") {
		t.Fatalf("error = %v, want it to name the source file", err)
	}
}

// TestTailCovParseTOMLRulesetSkipsEmptyAndDuplicateIDs pins the two identity
// guards: a rule with no id cannot be reported or overridden, and a rule that
// repeats an id would shadow the first. Both are skipped with a reason, and
// the surrounding rules still load.
func TestTailCovParseTOMLRulesetSkipsEmptyAndDuplicateIDs(t *testing.T) {
	document := `
[[rules]]
description = "no id at all"
regex = '''anon-[a-z]+'''

[[rules]]
id = "twice"
description = "first"
regex = '''first-[a-z]+'''

[[rules]]
id = "twice"
description = "duplicate"
regex = '''second-[a-z]+'''
`
	rules, skipped, err := parseTOMLRuleset([]byte(document), "tailcov-extra.toml", classifyExtraRule)
	if err != nil {
		t.Fatalf("parseTOMLRuleset: %v", err)
	}
	if len(rules) != 1 || rules[0].ID != "twice" {
		t.Fatalf("rules = %+v, want only the first \"twice\" rule", rules)
	}
	if rules[0].Description != "first" {
		t.Fatalf("the duplicate shadowed the first rule: %+v", rules[0])
	}
	var emptyID, duplicateID bool
	for _, reason := range skipped {
		if strings.Contains(reason, "tailcov-extra.toml") && strings.Contains(reason, "empty id") {
			emptyID = true
		}
		if strings.Contains(reason, "tailcov-extra.toml") && strings.Contains(reason, "duplicate rule id") {
			duplicateID = true
		}
	}
	if !emptyID {
		t.Fatalf("the id-less rule was not reported as skipped: %v", skipped)
	}
	if !duplicateID {
		t.Fatalf("the duplicate rule was not reported as skipped: %v", skipped)
	}
}

// TestTailCovParseOverridesIgnoresBlankValues pins that a blank CLI value is
// a no-op rather than a malformed override: repeatable flags commonly leave
// empty entries behind, and only a non-empty value must be well-formed.
func TestTailCovParseOverridesIgnoresBlankValues(t *testing.T) {
	overrides, err := ParseOverrides([]string{"", "   ", "aws-access-token:sha256:4f9c2a1b"})
	if err != nil {
		t.Fatalf("ParseOverrides: %v", err)
	}
	if len(overrides) != 1 || !overrides["aws-access-token:sha256:4f9c2a1b"] {
		t.Fatalf("overrides = %+v, want only the one real value", overrides)
	}
}

// TestTailCovScanOrdersFindingsBySegmentLineAndRule pins the full ordering
// contract of a scan: segment first, then line, then column, then rule id.
// The rule-id tie-break matters because two rules can match the same bytes at
// the same position, and the refusal output must not depend on load order.
func TestTailCovScanOrdersFindingsBySegmentLineAndRule(t *testing.T) {
	scanner := NewScanner([]Rule{
		{ID: "b-rule", Description: "b", Regex: regexp.MustCompile("TOK"), Keywords: []string{"tok"}, Severity: SeverityBlock, Source: "tailcov"},
		{ID: "a-rule", Description: "a", Regex: regexp.MustCompile("TOK"), Keywords: []string{"tok"}, Severity: SeverityBlock, Source: "tailcov"},
	})

	result := scanner.Scan(
		Segment{Name: "b-segment", Content: []byte("TOK")},
		Segment{Name: "a-segment", Content: []byte("TOK\n\nTOK")},
	)

	var got []string
	for _, finding := range result.Findings {
		got = append(got, fmt.Sprintf("%s/%s/L%dC%d", finding.Segment, finding.RuleID, finding.Line, finding.Column))
	}
	want := []string{
		"a-segment/a-rule/L1C1",
		"a-segment/b-rule/L1C1",
		"a-segment/a-rule/L3C1",
		"a-segment/b-rule/L3C1",
		"b-segment/a-rule/L1C1",
		"b-segment/b-rule/L1C1",
	}
	if len(got) != len(want) {
		t.Fatalf("findings = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("findings = %v, want %v", got, want)
		}
	}
}
