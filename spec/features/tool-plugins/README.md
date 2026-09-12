---
format: https://specscore.md/feature-specification
status: Deprecated
---

# Feature: Tool Plugins

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/tool-plugins?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/tool-plugins?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/tool-plugins?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/tool-plugins?op=request-change) |

**Status:** Deprecated
**Source Ideas:** —

## Summary

This proposed built-in tool registry was retired before release. WB now uses
trusted generic repository-update hooks instead of embedding CodeGrapher
installation and lifecycle behavior.

## Problem

Embedding one tool made WB responsible for that tool's distribution and release
model while still not solving generic post-update automation. With no users to
support, retaining the commands would create two competing integration paths.

## Behavior

`wb plugin` and `wb codegrapher` MUST not be shipped. CodeGrapher remains an
independent CLI and MAY be invoked through the generic trusted lifecycle-hook
feature.

## Acceptance Criteria

No deprecated command appears in root help or the capability registry.

## Open Questions

None. Superseded by
[Trusted Repository Update Hooks](../trusted-repository-update-hooks/README.md).

---
*This document follows the https://specscore.md/feature-specification*
