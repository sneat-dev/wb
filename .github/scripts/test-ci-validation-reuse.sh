#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
temp=$(mktemp -d)
trap 'rm -rf "$temp"' EXIT

assert_output() {
  file=$1
  expected=$2
  if ! grep -Fx "$expected" "$file" >/dev/null; then
    printf 'missing %s in %s\n' "$expected" "$file" >&2
    exit 1
  fi
}

valid_receipt=$temp/valid.json
(
  cd "$root"
  GITHUB_REPOSITORY=sneat-dev/wb PULL_NUMBER=17 HEAD_SHA=head-sha HEAD_REPOSITORY=sneat-dev/wb RUN_ID=44 \
    sh .github/scripts/ci-reuse-receipt.sh "$valid_receipt"
)

valid_output=$temp/valid.output
(
  cd "$root"
  GITHUB_REPOSITORY=sneat-dev/wb CI_REUSE_RUN_ID=44 CI_REUSE_PULL_NUMBER=17 CI_REUSE_HEAD_SHA=head-sha \
    sh .github/scripts/ci-reuse-verify.sh "$valid_output" "$valid_receipt"
)
assert_output "$valid_output" 'reuse=true'
assert_output "$valid_output" 'reason=exact-trusted-receipt'

mismatch_receipt=$temp/mismatch.json
jq '.tested_tree = "wrong-tree"' "$valid_receipt" > "$mismatch_receipt"
mismatch_output=$temp/mismatch.output
(
  cd "$root"
  GITHUB_REPOSITORY=sneat-dev/wb CI_REUSE_RUN_ID=44 CI_REUSE_PULL_NUMBER=17 CI_REUSE_HEAD_SHA=head-sha \
    sh .github/scripts/ci-reuse-verify.sh "$mismatch_output" "$mismatch_receipt"
)
assert_output "$mismatch_output" 'reuse=false'
assert_output "$mismatch_output" 'reason=receipt-mismatch'

policy_mismatch_receipt=$temp/policy-mismatch.json
jq '.policy_sha256 = "wrong-policy"' "$valid_receipt" > "$policy_mismatch_receipt"
policy_mismatch_output=$temp/policy-mismatch.output
(
  cd "$root"
  GITHUB_REPOSITORY=sneat-dev/wb CI_REUSE_RUN_ID=44 CI_REUSE_PULL_NUMBER=17 CI_REUSE_HEAD_SHA=head-sha \
    sh .github/scripts/ci-reuse-verify.sh "$policy_mismatch_output" "$policy_mismatch_receipt"
)
assert_output "$policy_mismatch_output" 'reuse=false'
assert_output "$policy_mismatch_output" 'reason=receipt-mismatch'

direct_output=$temp/direct.output
GITHUB_EVENT_NAME=push GITHUB_REF=refs/heads/feature GITHUB_REPOSITORY=sneat-dev/wb GITHUB_SHA=landed \
  sh "$root/.github/scripts/ci-reuse-select.sh" "$direct_output"
assert_output "$direct_output" 'reuse=false'
assert_output "$direct_output" 'reason=not-main-push'

select_output=$temp/select.output
PATH="$root/.github/scripts/testdata/ci-validation-reuse:$PATH" \
  CI_REUSE_FIXTURE_DIR="$root/.github/scripts/testdata/ci-validation-reuse" \
  GITHUB_EVENT_NAME=push GITHUB_REF=refs/heads/main GITHUB_REPOSITORY=sneat-dev/wb GITHUB_SHA=landed-sha \
  sh "$root/.github/scripts/ci-reuse-select.sh" "$select_output"
assert_output "$select_output" 'candidate=true'
assert_output "$select_output" 'run_id=44'
assert_output "$select_output" 'pull_number=17'

policy_output=$temp/policy.output
PATH="$root/.github/scripts/testdata/ci-validation-reuse:$PATH" \
  CI_REUSE_FIXTURE_DIR="$root/.github/scripts/testdata/ci-validation-reuse" CI_REUSE_FILES=files-policy.json \
  GITHUB_EVENT_NAME=push GITHUB_REF=refs/heads/main GITHUB_REPOSITORY=sneat-dev/wb GITHUB_SHA=landed-sha \
  sh "$root/.github/scripts/ci-reuse-select.sh" "$policy_output"
assert_output "$policy_output" 'candidate=false'
assert_output "$policy_output" 'reason=github-policy-changed'

fork_output=$temp/fork.output
PATH="$root/.github/scripts/testdata/ci-validation-reuse:$PATH" \
  CI_REUSE_FIXTURE_DIR="$root/.github/scripts/testdata/ci-validation-reuse" CI_REUSE_PULL=pull-fork.json \
  GITHUB_EVENT_NAME=push GITHUB_REF=refs/heads/main GITHUB_REPOSITORY=sneat-dev/wb GITHUB_SHA=landed-sha \
  sh "$root/.github/scripts/ci-reuse-select.sh" "$fork_output"
assert_output "$fork_output" 'candidate=false'
assert_output "$fork_output" 'reason=untrusted-or-nonexact-pull'

printf 'ci validation reuse scripts: ok\n'
