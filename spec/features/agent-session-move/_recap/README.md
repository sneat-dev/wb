# Recap Reports — agent-session-move

Per-run recap reports produced by `specstudio:recap`. Each report is named `<sha>.md` where `<sha>` is the abbreviated git SHA of `HEAD` at run time. Each row records which verify report the recap was compared against (`Verify revision`) so reviewers can trace the drift gate end-to-end.

## Contents

| Report | Run revision | Verify revision | Drift summary |
|---|---|---|---|
| [4466faf.md](4466faf.md) | 4466faf | 6879606 | 7 no-drift, 1 spec-tighter, 0 code-tighter, 0 contradiction, 0 unmapped, 0 errored |

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/index-specification*
