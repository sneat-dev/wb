package secretscan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testScanner(t *testing.T) *Scanner {
	t.Helper()
	scanner, skipped, err := LoadDefault(LoadOptions{EnvExtraRulesPath: strPtr("")})
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	// The vendored ruleset has exactly one path-only rule (pkcs12-file, a
	// file-extension check with no content regex), which is not applicable
	// to text scanning and is expected to be skipped; anything else would
	// be a real regression.
	for _, reason := range skipped {
		if !strings.Contains(reason, "pkcs12-file") {
			t.Fatalf("unexpected skipped rule loading the embedded ruleset: %v", skipped)
		}
	}
	if len(scanner.Rules()) < 100 {
		t.Fatalf("expected the full vendored gitleaks ruleset (~200 rules), got %d", len(scanner.Rules()))
	}
	return scanner
}

func strPtr(s string) *string { return &s }

// The functions below build fixture secret values from split, non-contiguous
// literal fragments. The runtime string is still exactly the shape a real
// credential would have, so the scanner under test evaluates it faithfully,
// but the shape never appears as one contiguous token in this file's own
// source text -- so it cannot itself trip a source-text secret scanner (this
// repository's own gate, or GitHub push protection) on this repository.
// These are provably synthetic: fixed filler characters, never a value that
// was ever live anywhere.
func fakeStripeLiveKey() string   { return "sk_live_" + "51ThisIsAFakeStripeKeyNotReal00000000" }
func fakeAWSAccessKeyIDA() string { return "AKIA" + "ABCDEFGHIJKLMNOP" }
func fakeAWSAccessKeyIDB() string { return "AKIA" + "QWERTYUIOPASDFGH" }
func fakeSlackBotToken() string {
	return "xoxb-" + "1234567890123-1234567890123-fakefakefakefakefakefake"
}

// fakeAmbiguousHeuristicSecret matches generic-api-key's loose
// keyword+entropy shape (a plausible false positive: looks hash- or
// base64-like) but, unlike a real AWS secret access key, is not 40
// contiguous base64 characters -- the hyphens break that specific shape
// while keeping the string's own entropy high.
func fakeAmbiguousHeuristicSecret() string {
	return "cache-key-Xf9K2mZ-7qT8vL3n-R5uW1cY6-bH0jD4gS"
}

// --- Invariant 1: fail closed on named patterns. -----------------------
//
// A match on a high-precision, brand-specific shape refuses the operation.
// Not a warning: warnings in automated pipelines are read by nobody.
func TestNamedPatternsFailClosed(t *testing.T) {
	scanner := testScanner(t)
	cases := []struct {
		name    string
		content string
		ruleID  string
	}{
		{"stripe secret key", `const key = "` + fakeStripeLiveKey() + `"`, "stripe-access-token"},
		{"aws access key id", "AWS_ACCESS_KEY_ID=" + fakeAWSAccessKeyIDA(), "aws-access-token"},
		{"github classic pat", "token: ghp_" + strings.Repeat("A", 36), "github-pat"},
		{"github fine-grained pat", "token: github_pat_" + strings.Repeat("B", 82), "github-fine-grained-pat"},
		{"slack bot token", "SLACK_TOKEN=" + fakeSlackBotToken(), "slack-bot-token"},
		{"pem private key", "-----BEGIN RSA PRIVATE KEY-----\n" + strings.Repeat("MIIFakeNotARealKeyMaterial0123456789/+=\n", 3) + "-----END RSA PRIVATE KEY-----", "private-key"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := scanner.Scan(Segment{Name: "body", Content: []byte(testCase.content)})
			blocking := result.Blocking(nil)
			if len(blocking) == 0 {
				t.Fatalf("expected a blocking finding for %q, got none (all findings: %+v)", testCase.name, result.Findings)
			}
			found := false
			for _, finding := range blocking {
				if finding.RuleID == testCase.ruleID {
					found = true
					if finding.Severity != SeverityBlock {
						t.Fatalf("rule %s severity = %s, want block", finding.RuleID, finding.Severity)
					}
				}
			}
			if !found {
				t.Fatalf("expected rule %q among blocking findings, got %+v", testCase.ruleID, blocking)
			}
			if err := FormatRefusal(blocking); err == nil {
				t.Fatal("FormatRefusal must return a non-nil refusal for blocking findings")
			}
		})
	}
}

