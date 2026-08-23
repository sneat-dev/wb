# Handle repositories sync cannot pull

`wb sync` fast-forwards every owned, non-fork repository it finds. A clone
whose branch was never pushed has nothing to fast-forward, so `git pull` fails
on it every run:

```
Your configuration specifies to merge with the ref 'refs/heads/main'
from the remote, but no such ref was fetched.
```

That happens when the GitHub repository exists but is still empty, or when the
branch has simply never been published. Pick one of the two commands below;
both are one-shot and per-repository.

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

When there is nothing to sync yet and that is fine — an empty placeholder
repository, or one deliberately kept local:

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

Note this is distinct from the `local-only` count in `wb fleet --remote`, which
means "found on disk, absent from GitHub" — something WB detects, not something
you declare. Ignored repositories are counted separately as `ignored`.
