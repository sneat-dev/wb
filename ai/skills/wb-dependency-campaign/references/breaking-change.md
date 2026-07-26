# Breaking dependency changes

Separate source compatibility from released-version propagation.

## Before provider merge

1. Implement the provider change in a WB worktree.
2. Prepare consumer compatibility PRs in the same task.
3. Link the wired consumer from the provider PR body:

   ```md
   **sneat-go-PR**: https://github.com/sneat-co/sneat-go/pull/<number>
   ```

4. Require provider E2E to test both PR heads together.

This proves the new API and its wiring before the provider can merge, without
publishing local path replacements.

## Release and propagation

1. Merge the provider only after paired E2E and required checks pass.
2. Wait for the immutable provider release.
3. Add that release to the existing combined WB bump campaign.
4. Let WB merge provider layers and observe releases before building consumers.
5. Resume the prepared consumer PR or let WB update its expected campaign PR;
   do not open a duplicate.

When several provider or extension releases converge on `sneat-go`, collect
them into the same campaign so its manifest is changed and tested once.

Never point a mergeable consumer PR at an unpublished version or a local
filesystem replacement. Never merge a breaking provider merely because its
own unit tests are green; the linked consumer E2E is the compatibility gate.
