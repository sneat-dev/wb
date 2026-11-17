# Operator setup

This reference is for authorized setup and diagnosis. Agents use local
`send`, `receive` and `ack`; they do not need transport credentials or network
permission for that workflow. Configure the transport independently.

## Direction and transport

On the machine holding an agent's locally enqueued messages, forward that
sender/recipient pair to a reachable mailbox host:

```sh
wb message forward --host <mailbox-host> --from <self> --to <peer>
```

On a receiving machine that connects outbound to the mailbox host, copy its
recipient inbox locally:

```sh
wb message relay --host <mailbox-host> --agent <self>
```

Continuous forwarding listens for local delivery before scanning, then waits
on that inbox socket after each successful pass; failed passes retry after two
seconds. `--once` scans the complete directory in bounded pages, not only the
first 100 pending messages. The full socket path must fit 103 bytes, and the
continuous forwarder cannot share its inbox with another waiting receiver.
These local wake mechanics do not guarantee instant cross-machine delivery.

Assign stable, distinct inbox names and the same intended projects root for
the agent, transport and wake binding. Supervise transport processes outside
the agent's turn; prepare the reverse direction separately for replies.
These commands do not create a system service or grant network permission.

The transport uses authenticated SSH with strict host-key verification. The
operator supplies existing authorized host configuration and a compatible
remote WB executable. See each command's `--help` for its exact connection
flags. Do not bypass host verification or copy credentials into messages.

Transport acknowledgement confirms a durable copy at the next location;
the receiving agent's local ACK separately records processing. Keep those
receipts distinct. Retries preserve the original ID and complete envelope.

## Optional native Codex wake

The receiving operator chooses an existing local Codex thread and socket:

```sh
wb message bind --agent <self> --codex-socket <absolute-socket> --thread <thread-id>
wb message unbind --agent <self>
```

Binding verifies the local target and opts it into wake-up; it does not start
Codex or grant the sender authority to choose a thread. Unbinding preserves
the inbox. Other harnesses can use the same CLI inbox without assuming a
native wake adapter exists for them.

Keep wake text to `New message from <from>.` and the recipient's exact
`wb message receive --agent <self>` command. A static skill cannot infer the
bound recipient. The skill supplies ACK, retry and authorization guidance;
do not repeat transport instructions or message bodies in every wake.

## Verify the matching skill bundle

Skills are embedded in the WB binary at build time. Adding source files does
not update an already installed CLI. Before installing a new build globally,
test that candidate binary's bundle in an isolated destination:

```sh
<candidate-wb> skills sync --dir <isolated-skills-directory> --format json
<candidate-wb> skills sync --dir <isolated-skills-directory> --format json
```

Verify `wb-message/SKILL.md`, its reference and UI metadata match the candidate
source, and that the second sync reports unchanged skills. Do not use an old
installed binary to prove the new bundle or sync it over active harness skills.
Normal authorized installation uses the matching build's `wb skills sync`;
see `$wb-skills`. No transport or wake is activated by this isolated proof.
