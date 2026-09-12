# Lifecycle hooks

WB reads automatic checkout-update executors only from the trusted user
`~/.config/wb/wb.yaml`; never add one to repository content. A binding matches
the canonical `host/owner/repository` identity and runs only after the checked
out commit changes. New clones count; already-current pulls and dry runs do not.

```yaml
hooks:
  version: 1
  executors:
    code-index:
      run: /opt/homebrew/bin/codegrapher
      args: [sync, --init, .]
      cwd: repository
      mode: coalesced
      timeout: 2m
      failure: warn
  bindings:
    - on: [checkout-updated]
      match:
        repositories:
          include: ['*/*/*']
      execute: [code-index]
```

WB invokes the absolute executable directly, with no shell, from the updated
checkout. The executable must be owned by the current user or root, must not be
group/world-writable, and is revalidated immediately before execution.
`mode: coalesced` durably collapses pending executor-plus-checkout events across
WB processes to the latest commit. Repository operations enqueue and return;
one background worker runs at most two executors concurrently and recovers
interrupted claims. `failure: warn` preserves the successful Git update and
records the failed hook in the private local receipt stream. Standard output
and standard error are private per-attempt files capped at 64 KiB each. The
example's resulting argv is
`codegrapher sync --init .`: CodeGrapher initializes a missing index on the
first update, then uses its incremental reconciler.

Run the ordinary fleet update; only changed checkouts dispatch:

```sh
wb sync
```

Validate, inspect, retry, and explicitly initialize existing matching
repositories with the generic lifecycle commands:

```sh
wb hooks lifecycle check
wb hooks lifecycle status
wb hooks lifecycle retry <receipt-id>
wb hooks lifecycle backfill
wb hooks lifecycle backfill --apply
```

Backfill is dry-run by default, never changes Git, and uses current canonical
HEAD values plus the configured binding and executor arguments.
