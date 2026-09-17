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
checkout. The config and XDG state roots must resolve outside the checkout and
use trusted non-symlink storage. The executable must be owned by the current
user or root, must not be group/world-writable, and is revalidated immediately
before execution. Windows accepts only direct `.exe` or `.com` executables and
enforces owner and ACL trust. WB also revalidates the checkout's physical
identity, canonical repository, and exact queued HEAD.
`mode: coalesced` durably collapses pending executor-plus-checkout events across
WB processes to the latest commit. Repository operations enqueue and return;
one background worker runs at most two executors concurrently and recovers
interrupted claims. Delivery is at least once, so executors must be idempotent
for the repository and target commit. Corrupt queue entries are quarantined
without blocking valid work. `failure: warn` preserves the successful Git
update, records the failed hook in the private local receipt stream, and warns
on the next lifecycle dispatch. Standard output
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
wb hooks lifecycle resume
wb hooks lifecycle retry <receipt-id>
wb hooks lifecycle gc
wb hooks lifecycle gc --apply
wb hooks lifecycle backfill
wb hooks lifecycle backfill --apply
```

Backfill is dry-run by default, never changes Git, and uses current canonical
HEAD values plus the configured binding and executor arguments.
Status includes durable worker health, unseen failures, quarantined-state
findings, and malformed-receipt findings. Resume starts a worker only when work
is pending or interrupted. GC is also dry-run by default; it protects unseen
failures and retains the newest 1,000 receipts plus everything newer than 30
days unless its flags are changed.
