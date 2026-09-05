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

The default URL is `http://127.0.0.1:8766`. Keep the daemon on loopback. To
reach it from another registered machine, route that local endpoint through a
Cloudflare Tunnel protected by Cloudflare Access service authentication.

`wb daemon start` is idempotent. If the managed listener belongs to an older
installed WB executable, it drains the old generation and hands the durable
queue owner record to the installed executable. `stop` and `restart` preserve
that handoff record; `restart --if-running` is safe for the verified
self-update path because it never starts a daemon that was absent.

The current local lifecycle transport is loopback HTTP. The future ConnectRPC/
gRPC and MCP adapters consume the same typed queue lifecycle contract; they do
not read the lifecycle state file. The lifecycle package has a Windows process
boundary, but WB's existing Unix-only packages currently prevent a Windows
binary; port those dependencies before enabling a per-user Service Control
Manager supervisor.

The MVP has no remote mutation endpoint. Use the dashboard and `/api/v1/*`
read models for machine, worktree, and governed-command visibility; continue to
perform mutations through local WB commands.
