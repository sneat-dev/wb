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
  GITHUB_REPOSITORY=sneat-dev/wb PULL_NUMBER=17 HEAD_SHA=head-sha HEAD_REPOSITORY=sneat-dev/wb RUN_ID=44 BASE_REF=main \
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

# A successful run against a stacked branch is not main validation, even
# when every other identity, tree, and policy field matches the main verifier.
stack_receipt=$temp/stack.json
(
  cd "$root"
  GITHUB_REPOSITORY=sneat-dev/wb PULL_NUMBER=17 HEAD_SHA=head-sha HEAD_REPOSITORY=sneat-dev/wb RUN_ID=44 BASE_REF=integration/stack \
    sh .github/scripts/ci-reuse-receipt.sh "$stack_receipt"
)
stack_output=$temp/stack.output
(
  cd "$root"
  GITHUB_REPOSITORY=sneat-dev/wb CI_REUSE_RUN_ID=44 CI_REUSE_PULL_NUMBER=17 CI_REUSE_HEAD_SHA=head-sha \
    sh .github/scripts/ci-reuse-verify.sh "$stack_output" "$stack_receipt"
)
assert_output "$stack_output" 'reuse=false'
assert_output "$stack_output" 'reason=receipt-mismatch'
jq -e '.base_ref == "integration/stack"' "$stack_receipt" >/dev/null

# Missing and explicitly empty context fail independently, before creating
# or overwriting a receipt. Each case has its own output and diagnostic file.
for base_state in missing empty; do
  invalid_receipt=$temp/invalid-base-$base_state.json
  for output_state in absent existing; do
    if [ "$output_state" = existing ]; then
      printf '%s\n' 'preserve-existing-receipt' > "$invalid_receipt"
    fi
    if (
      cd "$root"
      export GITHUB_REPOSITORY=sneat-dev/wb PULL_NUMBER=17 HEAD_SHA=head-sha HEAD_REPOSITORY=sneat-dev/wb RUN_ID=44
      unset BASE_REF
      if [ "$base_state" = empty ]; then export BASE_REF=; fi
      sh .github/scripts/ci-reuse-receipt.sh "$invalid_receipt"
    ) > "$temp/$base_state-$output_state.log" 2>&1; then
      printf 'receipt generator accepted %s base\n' "$base_state" >&2
      exit 1
    fi
    grep -Fq 'BASE_REF is required' "$temp/$base_state-$output_state.log"
    if [ "$output_state" = absent ]; then
      test ! -e "$invalid_receipt"
    else
      assert_output "$invalid_receipt" 'preserve-existing-receipt'
    fi
  done
done

# The real receipt-producing step must supply the event base, rather than a
# constant or an unrelated job's BASE_REF setting.
if ! awk '
  /- name: Write exact validation receipt/ { in_step=1; next }
  in_step && /- name:/ { exit }
  in_step && $0 == "          BASE_REF: ${{ github.event.pull_request.base.ref }}" { found=1 }
  END { exit !found }
' "$root/.github/workflows/go-ci.yml"; then
  printf 'receipt producer must receive the actual pull-request base\n' >&2
  exit 1
fi

unavailable_output=$temp/unavailable.output
(
  cd "$root"
  GITHUB_REPOSITORY=sneat-dev/wb CI_REUSE_RUN_ID=44 CI_REUSE_PULL_NUMBER=17 CI_REUSE_HEAD_SHA=head-sha \
    sh .github/scripts/ci-reuse-verify.sh "$unavailable_output" "$temp/missing.json"
)
assert_output "$unavailable_output" 'reuse=false'
assert_output "$unavailable_output" 'reason=receipt-unavailable'
if ! awk '/name: Download selected validation receipt/{found=1} found && /continue-on-error: true/{ok=1; exit} END {exit !ok}' "$root/.github/workflows/go-ci.yml"; then
  printf 'download fallback must continue to verification\n' >&2
  exit 1
fi
if ! grep -Fq "if: \${{ !cancelled() && github.event_name == 'pull_request' && needs.test.result == 'success' && needs.go-scope.outputs.required == 'true' }}" "$root/.github/workflows/go-ci.yml"; then
  printf 'receipt publication must override skipped-dependency propagation\n' >&2
  exit 1
fi

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

deleted_branch_output=$temp/deleted-branch.output
PATH="$root/.github/scripts/testdata/ci-validation-reuse:$PATH" \
  CI_REUSE_FIXTURE_DIR="$root/.github/scripts/testdata/ci-validation-reuse" CI_REUSE_BRANCH_DELETED=1 \
  GITHUB_EVENT_NAME=push GITHUB_REF=refs/heads/main GITHUB_REPOSITORY=sneat-dev/wb GITHUB_SHA=landed-sha \
  sh "$root/.github/scripts/ci-reuse-select.sh" "$deleted_branch_output"
assert_output "$deleted_branch_output" 'candidate=true'
assert_output "$deleted_branch_output" 'run_id=44'

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
