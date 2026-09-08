# Per-repository sync analysis records

## Outcome

An agent can turn WB's transient sync findings into one durable, queryable
Markdown record per repository. WB validates the records as an InGitDB
collection, commits them to the user's workbench repository, pushes the commit,
and returns a stable web URL containing the workbench repository, immutable
commit SHA, and report ID.

## Contract

- A publication batch contains exactly one report ID and at most one record per
  `owner/repository`.
- Queryable YAML frontmatter carries identity, finding, severity, state,
  observation time, optional observed HEAD, title, and suggested action.
- The Markdown body carries repository-specific evidence and analysis.
- WB performs strict local parsing and an InGitDB validation both before and
  after installing the records in the target checkout.
- WB refuses symlink/special-file input, schema conflicts, and a dirty,
  stashed, or already-unpushed target checkout.
- Publication creates or non-destructively extends the InGitDB root collection
  registry, owns the `sync-reports` collection definition, and writes the
  deterministic record files in its batch.
- A failed pre-commit validation restores prior file contents. A twice-rejected
  push keeps the local commit and reports that state for explicit recovery.

## Presentation identity

The canonical presentation URL is:

```text
https://sneat.work/bench/app/sync-report?repo=<owner/repository>&ref=<commit-sha>&report=<report-id>
```

The URL is a data contract for WB web/CLI views. It does not couple the stored
records to one presentation layout.
