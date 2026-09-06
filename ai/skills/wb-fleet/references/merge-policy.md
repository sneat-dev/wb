# Audit and apply GitHub merge policy

Audit is read-only and selects the authenticated user's fleet plus repeated
`--org` owners. Narrow it with `--filter` before a fleet-wide apply.

```sh
wb fleet merge-policy --filter sneat-dev/wb
wb fleet merge-policy --org sneat-dev --format=json
```

The desired policy enables merge commits only and uses the pull request title
and body for the merge commit. The report includes repository setting drift and
effective default-branch rules. Repository-owned required linear history is
drift that apply removes through its dedicated classic endpoint or by deleting
only that repository-ruleset rule. Merge queues and higher-level linear-history
rules remain preserved blockers.

Apply only after reviewing the complete scope. WB writes its plan before the
first mutation and re-observes repository settings before changing them:

```sh
wb fleet merge-policy --org sneat-dev --report-dir reports/merge-policy --apply
wb fleet merge-policy --org sneat-dev --report-dir reports/merge-policy --apply --resume
```

By default the GitHub read and repository-apply pools use WB's CPU budget
(logical CPU count minus one, with a minimum of one). Set `--parallel`
explicitly to override that bound. Shared rulesets remain serialized, and the
report is checkpointed after every mutation for truthful resume.

Existing repository pull-request rulesets preserve their unrelated conditions,
bypass actors, review requirements, checks, enforcement, and rules.
Organization and enterprise rulesets are reported as higher-level authorities
and remain audit-only in this slice. Their conflicts block repository fallback.
Classic branch protection is checked separately; apply leases its full snapshot
and deletes only the dedicated required-linear-history setting. Any protection
drift after planning is refused.
