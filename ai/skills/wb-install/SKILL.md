---
name: wb-install
description: Install, update, and verify the WB CLI and its build provenance. Use when wb is missing, a required command or flag is unavailable, or a task requires the exact build produced by a merged GitHub revision.
---

# WB install

Avoid downloads when the installed binary already supports the task:

```sh
command -v wb
wb version --json
wb <required-command> --help
```

Install only when a required surface or provenance is missing.

For normal use, install the latest release:

```sh
go install github.com/sneat-dev/wb/cmd/wb@latest
```

For a just-merged feature, first confirm the revision is on `main`, then pin
the immutable merged SHA:

```sh
go install github.com/sneat-dev/wb/cmd/wb@<merged-sha>
```

Do not install a PR head and describe it as merged. Do not use `@main` when
an exact SHA is known. After installation, verify both behavior and source:

```sh
wb version --json
go version -m "$(command -v wb)"
wb <required-command> --help
```

Reuse the verified binary for the rest of the task. Reinstall only if the
required revision changes.
