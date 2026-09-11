---
format: https://specscore.md/decision-specification
status: Approved
---

# Decision: Bench is open source inside wb and self-hosts as wb serve

**Status:** Approved
**Date:** 2026-09-11
**Owner:** alex
**Tags:** bench,open-source,self-hosting,github-app,license
**Source Idea:** —
**Supersedes:** —
**Superseded By:** —

## Context

sneat.work/bench is the hosted dashboard and GitHub App behind the wb CLI.
Its backend was split across three places: the public `api/githubapp`
contracts in this repository, the private `sneat-dev/workbench-gh-app`
provider (GitHub App, OAuth, installations, repository events, status), and
a `pkg/modules/workbench` module in the private `sneat-co/sneat-go`
composition host that mounts the provider on Firestore and sneat Firebase
identity. The dashboard pages and the marketing site shared one private
Astro repository, `sneat-dev/workbench-web`.

Known at decision time: wb has been public since 2026-07-18 with no external
users. The hub is optional to every wb workflow; fleet state already has a
serverless git-repository provider. The provider ingests GitHub events only
by inbound webhook. What wb sends the hub is login, machine name, repository
identities, and per-worktree task, branch, lifecycle, and owner. The provider
imports nothing from sneat-co and its history holds no key material. Every
wb commit is the founder's, so relicensing is clean. OpenVaultDB already has a
`firestore` engine through dalgo2firestore. The daemon reacts today only to
repository rename and default-branch change. Cross-repository module pins
between wb, the provider, and sneat-go cost a full day of churn on
2026-09-10.

## Decision

Bench is a free product and a showcase for the sneat.dev platform. Its code
is open source and lives in this repository. `wb serve` is the self-hosting
unit and ships in the wb binary.

Concretely:

1. wb relicenses from MIT to Apache-2.0 before any hub code lands.
2. The provider moves into this repository and `workbench-gh-app` is
   archived. The dashboard pages move here too and the binary embeds them;
   the marketing site stays in a dedicated private repository.
3. The hand-written `FirestoreBackend` port is replaced by DALgo. The hosted
   instance uses dalgo2firestore; a self-hoster picks any DALgo engine,
   including OpenVaultDB through its driver.
4. `wb serve` binds to localhost with no login in its first cut and polls
   GitHub with a plain token by default. Registering it as a GitHub App with
   inbound webhooks is opt-in. Tunnels use the operator's own cloudflared
   or ngrok credentials: ngrok by SDK or spawned binary, cloudflared by
   spawned binary. Exposure beyond localhost requires an OAuth2 or OIDC
   provider; SSO is the likely first paid feature if a paid tier ever exists.
5. On a default-branch push, the MVP fetches and fast-forwards the canonical
   clone only, skips and surfaces a dirty or diverged clone, and never
   touches worktrees. Rebasing worktrees becomes a user option in a later
   story.
6. sneat.work/bench keeps mounting the hub inside sneat-go with sneat
   Firebase identity for now. Cutting the hosted instance over to `wb serve`
   on its own service is allowed later and is not part of this decision.
7. Order of work: relicense; move the provider; split the dashboard out of
   workbench-web; DALgo port; `wb serve` with polling; tunnels; pull on
   push. This is a background stream behind sneat product launch work.

## Rationale

The functionality is small enough that closing it protects nothing: an AI
agent can rebuild it from the public contracts, and the dashboard is static
output every browser already receives. Traction is the scarce resource, and
readable source is what lets a wb user trust the server their CLI reports
to. The repository also becomes a window onto SpecScore, OpenVaultDB,
inGitDB, DataTug, and Synchestra.

One repository removes the pin churn that three repositories produced, and
makes the self-hosting artifact the binary that already ships through
goreleaser and Homebrew. Polling as the default means self-hosting requires
nothing but a token; the App path exists for push latency. DALgo as the port
lets the hosted path stay on Firestore with no server in between while every
other engine is a configuration change.

Apache-2.0 over AGPL because adoption is what is missing and AGPL costs it
with exactly the corporate self-hosters who might one day pay. The choice is
reversible until the first outside contribution; a CLA at that point keeps
the option to tighten.

## Declined Alternatives

### Keep the backend closed inside sneat-go

Lowest effort. It lost because the hub was already two-thirds public, the
remaining third was thin wiring plus two stores that belonged in the
provider, and the private-module plumbing in CI was the most expensive part
of every change.

### Self-hosted hub per team

A separate service a team would deploy with its own identity and store. It
lost because nobody has asked, it would need identity and deploy docs for
other people's clouds, and the daemon already runs on every machine that
needs the events.

### AGPL-3.0

It blocks hosting the hub as a service without opening changes and keeps a
paid SSO tier credible. It lost on adoption cost while the paid tier is
undecided; it can be revisited while all copyright is still the founder's.

### Embed a privately built dashboard in the public binary

Keep the dashboard source private as a moat. It lost because the browser
receives the full output anyway, public CI would need a private-repo token,
and nobody outside could build wb from source.

### Split api/githubapp into its own module

Breaks the import cycle without merging repositories. It lost because it
keeps three repositories and their pins for no benefit once the provider is
public.

## Consequences at Decision Time

Expected positive: one public repository and one release for CLI, hub, and
dashboard; no GOPRIVATE plumbing in any consumer; self-hosting means "run
wb"; the persistence port and its fake disappear in favour of DALgo.

Expected negative: the wb repository grows by roughly 3,600 lines of provider
code and 2,000 lines of Astro, all held to this repository's coverage floor;
the Astro build joins the Go release pipeline; the API contract becomes
something outsiders may depend on; the hosted instance and a self-hosted
`wb serve` have different identity paths until the hosted cutover happens.

## Observed Consequences

None observed yet.

## Affected Features

- [GitHub App Repository Events](../features/github-app-repository-events/README.md)
- [Remote State](../features/remote-state/README.md)
- [Agent SDLC Throughput](../features/agent-sdlc-throughput/README.md)

---
*This document follows the https://specscore.md/decision-specification*
