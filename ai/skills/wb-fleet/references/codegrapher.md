# CodeGrapher plugin

CodeGrapher is WB's default preconfigured local-tool plugin. Discover its typed
lifecycle before acting:

```sh
wb plugin list --format=json
wb codegrapher status --format=json
```

The local tool is installed or updated only with an explicit authority:

```sh
wb codegrapher install --dry-run --format=json
wb codegrapher install --yes
wb codegrapher update --yes
```

macOS and Linux use the CodeGrapher Homebrew cask. Windows uses the published
Go module, where `--version` selects an exact CodeGrapher release. The command
re-probes the installed executable and reports its path, version, and available
build provenance. It never runs CodeGrapher indexing or graph sync; that needs
an explicit future exact-ref provider contract.
