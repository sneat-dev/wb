---
captured_by: user
status: queued
---
# wb should enforce required files per repo type: the project's wb config declares the repo type, and the type defines the required shape (README.md, lockfile, CI workflow, ...)

Today three extension repos (`ext-remindius`, `ext-renewon`, `ext-assetus`) turned out to commit **no `pnpm-lock.yaml`** while every sibling extension repo does. Two of them went from a three-week green streak to red overnight with no code change, because a floating `ng-packagr: ~21.2.0` re-resolved to a build-breaking transitive combination. Nothing detected the missing lockfile; it surfaced only from a hand-rolled sweep.

The same shape recurred all day in other guises: a repo with no `pull_request` CI workflow at all, a publish workflow naming Nx projects deleted months earlier, and a repo whose only source file imported a package that never existed.

Idea: `wb` already knows the fleet. Let it also know what a repo of a given KIND is supposed to contain.

- The project's `wb` config declares a repo **type** (e.g. `nx-extension`, `go-cli`, `astro-landing`, `scaffold-template`).
- The type declares the required shape: files that must exist (`README.md`, lockfile, `AGENTS.md`), files that must NOT (a committed `.env`), and possibly per-type assertions (a `pull_request`-triggered workflow exists; the publish workflow's project names resolve).
- `wb` reports drift across the fleet, the way `wb status` already reports git drift.

Deliberately about SHAPE, not content — "does this repo have the parts its kind requires", not "is the code good". The value is that it answers the question for every repo at once, instead of one repo at a time after something breaks.

Open: where the type lives (per-repo config vs central registry), whether it warns or fails, and how it relates to the existing `wb ci` policy checks.
