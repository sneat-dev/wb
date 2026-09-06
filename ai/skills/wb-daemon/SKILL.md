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

Start a normal execution worker inside the caller or harness sandbox. Give it a
stable ID and only the canonical roots it may execute within:

```sh
wb worker connect --id codex-local --root /absolute/projects/root
```

`wb run --async --worker codex-local -- <command>` submits long-running work
without blocking the caller and binds it to that stable worker ID. The daemon
never chooses another worker even when roots and capacity match. It journals
argv, so secrets belong in the worker's inherited environment and never in
argv. The worker independently checks the cwd, executes with its inherited
sandbox and environment, and returns a bounded receipt. A reconnect creates a
new generation of the same stable identity. Queued work survives daemon
restart; an interrupted running lease becomes `recovery_required`.

WB tries the protected local socket first. If the harness sandbox returns a
permission or reachability error, the client reports that it is using the
project-root file bridge. Do not move the worker outside the sandbox. The bridge
uses owner-only atomic request and response envelopes under
`<projects-root>/.wb/runtime/file-bridge`; every envelope is authenticated and
fenced to the scheduler generation and explicit worker ID. It carries no
environment overrides. Authentication, protocol, or generation failures never
trigger fallback or local execution.

Raw command submission from the daemon process is a trusted fallback and is
disabled by default. An administrator may opt in by creating
`~/.config/wb/daemon-raw-exec.json` outside the projects root with mode `0600`
and exactly this policy:

```json
{"version":1,"allow_raw_daemon_execution":true}
```

Agents and WB commands must never create or enable this policy. The daemon
revalidates it for every submission and immediately before launching queued
work, so deleting or invalidating it revokes permission without a restart.

```sh
wb daemon operation submit --format json -- go test ./internal/worktrees
wb daemon operation get <operation-id> --format json
wb daemon operation wait <operation-id> --timeout 15m
wb daemon operation cancel <operation-id>
```

The raw policy applies only to `wb daemon operation submit`. Normal `wb run
--async` jobs do not need it and never persist the worker's environment.

The default URL is `http://127.0.0.1:8766`. Keep the daemon on loopback. To
reach it from another registered machine, route that local endpoint through a
Cloudflare Tunnel protected by Cloudflare Access service authentication.

`wb daemon start` is idempotent. If the managed listener belongs to an older
installed WB executable, it drains the old generation and hands the durable
queue owner record to the installed executable. `stop` and `restart` preserve
that handoff record; `restart --if-running` is safe for the verified
self-update path because it never starts a daemon that was absent.

The dashboard stays on read-only loopback HTTP. Mutating operation RPCs prefer
a separate mode-0600 Unix socket plus the private lifecycle owner token.
Sandbox-denied socket calls use the authenticated owner-only project-root file
bridge without moving execution outside the sandbox. Windows builds expose the
equivalent named-pipe endpoint contract and fail closed until either its native
adapter or owner-only bridge ACL verification is available; WB never falls back
to TCP.

The bridge uses a separate owner-only integrity key that survives daemon owner
token and generation rotation. A lost Submit response is replayed only with the
same envelope-bound idempotency key. Ambiguous worker mutation RPCs require
reconnect and are never replayed; bounded stale envelope cleanup preserves a
seven-day Submit recovery window.

There is no remote mutation endpoint. Normal jobs are leased only to compatible
registered workers over the protected local Connect RPC transport. The queue
persists bounded output and command digests and rejects environment overrides.
Reusing an idempotency key
with different cwd, argv, environment, or CPU units is rejected. Use the
dashboard and `/api/v1/*` read models for machine, worktree, and
governed-command visibility.
