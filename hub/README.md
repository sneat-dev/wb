# Workbench GitHub App

The `github.com/sneat-dev/wb/hub` package is the server side of bench: the
Workbench GitHub App. It owns browser and daemon enrollment, GitHub user
OAuth verification, installation entitlements, signed webhook translation,
and durable repository-event delivery.

The `api/githubapp` package in this same module owns only the daemon-facing
wire models and client behavior. A host supplies Firebase identity and
secret configuration adapters and mounts this package's handler on a
`githubapp.DocumentStore` — in practice `githubapp/dalgostore.New(db)` over
any DALgo engine.

See [docs/architecture.md](docs/architecture.md) for the trust boundaries,
protocols, authentication, wire format, delivery guarantees, and daemon
communication diagrams.

## Self-hosting

`wb daemon serve` is the self-hosting unit. Add a `hub:` section to
`~/.config/wb/wb.yaml` and restart the daemon; without the section the daemon
serves exactly what it served before.

```yaml
hub:
  store:
    engine: memory                       # memory | ingitdb | openvaultdb
  github:
    token_file: /Users/you/.config/wb/credentials/github.token
```

What appears where, on the daemon's loopback listener (default
`127.0.0.1:8766`):

| Path | Served by |
|---|---|
| `/` and `/api/v1/…` | the existing read-only WB dashboard and API |
| `/v0/workbench/…` | this package's hub API (`hub.NewHandler`) |
| `/bench/dashboard/` | the embedded bench dashboard from `hub/web/dist` |

Starting the daemon prints one line to stderr naming the engine, the store
location and the dashboard URL. `wb daemon status` repeats it as
`hub_mounted`, `hub_engine`, `hub_store` and `hub_listen`, in text and JSON.

There is no sign-in: only this machine can reach the listener, so the viewer
resolver returns one fixed identity. `wb daemon serve` refuses a non-loopback
`--listen`, which is what makes that safe. On first start the daemon enrols
itself against its own hub, writes the machine credential to
`~/.config/wb/credentials/hub-local-<machine>.token` with mode 0600, and
points `remote:` at `http://<listen>`; `remote.url` accepts plain `http://`
only for loopback hosts. Restarting is a no-op because the credential on disk
is resolved through the same bearer path a request takes.

### Store engines

- `memory` — dalgo2memory on the strict Firestore profile. Nothing survives a
  restart; it is for trying bench out and for tests, and `wb daemon status`
  says so.
- `openvaultdb` — dalgo2openvaultdb against a server you already run. Set
  `hub.store.url`, and `hub.store.database_id` if it is not `wb`.
- `ingitdb` — dalgo2ingitdb over a directory of inspectable files, created at
  `hub.store.path` (default `~/.wb/hub`). **Not usable yet.** inGitDB is
  schema-first, so `hubstore.Open` declares every collection in
  `hub.Collections()` up front and writes then succeed — but
  `dalgo2ingitdb` v0.4.0's query path ignores the record factory a DALgo query
  carries and returns `map[string]any` for every row, so any read-back through
  `githubapp.DocumentStore.Query` fails. That is one upstream fix away;
  `TestInGitDBEngineCannotServeQueriesYet` in `internal/hubstore` pins the
  exact behaviour and will fail, loudly, the day it is fixed.

### Building the dashboard

`hub/web/dist` is embedded at build time. A clean clone carries only
`dist/.gitkeep`, and `/bench/` then serves a one-line page saying so. To get
the real pages:

```sh
cd hub/web && pnpm install && pnpm build   # then rebuild wb
```

The build output stays git-ignored. `hub/web/public/.gitkeep` is copied back
into `dist/` by every build, so building never deletes the tracked
placeholder `go:embed` needs.
