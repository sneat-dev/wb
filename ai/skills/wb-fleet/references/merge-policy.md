# Audit and apply GitHub merge policy

Audit is read-only and selects the authenticated user's fleet plus repeated
`--org` owners. Narrow it with `--filter` before a fleet-wide apply.

```sh
wb fleet merge-policy --filter sneat-dev/wb
wb fleet merge-policy --org sneat-dev --format=json
```

The desired policy enables merge commits only and uses the pull request title
and body for the merge commit. The report includes repository setting drift and
effective default-branch rules. Required linear history and merge queue rules
are blockers; WB reports them and does not weaken them.

Apply only after reviewing the complete scope. WB writes its plan before the
first mutation and re-observes repository settings before changing them:

```sh
wb fleet merge-policy --org sneat-dev --report-dir reports/merge-policy --apply
wb fleet merge-policy --org sneat-dev --report-dir reports/merge-policy --apply --resume
```

By default the GitHub read pool uses WB's CPU budget (logical CPU count minus
one, with a minimum of one). Set `--parallel` explicitly to override that bound.

Existing repository pull-request rulesets preserve their unrelated conditions,
bypass actors, review requirements, checks, enforcement, and rules.
Organization and enterprise rulesets are reported as higher-level authorities
and remain audit-only in this slice. Their conflicts block repository fallback.
Classic branch protection is checked separately; required linear history also
blocks merge-only apply, and any protection drift after planning is refused.
