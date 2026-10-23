# Worktree lifecycle

`internal/worktrees` is WB's compatibility facade and transaction coordinator for managed worktree creation, discovery, claims, landing evidence, cleanup, retirement, and session custody.

See [DOMAIN_MAP.md](DOMAIN_MAP.md) for the reviewed domain boundaries, dependency order, and staged extraction plan. The complete per-symbol inventory is [domain_map.tsv](domain_map.tsv); its move gates identify symbols that still need a facade or injected port before they can change packages.

See [COVERAGE_REPORT.md](COVERAGE_REPORT.md) for the verified statement coverage result, refactoring summary, validation scope, and proposed test-performance work.