func TestCleanContinuationProducesNoFindings(t *testing.T) {
	scanner := testScanner(t)
	content := `Goal: land the secret scanner. Branch feature/secret-scan is pushed;
PR #42 is open against main. Next step: watch CI run 12345678 and merge on
green. No credentials were needed for this step.`
	result := scanner.Scan(Segment{Name: "body", Content: []byte(content)})
	if blocking := result.Blocking(nil); len(blocking) != 0 {
		t.Fatalf("ordinary continuation prose must not match named patterns, got %+v", blocking)
	}
}

// --- Invariant 2: never echo the matched value. -------------------------
//
// Feed a known fake secret and assert the value does not appear in the
// finding, the refusal error, the fingerprint, or any string derived from
// them -- the closest thing this package has to "stdout/stderr/logs".
func TestNeverEchoesMatchedSecret(t *testing.T) {
	scanner := testScanner(t)
	fakeSecret := fakeAWSAccessKeyIDB()
	content := "leftover debug line: AWS_ACCESS_KEY_ID=" + fakeSecret + " (do not commit)"
	result := scanner.Scan(Segment{Name: "body", Content: []byte(content)})
	blocking := result.Blocking(nil)
	if len(blocking) == 0 {
		t.Fatal("expected a blocking finding to exercise the never-echo guarantee against")
	}

	err := FormatRefusal(blocking)
	if err == nil {
		t.Fatal("expected a refusal error")
	}

	var surfaces []string
	surfaces = append(surfaces, err.Error())
	for _, finding := range result.Findings {
		surfaces = append(surfaces,
			finding.String(),
			finding.Key(),
			finding.Fingerprint,
			finding.RuleID,
			finding.Description,
			fmt.Sprintf("%+v", finding),
			fmt.Sprintf("%#v", finding),
		)
	}
	surfaces = append(surfaces, fmt.Sprintf("%+v", result), fmt.Sprintf("%#v", result))

	for _, surface := range surfaces {
		if strings.Contains(surface, fakeSecret) {
			t.Fatalf("secret scan surface leaked the matched value: %q contains %q", surface, fakeSecret)
		}
		// Also guard against a partial echo: nobody should be able to
		// reconstruct the secret from a long overlapping substring either.
		if len(fakeSecret) > 8 && strings.Contains(surface, fakeSecret[4:len(fakeSecret)-4]) {
			t.Fatalf("secret scan surface leaked a long substring of the matched value: %q", surface)
		}
	}
}

// --- Invariant 3: named patterns fail closed; entropy heuristics only warn.
func TestGenericHeuristicNeverBlocks(t *testing.T) {
	scanner := testScanner(t)
	// Trips generic-api-key's loose `key = <high-entropy blob>` shape but no
	// brand-specific rule: a plausible false positive (looks like a hash or
	// random base64 blob), which is exactly what this invariant protects.
	content := `local_cache_key = "` + fakeAmbiguousHeuristicSecret() + `"`
	result := scanner.Scan(Segment{Name: "body", Content: []byte(content)})

	blocking := result.Blocking(nil)
	for _, finding := range blocking {
		if finding.RuleID == "generic-api-key" {
			t.Fatalf("generic-api-key must never be severity block, got a blocking finding: %+v", finding)
		}
	}
	warnings := result.Warnings(nil)
	sawWarning := false
	for _, finding := range warnings {
		if finding.RuleID == "generic-api-key" {
			sawWarning = true
			if finding.Severity != SeverityWarn {
				t.Fatalf("generic-api-key severity = %s, want warn", finding.Severity)
			}
		}
	}
	if !sawWarning {
		t.Fatalf("expected generic-api-key to at least warn on a plausible key=entropy-blob line; findings: %+v", result.Findings)
	}
	if err := FormatRefusal(blocking); err != nil {
		t.Fatalf("a warn-only finding must never produce a refusal, got: %v", err)
	}
}

