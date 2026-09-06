#!/bin/sh
# Emit the small receipt that main verifies before it reuses PR validation.
set -eu

receipt=${1:?usage: ci-reuse-receipt.sh RECEIPT_PATH}
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
: "${PULL_NUMBER:?PULL_NUMBER is required}"
: "${HEAD_SHA:?HEAD_SHA is required}"
: "${HEAD_REPOSITORY:?HEAD_REPOSITORY is required}"
: "${RUN_ID:?RUN_ID is required}"

tree=$(git rev-parse HEAD^{tree})
policy=$(for path in .github/workflows/go-ci.yml .github/scripts/release-eligible.sh; do sha256sum "$path" | awk '{print $1}'; done | sha256sum | awk '{print $1}')
jq -n \
  --arg repository "$GITHUB_REPOSITORY" \
  --arg pull_number "$PULL_NUMBER" \
  --arg head_sha "$HEAD_SHA" \
  --arg head_repository "$HEAD_REPOSITORY" \
  --arg run_id "$RUN_ID" \
  --arg tested_tree "$tree" \
  --arg policy_sha256 "$policy" \
  '{schema_version: 1, event: "pull_request", repository: $repository, workflow: ".github/workflows/go-ci.yml", base_ref: "main", pull_number: $pull_number, head_sha: $head_sha, head_repository: $head_repository, run_id: $run_id, tested_tree: $tested_tree, policy_sha256: $policy_sha256}' > "$receipt"
