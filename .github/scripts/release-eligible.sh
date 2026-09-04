#!/bin/sh
# Writes one GitHub Actions output deciding whether this coordinator may
# publish. The caller separately proves aggregate CI success, so this script
# only owns event/ref/path eligibility.
set -eu

event_name=${1:?event name is required}
ref=${2:?ref is required}
before=${3-}
sha=${4-}
eligible=false

case "$event_name:$ref" in
  push:refs/heads/main)
    [ -n "$before" ] && [ -n "$sha" ]
    if [ "$before" = 0000000000000000000000000000000000000000 ]; then
      changed=$(git ls-tree -r --name-only "$sha")
    else
      changed=$(git diff --name-only "$before" "$sha")
    fi
    while IFS= read -r path; do
      case "$path" in
        .goreleaser.yml|cmd/*|internal/*|go.mod|go.sum|.github/workflows/go-ci.yml|.github/scripts/release-eligible.sh)
          eligible=true
          ;;
      esac
    done <<EOF
$changed
EOF
    ;;
  push:refs/tags/v*)
    eligible=true
    ;;
  workflow_dispatch:refs/heads/main|workflow_dispatch:refs/tags/v*)
    eligible=true
    ;;
esac

printf 'eligible=%s\n' "$eligible"