func TestHeuristicRuleIDsAreExplicitAndSmall(t *testing.T) {
	// Guards against the classification silently growing or shrinking by
	// accident: every downgrade to warn is a reviewable, named decision
	// (see policy.go), not something derived from a regex shape check.
	if len(heuristicRuleIDs) != 1 || !heuristicRuleIDs["generic-api-key"] {
		t.Fatalf("heuristicRuleIDs drifted from the reviewed set: %+v", heuristicRuleIDs)
	}
}

// --- Invariant 4: extensible via config, not code. ----------------------
func TestExtraRulesFileAddsPatternWithoutTouchingCode(t *testing.T) {
	dir := t.TempDir()
	extraPath := filepath.Join(dir, "rules.toml")
	extraTOML := `
[[rules]]
id = "acme-internal-token"
description = "ACME internal service token"
regex = '''ACME_TOK_[A-Z0-9]{20}'''
keywords = ["acme_tok_"]
`
	if err := os.WriteFile(extraPath, []byte(extraTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	scanner, skipped, err := LoadDefault(LoadOptions{EnvExtraRulesPath: &extraPath})
	if err != nil {
		t.Fatalf("LoadDefault with extra rules file: %v", err)
	}
	for _, reason := range skipped {
		if !strings.Contains(reason, "pkcs12-file") {
			t.Fatalf("well-formed extra rules file should not be skipped: %v", skipped)
		}
	}

	content := "internal token: ACME_TOK_ABCDEFGHIJ0123456789"
	result := scanner.Scan(Segment{Name: "body", Content: []byte(content)})
	blocking := result.Blocking(nil)
	found := false
	for _, finding := range blocking {
		if finding.RuleID == "acme-internal-token" {
			found = true
			if finding.Severity != SeverityBlock {
				t.Fatalf("custom rule without severity=warn must default to block, got %s", finding.Severity)
			}
			if finding.Source != extraPath {
				t.Fatalf("finding.Source = %q, want extra rules file path %q", finding.Source, extraPath)
			}
		}
	}
	if !found {
		t.Fatalf("expected the config-defined acme-internal-token rule to fire; findings: %+v", result.Findings)
	}
}

func TestExtraRuleCanOptIntoWarnOnly(t *testing.T) {
	dir := t.TempDir()
	extraPath := filepath.Join(dir, "rules.toml")
	extraTOML := `
[[rules]]
id = "acme-noisy-heuristic"
description = "ACME noisy heuristic, warn only"
regex = '''noisy-[a-z0-9]{8}'''
severity = "warn"
`
	if err := os.WriteFile(extraPath, []byte(extraTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	scanner, _, err := LoadDefault(LoadOptions{EnvExtraRulesPath: &extraPath})
	if err != nil {
		t.Fatal(err)
	}
	result := scanner.Scan(Segment{Name: "body", Content: []byte("noisy-a1b2c3d4")})
	if blocking := result.Blocking(nil); len(blocking) != 0 {
		t.Fatalf("severity=warn extra rule must never block, got %+v", blocking)
	}
	if warnings := result.Warnings(nil); len(warnings) == 0 {
		t.Fatal("expected the warn-only extra rule to still be reported")
	}
}

func TestUnusableExtraRuleIsSkippedNotFatal(t *testing.T) {
	dir := t.TempDir()
	extraPath := filepath.Join(dir, "rules.toml")
	// An invalid regex (unbalanced group) must not take down the whole gate.
	extraTOML := `
[[rules]]
id = "broken-rule"
description = "bad regex"
regex = '''(unbalanced'''
`
	if err := os.WriteFile(extraPath, []byte(extraTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	scanner, skipped, err := LoadDefault(LoadOptions{EnvExtraRulesPath: &extraPath})
	if err != nil {
		t.Fatalf("an unusable rule must be skipped, not fatal: %v", err)
	}
	if len(skipped) == 0 {
		t.Fatal("expected the unusable rule to be reported as skipped")
	}
	if len(scanner.Rules()) < 100 {
		t.Fatal("the embedded baseline must still load despite one bad extra rule")
	}
}

// --- Invariant 6: a clear, actionable refusal. ---------------------------
func TestRefusalNamesRuleLocationAndHowToProceed(t *testing.T) {
	scanner := testScanner(t)
	content := "line one\nline two AWS_ACCESS_KEY_ID=" + fakeAWSAccessKeyIDA() + " trailing"
	result := scanner.Scan(Segment{Name: "handover-body", Content: []byte(content)})
	blocking := result.Blocking(nil)
	if len(blocking) == 0 {
		t.Fatal("expected a blocking finding")
	}
	err := FormatRefusal(blocking)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	message := err.Error()
	for _, mustContain := range []string{
		"aws-access-token",  // which rule
		"handover-body",     // where: segment
		"line 2",            // where: location
		"--override-secret", // how to proceed
		"redact",
	} {
		if !strings.Contains(message, mustContain) {
			t.Fatalf("refusal message missing %q; got:\n%s", mustContain, message)
		}
	}
}

// --- Overrides: explicit, logged, never the path of least resistance. ---
func TestOverrideRequiresExactRuleAndFingerprint(t *testing.T) {
	scanner := testScanner(t)
	content := "AWS_ACCESS_KEY_ID=" + fakeAWSAccessKeyIDA()
	result := scanner.Scan(Segment{Name: "body", Content: []byte(content)})
	blocking := result.Blocking(nil)
	if len(blocking) != 1 {
		t.Fatalf("expected exactly one blocking finding, got %+v", blocking)
	}
	finding := blocking[0]

	// A guessed or unrelated override does not suppress the block.
	wrongOverrides, err := ParseOverrides([]string{"aws-access-token:sha256:00000000 len=20"})
	if err != nil {
		t.Fatal(err)
	}
	if still := result.Blocking(wrongOverrides); len(still) != 1 {
		t.Fatalf("a fabricated override must not suppress the finding, got %+v", still)
	}

	// The exact key the refusal printed does suppress it, and it still
	// shows up as a warning -- overriding never means "delete the record".
	exactOverrides, err := ParseOverrides([]string{finding.Key()})
	if err != nil {
		t.Fatal(err)
	}
	if still := result.Blocking(exactOverrides); len(still) != 0 {
		t.Fatalf("the exact acknowledged key must suppress the block, got %+v", still)
	}
	warnings := result.Warnings(exactOverrides)
	sawOverriddenFinding := false
	for _, warning := range warnings {
		if warning.RuleID == "aws-access-token" {
			sawOverriddenFinding = true
		}
	}
	if !sawOverriddenFinding {
		t.Fatalf("an overridden finding must still be surfaced as a warning (logged), got %+v", warnings)
	}
}

func TestParseOverridesRejectsMalformedInput(t *testing.T) {
	for _, bad := range []string{"", "no-colon-here", ":missing-rule-id", "rule-id-only:"} {
		if bad == "" {
			continue // empty entries are allowed (ignored) so a flag default of "" is harmless
		}
		if _, err := ParseOverrides([]string{bad}); err == nil {
			t.Fatalf("expected ParseOverrides(%q) to fail", bad)
		}
	}
}

func TestFingerprintNeverContainsRawSecretCharacters(t *testing.T) {
	secret := []byte(fakeStripeLiveKey())
	fingerprint := Fingerprint(secret)
	if strings.Contains(fingerprint, string(secret)) {
		t.Fatalf("fingerprint leaked the secret: %s", fingerprint)
	}
	for i := 0; i+4 <= len(secret); i++ {
		if strings.Contains(fingerprint, string(secret[i:i+4])) {
			t.Fatalf("fingerprint %q leaked a 4-byte window of the secret", fingerprint)
		}
	}
	if Fingerprint(secret) != fingerprint {
		t.Fatal("Fingerprint must be deterministic for the same input, so --override-secret can name it exactly")
	}
}
