---
name: wb-daemon
description: Serve and inspect WB's local operations API and web dashboard. Use when reviewing worktree activity and governed command cost, or when publishing a loopback WB service through an authenticated Cloudflare Tunnel.
---

# WB daemon

Start and inspect the local read-only API and embedded dashboard:

```sh
wb daemon start
wb daemon status --format json
wb daemon stop
wb daemon restart --if-running
```

For foreground debugging, run `wb daemon serve`.

Submit long-running local work without blocking the caller, then inspect or
control it through the same authenticated local queue:

```sh
wb daemon operation submit --format json -- go test ./internal/worktrees
wb daemon operation get <operation-id> --format json
wb daemon operation wait <operation-id> --timeout 15m
wb daemon operation cancel <operation-id>
```

`wb run --async -- <command>` is the short submission form. It returns a JSON
receipt immediately; use the operation ID with the commands above.

The default URL is `http://127.0.0.1:8766`. Keep the daemon on loopback. To
reach it from another registered machine, route that local endpoint through a
Cloudflare Tunnel protected by Cloudflare Access service authentication.

`wb daemon start` is idempotent. If the managed listener belongs to an older
installed WB executable, it drains the old generation and hands the durable
queue owner record to the installed executable. `stop` and `restart` preserve
that handoff record; `restart --if-running` is safe for the verified
self-update path because it never starts a daemon that was absent.

The dashboard stays on read-only loopback HTTP. Mutating operation RPCs use a
separate mode-0600 Unix socket plus the private lifecycle owner token. Windows
builds expose the equivalent named-pipe endpoint contract and refuse rather
than falling back to TCP until the native adapter is enabled.

There is no remote mutation endpoint. The queue accepts only local raw command
submissions, persists bounded output and command digests, and rejects arbitrary
environment overrides. Use the dashboard and `/api/v1/*` read models for
machine, worktree, and governed-command visibility.
