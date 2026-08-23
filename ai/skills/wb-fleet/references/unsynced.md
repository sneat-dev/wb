# Handle repositories sync cannot pull

`wb sync` fast-forwards every owned, non-fork repository it finds. A clone
whose branch was never pushed has nothing to fast-forward, so `git pull` fails
on it:

```
Your configuration specifies to merge with the ref 'refs/heads/main'
from the remote, but no such ref was fetched.
```

## Nothing to do: the remote is empty

Handled automatically. When a pull fails, WB checks whether origin publishes
any branch at all; if it publishes none, the repository was created on the
host and never pushed to, there is genuinely nothing to pull, and sync reports
it as `Empty remote` rather than an error. The moment someone pushes a first
commit, the next sync pulls it normally — no marker to set and none to clear.

Do not mark such a repository as ignored. That would keep it out of sync even
after it gains content.

The two commands below are for the cases automation cannot decide.

## Publish the branch

When the repository is meant to be synced and just needs its first push:

```sh
wb repo init-remote ~/projects/<owner>/<repository>
wb repo init-remote .
```

It gives the branch an empty initial commit if it has none, pushes to origin,
and sets the upstream. Afterwards `wb sync` pulls the repository normally.

The command validates before it mutates. It refuses a detached HEAD, a missing
origin, and a repository already marked as ignored, leaving the checkout
untouched in each case rather than creating a commit for a push that cannot
run. If origin already holds unrelated history the push fails and git's own
error is reported; resolve that by hand.

## Leave the repository alone

For a repository that must stay out of sync as a standing decision — one kept
deliberately local, or whose remote must not be touched. This is a policy
choice, not a description of the remote's current state; an empty remote needs
no marker because the case above already covers it.

```sh
wb repo ignore ~/projects/<owner>/<repository>
```

`wb sync` then skips it entirely — no clone, pull, or push — whatever its
working-tree state, and reports it under `Skipped (ignored)`. The marker also
protects the clone from archived-repository cleanup: WB never deletes a
checkout it was told to leave alone.

Reverse it with:

```sh
wb repo ignore --unset .
```

The marker is `wb.skip-sync` in the repository's own git config, so it travels
with the checkout and never leaks to other repositories.

Three nearby counts in `wb fleet --remote` mean different things:

| Count | Meaning | Who decided |
|---|---|---|
| `ignored` | Marked with `wb repo ignore` | You |
| `empty-remote` | Origin publishes no branches yet | Detected |
| `local-only` | Found on disk, absent from the host entirely | Detected |
