#!/bin/sh
# Verify a downloaded receipt against the exact main checkout before reuse.
set -eu

output=${1:?usage: ci-reuse-verify.sh GITHUB_OUTPUT RECEIPT}
receipt=${2:?usage: ci-reuse-verify.sh GITHUB_OUTPUT RECEIPT}

emit() {
  printf '%s=%s\n' "$1" "$2" >> "$output"
}

fallback() {
  emit reuse false
  emit reason "$1"
  exit 0
}

if [ ! -s "$receipt" ]; then
  fallback receipt-unavailable
fi
if [ -z "${GITHUB_REPOSITORY:-}" ] || [ -z "${CI_REUSE_RUN_ID:-}" ] || [ -z "${CI_REUSE_PULL_NUMBER:-}" ] || [ -z "${CI_REUSE_HEAD_SHA:-}" ]; then
  fallback missing-selection-identity
fi

tree=$(git rev-parse HEAD^{tree}) || fallback tree-unavailable
policy=$(for path in .github/workflows/go-ci.yml .github/scripts/release-eligible.sh; do sha256sum "$path" | awk '{print $1}'; done | sha256sum | awk '{print $1}') || fallback policy-unavailable
if ! jq -e \
  --arg repository "$GITHUB_REPOSITORY" \
  --arg run_id "$CI_REUSE_RUN_ID" \
  --arg pull_number "$CI_REUSE_PULL_NUMBER" \
  --arg head_sha "$CI_REUSE_HEAD_SHA" \
  --arg tree "$tree" \
  --arg policy "$policy" '
  .schema_version == 1 and
  .event == "pull_request" and
  .repository == $repository and
  .workflow == ".github/workflows/go-ci.yml" and
  .base_ref == "main" and
  .head_repository == $repository and
  .run_id == $run_id and
  .pull_number == $pull_number and
  .head_sha == $head_sha and
  .tested_tree == $tree and
  .policy_sha256 == $policy
' "$receipt" >/dev/null; then
  fallback receipt-mismatch
fi

emit reuse true
emit reason exact-trusted-receipt
