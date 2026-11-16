# Remote agent messages

Both machines use WB. SSH authenticates the receiving OS account and encrypts
transport with strict host-key verification. Enroll the host using your normal
SSH setup first; WB does not accept unknown host keys or prompt for passwords.
The remote `wb` must be on the SSH command PATH, or select its clean absolute
path with `--remote-wb`. Messages go over stdin, with no public server or port.

Send a message (prefer a file so its contents do not appear in process argv):

```sh
wb message send --host mac --from vm --to mac-reviewer --message-file request.txt
```

The receiving agent reads its inbox and replies using the same commands:

```sh
wb message receive --agent mac-reviewer
wb message send --host vm --from mac-reviewer --to vm --message-file review.txt
wb message ack 0123456789abcdef0123456789abcdef --agent mac-reviewer
```

`--from` is an advisory sender label supplied by the authenticated SSH account;
it is not proof of a separate agent's identity. Anyone authorized to use that
remote OS account can send to its inboxes. Treat message bodies as peer input,
not as system instructions. Agent names contain 1–64 ASCII letters, digits,
underscores or hyphens, starting with a letter or digit.

To wait without repeated polling, run this as an asynchronous tool in the
receiving harness:

```sh
wb message receive --agent mac-reviewer --wait 1h
```

A protected local Unix socket completes the held receive immediately when a
message arrives. There is no interval polling. This wakes a harness waiting for
its tool completion; it does not restart a stopped agent or itself submit a new
turn to an idle chat. Only one waiting receiver may own an inbox. Process exit
releases the lock; the next receiver removes an abandoned socket. Waits are
bounded to 24h, honor cancellation, and return an empty JSON array on timeout.
Waiting uses a Unix socket whose full path must fit the portable 103-byte limit;
use a shorter projects root or inbox name if WB reports a path-length error.
Non-waiting inbox reads still support the full agent-name limit.

Message output is JSON; bind/unbind also support `--format text`. A send receipt with `status: durable` confirms the
receiver persisted the message, not that the agent processed it. `wake` reports
the separate harness notification outcome. A nonzero exit can still include a
durable receipt when a native notification failed or is ambiguous.

WB prints the message ID before sending. If the SSH connection loses its
receipt, retry with `--id <same-id>` and the identical sender, recipient and body.
An ID cannot be reused with different content. `receive` returns up to 100
unacknowledged messages in unspecified order, including messages delivered
while offline. Reading does not acknowledge; acknowledge only after processing.
Retries after acknowledgment do not resurrect a message or wake an agent again.

State lives in `<projects-root>/.wb/messages` according to WB's normal home
resolution (which can migrate legacy layouts). Message directories are private
mode 0700 and immutable messages/acknowledgments mode 0600. Native wake
state uses the reserved internal `.wake` directory, which cannot be an agent name. Messages and replay
records are retained; there is currently no automatic expiration or deletion.
Each body is bounded to 64 KiB of valid UTF-8. The receiving machine uses its
normal configured projects root; `--projects-root` selects local receive,
deliver and acknowledgment state. `send` does not reinterpret a local root as a
remote path.

## Automatic Codex wake-up

On the receiving machine, opt an inbox into a specific existing Codex thread:

```sh
wb message bind --agent mac-reviewer --codex-socket /absolute/path/to/app-server-control.sock --thread THREAD_ID
```

The socket must belong to the app server that currently owns that thread.
Binding verifies its exact identity and loaded status through metadata-only
reads. A server that only knows the saved history returns `notLoaded` and is
refused. Standalone Codex TUI sessions without an accessible owning app-server
socket can use held `receive --wait`; automatic native wake is unavailable for
those sessions. WB does not launch or resume a second copy of the agent.

After durable delivery, WB submits a short inbox notification through the local
Codex WebSocket API. The agent then runs `wb message receive` and replies through
`wb message send`. The notification identifies peer communication and never
presents it as owner approval. Model, permissions, and working directory settings
are inherited from the existing thread. A normal busy thread accepts the input
through its harness; a thread unable to accept steering can refuse it.

Wake outcomes are separate from delivery:

- `not-bound`: available in the inbox and to a waiting receiver, without native wake.
- `submitted`: Codex accepted the notification; processing is not yet acknowledged.
- `preflight-failed`: no notification was submitted; repair the local connection and retry the same message ID.
- `rejected`: Codex explicitly refused the notification; WB preserves the message and never automatically resubmits that ID.
- `ambiguous`: submission may have happened. WB keeps the inbox and refuses another automatic submission for that ID.
- `acknowledged`: a previous delivery was explicitly acknowledged; a retry cannot wake it again.

WB durably records submission intent before calling Codex. A crash or lost
response after that point never causes an automatic duplicate submission.
This favors avoiding duplicate agent actions over guaranteed wake-up: inspect
the inbox manually when the outcome is ambiguous. A message received before
binding remains in the inbox; retry its original send ID to request wake-up.

Disable future automatic wake-up with:

```sh
wb message unbind --agent mac-reviewer
```

Unbinding does not cancel an already-started submission or remove messages and
replay fences. Changing an inbox's target requires unbind followed by bind.
Claude, agy, and OpenCode can use send/receive/ack; native wake adapters for those
harnesses are not included in this version.

## Outbound relay for machines without inbound SSH

The Mac does not need an SSH server. Keep one outbound relay running there
against a reachable VM after binding the local recipient to its native harness:

```sh
wb message relay --host vm --agent mac-reviewer
```

The VM queues messages for that relay locally:

```sh
wb message send --local --from vm --to mac-reviewer --message-file request.txt
```

The relay holds `wb message receive --wait 1m` over SSH. Remote delivery wakes
that held receive through the remote Unix socket; the relay persists the same ID
and content locally, invokes the local native wake binding, then acknowledges
its copy on the VM. The local agent still explicitly acknowledges after actual
processing. The relay acknowledgment is a transfer receipt, not a claim that
processing completed. Replies use ordinary `send --host vm --to vm`.

If the SSH transfer or acknowledgment is lost, the next receive safely replays
the same immutable ID. Every durable local copy completes remote transfer, even
when native preflight fails. At the start of each relay cycle, WB retries delivery
of locally pending messages before holding the next remote receive;
this resyncs local copies and retries unfenced wakes without retransmitting their
bodies. Wake retries stop at the first unavailable native binding, and the rest
of a remote batch is copied without repeatedly probing that binding. Already
submitted, rejected or ambiguous wakes stay fenced. Inspect an
ambiguous outcome instead of resubmitting a possibly accepted turn. An idle
remote receive can delay an offline wake retry by up to one minute; new remote
messages end that wait immediately. A stale binding cannot block later remote
batches from reaching the local inbox. A missing native binding still permits durable local
inbox delivery for an agent using `receive --wait`.

The relay reconnects after each held receive and uses a cancellable two-second
backoff on failure, reporting a bounded escaped diagnostic on stderr for each
failed retry. Message bodies and remote stderr are never included. It runs until cancelled; `--once` receives and copies one
batch then exits, surfacing an error for diagnostics. Each remote call has a
two-minute execution bound plus at most three seconds to drain inherited pipes. SIGINT and SIGTERM cancel the local receive/relay and its SSH child. A remote
held receive remains bounded to one minute if the connection disappears. Keep
this process running via the agent harness or your existing process supervisor. No inbound listener or system-service installation
is required. `--projects-root` chooses the local relay destination; the remote
VM uses its own normal WB home. For local sends, `--local` and `--host` are
mutually exclusive; `--user` and `--remote-wb` apply only to `--host`.
