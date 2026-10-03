# CLI command packages

This directory is the target home for WB's shared CLI contracts, root
composition, and isolated command families. The first extraction is `cmdlayout`;
the remaining executable is being migrated incrementally under the
[command-family plan](../../spec/plans/cli-command-families/README.md).

Command packages own argument parsing, option translation, result rendering and
CLI error classification. Existing internal packages own operations. Keep the
dependency direction from root composition to command family to operation;
families must not import root composition or call sibling commands to perform
work.

Shared contracts belong in `internal/cli/shared`, a leaf package that imports
neither command families nor root composition. `internal/cli` can compose
families without creating a cycle through their shared invocation/error types.

Each command factory owns fresh state. Read inherited flag values at execution
time so defaults captured during construction do not discard user overrides.
Use narrow functions or small consumer-owned interfaces for effectful operations.
Production wiring belongs in the composition layer; deterministic command tests
inject those same dependencies.

Test argument-to-option translation, invalid input, delegated failures, findings,
output/report errors and invocation isolation in each command package. Use
parallel tests and a family-local execution helper where possible. Real Git,
filesystem, daemon and process tests belong at the operation boundary, with a
small genuine executable journey suite to check wiring. Do not duplicate every
command test at the root or introduce shared test helpers that import all command
families.

Extract complete command families and their tests together. Shared helpers should
have concrete consumers and one tested contract; avoid a generic command engine,
copied per-family exit policies, and broad interfaces over unrelated services.
Every extracted command package must have 100% statement coverage. Root tests
can still be invalidated by an importing family change, so they must remain
cheap; package boundaries alone do not guarantee faster CI.
