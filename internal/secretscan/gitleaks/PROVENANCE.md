# Vendored gitleaks ruleset

`gitleaks.toml` in this directory is copied verbatim from:

- Upstream: https://github.com/gitleaks/gitleaks
- Tag: `v8.30.1`
- Commit: `83d9cd684c87d95d656c1458ef04895a7f1cbd8e`
- Path: `config/gitleaks.toml`
- Vendored: 2026-08-27

Byte-for-byte except one trailing blank line trimmed to satisfy WB's own
pre-commit hook (no blank line at EOF); no rule content was touched.

WB does not maintain this file's content. It is gitleaks' own maintained
corpus of ~200 secret-shape detection rules (`[[rules]]` entries: id,
description, regex, keywords, and an optional secondary entropy threshold).
WB's `internal/secretscan` package parses this TOML using the same schema
gitleaks itself defines and applies its own fail-closed/warn-only policy on
top (see `../policy.go`) — WB owns the integration and the blocking
decision, never the regex corpus.

## Refreshing without a WB release

An operator (or CI) can refresh detection coverage without waiting for a new
`wb` build by dropping a newer copy of gitleaks' `config/gitleaks.toml` (or a
narrower custom rules file using the same `[[rules]]` schema) at:

- the path named by `$WB_SECRETSCAN_RULES`, or
- `~/.config/wb/secretscan/rules.toml` (XDG config home if set)

That file's rules are added on top of (never replace) the embedded baseline
above, so this same mechanism also covers an internal/private token shape
that will never appear in gitleaks' public ruleset (invariant: extensible via
config, not code).

## Licence

gitleaks is MIT licensed (`LICENSE` in this directory, copyright Zachary
Rice). WB vendors only the data file (the ruleset), not gitleaks' Go source,
and reproduces its licence and attribution here as required by the MIT
licence's copyright-notice condition.
