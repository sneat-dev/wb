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
checkout. `mode: coalesced` collapses repeated executor-plus-checkout events in
one operation. `failure: warn` preserves the successful Git update and records
the failed hook in the local receipt stream. The example's resulting argv is
`codegrapher sync --init .`: CodeGrapher initializes a missing index on the
first update, then uses its incremental reconciler.

Run the ordinary fleet update; only changed checkouts dispatch:

```sh
wb sync
```
