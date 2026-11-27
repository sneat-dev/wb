# Connect this machine to Sneat Dev Desk

[Sneat Dev Desk](https://desk.sneat.dev) is the hosted overview of your machines,
repositories across forges, and teams. The daemon publishes this machine's state
over outbound HTTPS, reduced on the machine to its detail level.

| Need | Command |
|---|---|
| Connect this machine (approve the code in the browser) | `wb desk connect` |
| Connect and preselect a team space on the approval page | `wb desk connect --space <space-id>` |
| Send full detail (task and branch names) instead of the default | `wb desk connect --detail full` |
| Connect a second machine with the same task key | `wb desk task-key export \| ssh vm1 wb desk connect --task-key-stdin` |
| Check the connection, shares, last publish and heartbeat | `wb desk status` |
| Revoke this machine's credential and remove it | `wb desk disconnect` |

To give a second machine the same task key, pipe it over SSH:

```sh
wb desk task-key export | ssh vm1 wb desk connect --task-key-stdin
```

Detail levels, chosen on the machine:

- `repositories` (default): repositories, counts, risk codes and pull request
  states. No task, branch or worktree name leaves the machine; task names travel
  as keyed hashes under a task key (`~/.config/wb/credentials/desk-task-key`)
  that Desk never holds.
- `full`: everything Cockpit shows an anonymous reader, task and branch names
  included.

Neither level ever sends paths, file content, commit messages or error text.

`wb desk status` exits 1 when the machine is not connected or Desk reports an
error such as `revoked`; the fix is `wb desk connect`. `wb desk disconnect`
exits 1 when Desk could not be reached: the local credential is removed anyway,
so remove the machine in Desk too. `wb desk task-key export` refuses to print to
a terminal.
