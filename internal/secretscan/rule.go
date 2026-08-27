// Package secretscan is WB's deterministic secret-shape gate for
// agent-authored continuation text (wb session move's handover and wb
// session park's continuation). It exists because that text is written by an
// AI, persisted, handed to another agent, and eventually pushed -- so
// anything in it is effectively published. Guidance alone is not trusted
// here because its failure is silent: a leaked key in a continuation reads
// exactly like a good continuation.
//
// WB does not maintain its own secret-shape corpus. It parses gitleaks'
// maintained TOML rule format (https://github.com/gitleaks/gitleaks) --
// vendored at vendor/gitleaks/gitleaks.toml, see
// vendor/gitleaks/PROVENANCE.md for the exact pinned tag and how to refresh
// it without a WB release -- and applies its own fail-closed/warn-only
// policy on top. WB owns the integration and the blocking decision, never
// the regex corpus.
package secretscan

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

//go:embed vendor/gitleaks/gitleaks.toml
var embeddedGitleaksRuleset []byte

// Severity is WB's own policy classification for a rule, not anything
// gitleaks declares. See policy.go for how it is assigned.
type Severity string

const (
	// SeverityBlock rules refuse the operation outright: fail closed. Every
	// rule loaded from the vendored/embedded ruleset is Block unless it is
	// named in heuristicRuleIDs.
	SeverityBlock Severity = "block"
	// SeverityWarn rules are reported but never refuse. Reserved for
	// entropy-driven heuristics that lack a fixed, brand-specific shape and
	// are known to false-positive on hashes, UUIDs, and base64 blobs.
	SeverityWarn Severity = "warn"
)

// Rule is one compiled secret-shape detector.
type Rule struct {
	ID          string
	Description string
	Regex       *regexp.Regexp
	// Keywords are a cheap case-insensitive substring pre-filter: if none of
	// them appear in the scanned content, the (potentially expensive) regex
	// is skipped. An empty Keywords list means always evaluate the regex.
	Keywords []string
	Severity Severity
	// Source names where this rule came from, for refusal messages and
	// audit trails: "gitleaks-embedded", or the loaded config file path.
	Source string
}

// rawTOMLRuleset mirrors just the fields WB reads from a gitleaks-schema
// rules file. Unknown keys (title, minVersion, [allowlist], per-rule
// [[rules.allowlists]], ...) decode into nothing and are silently ignored;
// WB does not implement gitleaks' allowlist/stopword suppression semantics.
type rawTOMLRuleset struct {
	Rules []rawTOMLRule `toml:"rules"`
}

type rawTOMLRule struct {
	ID          string   `toml:"id"`
	Description string   `toml:"description"`
	Regex       string   `toml:"regex"`
	Entropy     float64  `toml:"entropy"`
	Keywords    []string `toml:"keywords"`
	// Severity is a WB extension, not part of gitleaks' schema. It is
	// meaningful only for config-loaded extra rules (see config.go): a
	// custom rule defaults to block ("an internal token shape can be added"
	// implies enforcement) but may opt into "warn" explicitly.
	Severity string `toml:"severity"`
}

// parseTOMLRuleset decodes a gitleaks-schema TOML document (embedded or an
// operator-supplied extra rules file) into compiled Rules. classify assigns
// the WB Severity for each parsed rule by ID; source is recorded on every
// rule for refusal/audit messages. A rule whose regex fails to compile is
// skipped, not fatal: one bad third-party or hand-edited entry must never
// silently disable the whole gate.
func parseTOMLRuleset(data []byte, source string, classify func(rawTOMLRule) Severity) ([]Rule, []string, error) {
	var parsed rawTOMLRuleset
	if _, err := toml.Decode(string(data), &parsed); err != nil {
		return nil, nil, fmt.Errorf("parse secret scan ruleset %s: %w", source, err)
	}
	var rules []Rule
	var skipped []string
	seen := map[string]bool{}
	for _, raw := range parsed.Rules {
		id := strings.TrimSpace(raw.ID)
		if id == "" {
			skipped = append(skipped, fmt.Sprintf("%s: rule with empty id", source))
			continue
		}
		if seen[id] {
			skipped = append(skipped, fmt.Sprintf("%s: duplicate rule id %q", source, id))
			continue
		}
		if strings.TrimSpace(raw.Regex) == "" {
			// gitleaks also defines path-only rules (a `path` key matched
			// against a file's path, e.g. "*.p12") instead of `regex`. WB
			// scans agent-authored text, not a file tree, so a rule with no
			// content regex has nothing to evaluate and is skipped rather
			// than treated as "matches everything".
			skipped = append(skipped, fmt.Sprintf("%s: rule %q has no content regex (path-only rule, not applicable to text scanning)", source, id))
			continue
		}
		compiled, err := regexp.Compile(raw.Regex)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: rule %q has an unusable regex: %v", source, id, err))
			continue
		}
		seen[id] = true
		keywords := make([]string, 0, len(raw.Keywords))
		for _, keyword := range raw.Keywords {
			keyword = strings.ToLower(strings.TrimSpace(keyword))
			if keyword != "" {
				keywords = append(keywords, keyword)
			}
		}
		rules = append(rules, Rule{
			ID:          id,
			Description: strings.TrimSpace(raw.Description),
			Regex:       compiled,
			Keywords:    keywords,
			Severity:    classify(raw),
			Source:      source,
		})
	}
	return rules, skipped, nil
}

// EmbeddedRuleset returns the byte-for-byte vendored gitleaks ruleset WB
// ships inside its own binary, so the gate always works even with no
// network access and no external config file. See
// vendor/gitleaks/PROVENANCE.md for exact provenance.
func EmbeddedRuleset() []byte {
	return embeddedGitleaksRuleset
}
