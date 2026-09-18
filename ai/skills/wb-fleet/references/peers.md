# Connect a peer

`wb peers` admits an always-on hub's downstream laptops (or, on the laptop,
shows its upstream hub) and lets the operator see and control every
connection. See
[spec/features/peer-connectivity](../../../../spec/features/peer-connectivity/README.md).

On the hub:

```sh
wb peers invite laptop --token-file /tmp/laptop.token
```

prints the token once, or writes it privately when `--token-file` is given.
An existing peer name is refused unless `--rotate` is also given.

On the laptop, pipe the token in (never as an argument):

```sh
cat /tmp/laptop.token | wb peers join https://vm1.sneat.dev --token-stdin
# equivalently, reading the token from stdin some other way:
wb peers join https://vm1.sneat.dev --token-stdin
```

`wb peers join` verifies the token, writes `peers.upstream` in `wb.yaml`
without touching `remote:`, and restarts a running daemon. It refuses a hub
already reachable through `remote.provider: hub` at the same origin.

| Need | Command |
|---|---|
| See every peer, or the upstream hub | `wb peers list` |
| One peer's full record | `wb peers get laptop` |
| Refuse a peer's sessions, keeping its history | `wb peers block laptop` |
| Let a blocked peer back in | `wb peers unblock laptop` |
| Close a peer's live session (it reconnects itself) | `wb peers disconnect laptop` |

Every command supports `--format json`. `<peer>` accepts a name or peer ID.
Invite, join, block, unblock and disconnect all go through the daemon's
owner-token unix-socket RPC — never over the network — so admission and trust
changes require the local operator, exactly like the daemon lifecycle
commands.
