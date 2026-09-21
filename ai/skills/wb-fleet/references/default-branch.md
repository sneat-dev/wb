# Audit and safely migrate GitHub default branches

`wb fleet default-branch` is read-only unless `--apply` is explicit. Desired
policy comes from `fleet.default_branch` in the WB config, an organization
override at `fleet.organizations.<owner>.default_branch`, or `--branch`.
Owner discovery refuses a 1,000-item GitHub listing rather than audit or apply
what may be a partial owner scope.

```sh
wb fleet default-branch --all-orgs --format json
wb fleet default-branch --all-orgs --apply --report-dir reports/default-branch
wb fleet default-branch --repo acme/app --apply --reconcile-from reports/default-branch/previous.json --reconcile-sha256 <sha256>
```

Apply writes a durable report before every mutation and verifies the observed
default branch and head afterwards. Forks are eligible only after WB queries
the parent repository for outgoing source-branch pull requests. It refuses
archived repositories, open source-branch pull requests, divergent targets,
Pages/protection/rules impacts, and concrete workflow references to the old
branch. The report is the exception queue; WB never rewrites workflow strings
blindly.

After a remote change, WB refreshes each matching local canonical clone and
renames its old default only when the clone is clean, exact at the remote SHA,
has no linked worktree, and has no destination branch. A second host can resume
that bounded action with `--reconcile-from` an earlier apply report and the
caller-held SHA-256 of its exact bytes via `--reconcile-sha256`. The digest
binds the request to the selected local receipt before WB reads its JSON; it is
not a signature or an authentication system, so the receipt remains trusted
operator input. Dirty,
unpublished, divergent, or conflicting local state stays intact with an exact
report blocker.
