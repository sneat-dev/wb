---
name: wb-dependency-campaign
description: Plan and execute dependency release waves with the fewest safe GitHub Actions builds. Use for breaking changes, multiple newly published modules, provider-first rollouts, or any WB deps bump campaign that must reuse PRs and converge downstream consumers once.
---

# WB dependency campaign

Optimize for one truthful build per ready consumer wave, not one campaign per
dependency.

1. Inventory all already-published root `module@version` events. If the
   provider package is approved but not yet published, use
   `wb deps publish npm` so the repository-owned workflow supplies the release
   and the registry receipt becomes the first event.
2. Search for existing campaign branches and PRs; resume them instead of
   opening duplicates.
3. If source compatibility must change before release, prepare paired PRs with
   `$wb-change` and its `**sneat-go-PR**:` contract.
4. Pass all root events to one `wb deps bump` invocation.
5. Preview once with `--dry-run`, then publish with `--merge`.
6. On interruption or red CI, fix the existing PR and rerun with `--resume`.
7. Read the final report and confirm every affected consumer converged.

Read [efficient-waves.md](references/efficient-waves.md) for the command shape
and [breaking-change.md](references/breaking-change.md) for merge ordering.

Do not launch independent campaigns for DALgo and each extension when they
feed the same downstream repository. A combined campaign lets WB accumulate
all ready dependency changes, update `sneat-go` once, and spend one downstream
CI build.
