#!/usr/bin/env bash
# Decide which validation a change needs. PRs compare their head to the merge
# base with the event's exact target SHA; pushes compare before to after.
set -euo pipefail

event=${1:?event name is required}
base=${2-}
head=${3-}
ref=${4-}
output=${5-}

required=true
contract_required=false

is_zero_sha() {
  [[ $1 =~ ^0+$ ]]
}

go_input() {
  # Assets (including Markdown embeds) and fixtures can change package
  # behavior without a Go source diff. Walk to the nearest package owner.
  local owner=${1%/*}
  if [[ $owner == "$1" ]]; then owner=.; fi
  while [[ $owner != / ]]; do
    if compgen -G "$owner/*.go" >/dev/null; then
      if [[ $1 != *.md ]]; then return 0; fi
      # Ordinary package prose stays cheap; Markdown embedded by Go is a
      # runtime input. Quoted patterns conservatively require validation.
      local source line pattern relative=${1#"$owner/"}
      for source in "$owner"/*.go; do
        while IFS= read -r line; do
          if [[ $line =~ ^[[:space:]]*//go:embed[[:space:]]+(.+) ]]; then
            if [[ ${BASH_REMATCH[1]} == *'"'* || ${BASH_REMATCH[1]} == *'`'* ]]; then return 0; fi
            for pattern in ${BASH_REMATCH[1]}; do
              pattern=${pattern#all:}
              if [[ $relative == $pattern || $relative == $pattern/* ]]; then return 0; fi
            done
          fi
        done < "$source"
      done
    fi
    if [[ $owner == . ]]; then break; fi
    if [[ $owner != */* ]]; then owner=.; else owner=${owner%/*}; fi
  done
  case "$1" in
    *.go|*.s|*.S|*.syso|*.c|*.h|*.cc|*.cpp|*.cxx|*.m|*.mm|*.f|*.F|*.for|*.f90|*.swig|*.swigcxx|\
    go.mod|go.sum|go.work|go.work.sum|*/go.mod|*/go.sum|*/go.work|*/go.work.sum|vendor/*|\
    .github/workflows/go-ci.yml|.github/workflows/race.yml|.github/workflows/nightly-coverage.yml|\
    .github/scripts/*|.wb/*|.golangci.*|.goreleaser.yml|.goreleaser.yaml|\
    Makefile|*/Makefile|Dockerfile|*/Dockerfile|buf.yaml|buf.gen.yaml|proto/*.proto|testdata/*|*/testdata/*|\
    ai/skills/*|hub/web/dist/*|hub/web/src/*|hub/web/public/*|hub/web/package.json|hub/web/pnpm-lock.yaml|\
    hub/web/pnpm-workspace.yaml|hub/web/astro.config.mjs|hub/web/tsconfig.json|\
    internal/secretscan/gitleaks/gitleaks.toml|examples/migrations/*.hcl|\
    examples/hooks-policy/*.yaml|examples/hooks-policy/templates/*)
      return 0 ;;
  esac
  return 1
}

# These repository contracts are read by focused tests but do not require the
# full build, race, E2E, or coverage suite.
contract_input() {
  case "$1" in
    spec/*|agents/*|ai/capabilities.json|ai/cli-capability-delivery.schema.json|ai/README.md|\
    .claude-plugin/*|.codex-plugin/*|.github/agents/*|.gitattributes|README.md|\
    docs/cli-flag-matrix.md|skills|internal/secretscan/gitleaks/LICENSE|internal/secretscan/gitleaks/PROVENANCE.md)
      return 0 ;;
  esac
  return 1
}

case "$event:$ref" in
  push:refs/tags/*|workflow_dispatch:*)
    # Tags and manually requested validation retain the full gate.
    ;;
  pull_request:*|push:refs/heads/main)
    if [[ -n $base && -n $head ]] && ! is_zero_sha "$base" &&
       git cat-file -e "$base^{commit}" 2>/dev/null &&
       git cat-file -e "$head^{commit}" 2>/dev/null; then
      changed=$(mktemp)
      trap 'rm -f "$changed"' EXIT
      diff_ok=true
      if [[ $event == pull_request ]]; then
        git diff --no-renames --name-only -z "$base...$head" > "$changed" || diff_ok=false
      else
        git diff --no-renames --name-only -z "$base" "$head" > "$changed" || diff_ok=false
      fi
      if [[ $diff_ok == true ]]; then
        required=false
        while IFS= read -r -d '' path; do
          if go_input "$path"; then
            required=true
          elif contract_input "$path"; then
            contract_required=true
          else
            # Unrecognized opaque inputs may be shared fixtures or build
            # configuration. Only known prose/documentation is cheap.
            case "$path" in
              *.md|docs/*|LICENSE*|NOTICE*|internal/*/domain_map.tsv) ;;
              *) required=true ;;
            esac
          fi
        done < "$changed"
      fi
    fi
    # An unavailable base or head fails closed to full validation.
    ;;
esac

if [[ $required == true ]]; then
  contract_required=false
fi
if [[ -n $output ]]; then
  printf 'required=%s\ncontract_required=%s\n' "$required" "$contract_required" >> "$output"
else
  printf 'required=%s\ncontract_required=%s\n' "$required" "$contract_required"
fi
