---
name: wb-daemon
description: Serve and inspect WB's local operations API and web dashboard. Use when reviewing worktree activity and governed command cost, or when publishing a loopback WB service through an authenticated Cloudflare Tunnel.
---

# WB daemon

Serve the read-only API and embedded dashboard:

```sh
wb daemon serve
```

The default URL is `http://127.0.0.1:8766`. Keep the daemon on loopback. To
reach it from another registered machine, route that local endpoint through a
Cloudflare Tunnel protected by Cloudflare Access service authentication.

The MVP has no remote mutation endpoint. Use the dashboard and `/api/v1/*`
read models for machine, worktree, and governed-command visibility; continue to
perform mutations through local WB commands.
