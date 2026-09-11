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
  `hub.store.path` (default `~/.wb/hub`). inGitDB is schema-first, so
  `hubstore.Open` declares every collection in `hub.Collections()` up front.
  `dalgo2ingitdb`'s query path returns `map[string]any` for every row rather
  than the record factory a DALgo query carries, so
  `githubapp/dalgostore.Query` decodes generic map rows;
  `TestInGitDBEngineRunsTheHubJourneys` in `internal/hubstore` walks the whole
  journey on it.

### Webhook mode

Polling is the default and needs nothing but a token. Register a GitHub App
only when you want push latency instead of an interval. Add:

```yaml
hub:
  github:
    token_file: /Users/you/.config/wb/credentials/github.token
    app:
      app_id: 1234567
      private_key_file: /Users/you/.config/wb/credentials/wb-app.private-key.pem
      webhook_secret_file: /Users/you/.config/wb/credentials/wb-app.webhook-secret
      public_url: https://bench.example.com
```

All four fields are required together, both files are read at start, and the
webhook secret must be at least 32 bytes — GitHub deliveries are verified
against it with HMAC-SHA256 over the raw body, exactly as the hosted instance
verifies them. The daemon's start line then ends with
`webhook=on public_url=…`, and `wb daemon status` reports `hub_webhook=true`.
Set the App's webhook URL to `<public_url>/v0/workbench/github/webhook`.

Repositories an installation of your App covers are taken off the poller, so
each push arrives once. Coverage is read from the installation bindings this
hub holds; while it holds none, polling continues alongside the webhook and
the two deduplicate through the event store, which is also what happens when
you stop the tunnel. Nothing is lost either way.

The connect, setup and OAuth-callback routes under
`/v0/workbench/github/installations/` answer `503
installation_connection_unavailable` on a self-hosted hub: completing them
needs an OAuth client secret and a browser redirect GitHub can reach, which a
loopback listener has no way to receive. Install the App from GitHub's own UI
instead.

#### Tunnels

wb never starts a tunnel. GitHub has to reach `public_url`, so run one
yourself with your own credentials, in its own terminal, forwarding to the
daemon's loopback port (default `8766`):

```sh
# cloudflared, named tunnel (one-time: cloudflared tunnel login)
cloudflared tunnel create wb-bench
cloudflared tunnel route dns wb-bench bench.example.com
cloudflared tunnel run --url http://127.0.0.1:8766 wb-bench
# public_url: https://bench.example.com
```

```sh
# cloudflared, quick tunnel — prints a fresh https://<random>.trycloudflare.com
# on every start, so public_url and the App's webhook URL change with it
cloudflared tunnel --url http://127.0.0.1:8766
```

```sh
# ngrok (one-time: ngrok config add-authtoken <your token>)
ngrok http 8766 --url=bench.example.com   # reserved domain, stable URL
ngrok http 8766                           # ephemeral URL, changes per start
```

Only `/v0/workbench/github/webhook` needs to be public. Both tools forward the
whole listener, so treat the tunnel URL as a secret unless you put your own
access control in front of it: everything else on that port is the
unauthenticated loopback dashboard.

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

Released binaries always carry the real pages: `.goreleaser.yml`'s `before`
hooks run `pnpm install --frozen-lockfile && pnpm build` in `hub/web` and then
assert that `hub/web/dist/dashboard/index.html` exists, so a release fails
rather than shipping the "not built" page.
