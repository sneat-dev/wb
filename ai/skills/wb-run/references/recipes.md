# Recipe configuration

The default config is `~/.config/wb/wb.yaml`; override it with `--config`.

Use `template-section` for a versioned Markdown block and `command` for a
repeatable tool invocation. Keep selection explicit with `applies_if`.

```yaml
recipes:
  refresh-guidance:
    type: template-section
    target: README.md
    template: /absolute/path/guidance.md
    marker: guidance
    applies_if: has_source:go
    commit_message: "docs: refresh guidance"
    pr_branch: "automation/refresh-guidance"
    pr_title: "docs: refresh guidance"
```

A template block uses matching versioned markers:

```md
<!-- guidance:v2 -->
Content
<!-- /guidance -->
```

For a command recipe, provide a read-only `dry_run_command` whenever possible:

```yaml
recipes:
  lint-fix:
    type: command
    command: "tool --fix"
    dry_run_command: "tool"
    applies_if: has_file:tool.yaml
```

Run `wb run <recipe>` before `--apply`. If a one-off change needs custom
reasoning or coordinated branches, use `$wb-change` instead of inventing an
opaque recipe.
