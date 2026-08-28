package secretscan

// heuristicRuleIDs names the vendored gitleaks rules WB downgrades to
// SeverityWarn because their regex has no fixed, brand-specific shape --
// they match a generic `key[-_]?=<entropy blob>` assignment pattern rather
// than a literal prefix like "AKIA", "ghp_", or "sk_live_". Blocking on
// these false-positives on hashes, UUIDs, and base64 blobs, which teaches
// agents to bypass the check -- worse than not having it. Every other rule
// in the vendored ruleset anchors on a concrete, brand-specific literal
// (even the ones that also carry a secondary entropy threshold as a
// confidence refinement) and is safe to block on.
//
// This is WB's own policy call layered on top of gitleaks' corpus, not
// something gitleaks' TOML declares -- gitleaks' default config has no
// severity/confidence field of its own. Keep this list short and named
// explicitly; do not derive it heuristically (e.g. "no literal prefix"),
// so every downgrade is a reviewable, intentional decision.
var heuristicRuleIDs = map[string]bool{
	"generic-api-key": true,
}

// classifyEmbeddedRule assigns Severity to a rule parsed from the vendored
// gitleaks ruleset (or an operator-supplied drop-in replacement using the
// same gitleaks schema, e.g. a refreshed config/gitleaks.toml -- see
// gitleaks/PROVENANCE.md).
func classifyEmbeddedRule(raw rawTOMLRule) Severity {
	if heuristicRuleIDs[raw.ID] {
		return SeverityWarn
	}
	return SeverityBlock
}

// classifyExtraRule assigns Severity to a rule parsed from a repo- or
// user-level extra rules file (invariant: extensible via config, not code).
// An operator adding a custom rule is, by definition, naming a shape they
// want enforced (e.g. an internal token prefix), so it defaults to
// SeverityBlock. The file may still opt a specific rule into warn-only via
// the WB-only `severity = "warn"` field.
func classifyExtraRule(raw rawTOMLRule) Severity {
	if raw.Severity == "warn" {
		return SeverityWarn
	}
	return SeverityBlock
}
