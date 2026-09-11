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
