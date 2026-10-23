# Connect a peer

`wb peers` admits an always-on hub's downstream laptops (or, on the laptop,
shows its upstream hub) and lets the operator see and control every
connection. See
[spec/features/peer-connectivity](../../../../spec/features/peer-connectivity/README.md).

On the hub:

```sh
wb peers invite laptop --token-file ~/.wb/credentials/laptop.token
```

prints the token once, or writes it privately (mode 0600, refusing to
overwrite an existing file) when `--token-file` is given. An existing peer
name is refused unless `--rotate` is also given. Invite requires a hub
section on this machine (`hub:` in `wb.yaml`) and calls the daemon's
owner-token unix-socket RPC — never over the network — so minting a
credential requires the local operator.

On the laptop, pipe the token in (never as an argument):

```sh
cat laptop.token | wb peers join https://vm1.sneat.dev --token-stdin
```

`wb peers join` verifies the token itself against the hub (not the
owner-token RPC — the peer's own future credential proves it), writes
`peers.upstream` in `wb.yaml` without touching `remote:`, stores the token as
a private credential file, and restarts a running daemon so the peer session
starts immediately. It refuses a hub already reachable through
`remote.provider: hub` at the same origin, since two receivers would consume
one queue.

| Need | Command | Who it talks to |
|---|---|---|
| See every peer, or the upstream hub | `wb peers list` | local daemon read API |
| One peer's full record | `wb peers get laptop` | local daemon read API |
| Refuse a peer's sessions, keeping its history | `wb peers block laptop` | hub's owner RPC |
| Let a blocked peer back in | `wb peers unblock laptop` | hub's owner RPC |
| Close a peer's live session (it reconnects itself) | `wb peers disconnect laptop` | hub's owner RPC |

Every command supports `--format json`. `<peer>` accepts a name or peer ID.
`invite` and `disconnect` always require a hub on this machine. `block` and
`unblock` are different on a laptop: naming the configured upstream (its
display name, "upstream", or its URL) flips a local state file instead —
stopping or resuming dialing — with no daemon or hub involved at all; naming
anything else still requires a hub and goes through the owner RPC as usual.
