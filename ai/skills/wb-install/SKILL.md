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

## Discover commands without guessing

Start with the intent-ranked catalog when the exact command path is unknown.
This works without loading an Agent Skill and avoids recursively opening every
help page:

```sh
wb commands --search "finish work" --format json
wb commands --search=finish --format json
wb commands --format json
```

Open the returned exact path with `--help` before executing a mutating command.
The catalog marks runnable commands even when they also own subcommands.

## Updating an already-installed wb

When a `go install` binary is already present and just needs the latest
published release rather than a pinned SHA, prefer `wb self-update` (alias
`wb update`) over reinstalling by hand:

```sh
wb self-update --check          # report availability only; never modifies
wb self-update                  # confirm, then update; --yes to skip the prompt
wb self-update --version 0.24.0 # install an exact release instead of the latest
```

`wb self-update` first detects how the running binary was installed. A
Homebrew-managed install (`brew install --cask sneat-dev/tap/wb`) is never
overwritten — it prints `brew upgrade --cask wb` instead of touching the
binary. A manual install (release archive or `go install`) is downloaded,
sha256-verified against the release checksums, and swapped in atomically. If
the install method is ambiguous, it refuses rather than guessing — fall back
to the `go install` recipe above in that case. `--check --format json` gives
a machine-readable verdict (`up_to_date`, `update_available`, or
`undetermined`) for scripts that need to branch on it without parsing text.
