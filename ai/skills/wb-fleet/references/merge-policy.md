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

An existing organization pull-request ruleset is updated only after WB lists
all repositories it affects, and its unrelated conditions, bypass actors,
review requirements, checks, enforcement, and rules are preserved. Repository
rulesets use the same preservation rule. Enterprise rulesets are reported and
remain unchanged until GitHub can provide an exact affected-repository scope.
