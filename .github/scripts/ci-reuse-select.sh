#!/bin/sh
# Select only a GitHub-authenticated PR run that is safe to consider for reuse.
# Every refusal writes reuse=false so main executes the existing full checks.
set -eu

output=${1:?usage: ci-reuse-select.sh GITHUB_OUTPUT}

emit() {
  printf '%s=%s\n' "$1" "$2" >> "$output"
}

fallback() {
  emit candidate false
  emit reuse false
  emit reason "$1"
  exit 0
}

if [ "${GITHUB_EVENT_NAME:-}" != push ] || [ "${GITHUB_REF:-}" != refs/heads/main ]; then
  fallback not-main-push
fi
if [ -z "${GITHUB_REPOSITORY:-}" ] || [ -z "${GITHUB_SHA:-}" ]; then
  fallback missing-github-identity
fi

pulls=$(gh api "repos/$GITHUB_REPOSITORY/commits/$GITHUB_SHA/pulls") || fallback commit-pulls-unavailable
pull_number=$(printf '%s' "$pulls" | jq -er '[.[] | .number] | unique | if length == 1 then .[0] else error("expected exactly one associated pull request") end') || fallback ambiguous-associated-pull-request
pull=$(gh api "repos/$GITHUB_REPOSITORY/pulls/$pull_number") || fallback pull-unavailable

base_ref=$(printf '%s' "$pull" | jq -er '.base.ref') || fallback malformed-pull
head_ref=$(printf '%s' "$pull" | jq -er '.head.ref') || fallback malformed-pull
head_sha=$(printf '%s' "$pull" | jq -er '.head.sha') || fallback malformed-pull
head_repository=$(printf '%s' "$pull" | jq -er '.head.repo.full_name') || fallback malformed-pull
if ! printf '%s' "$pull" | jq -e --arg sha "$GITHUB_SHA" --arg repo "$GITHUB_REPOSITORY" '
  .merged == true and .merge_commit_sha == $sha and .base.ref == "main" and .head.repo.full_name == $repo
' >/dev/null; then
  fallback untrusted-or-nonexact-pull
fi

# The receipt producer and every policy helper live below .github. A pull
# request changing any of them must be validated again after merge.
files=$(gh api --paginate "repos/$GITHUB_REPOSITORY/pulls/$pull_number/files?per_page=100") || fallback pull-files-unavailable
if printf '%s' "$files" | jq -e '.[] | select(.filename | startswith(".github/"))' >/dev/null; then
  fallback github-policy-changed
fi

runs=$(gh api -X GET "repos/$GITHUB_REPOSITORY/actions/workflows/go-ci.yml/runs" \
  -f event=pull_request -f branch="$head_ref" -f per_page=100) || fallback workflow-runs-unavailable
run=$(printf '%s' "$runs" | jq -cer --arg head "$head_sha" --arg repo "$head_repository" '
  [.workflow_runs[] | select(.event == "pull_request" and .head_sha == $head and .head_repository.full_name == $repo)]
  | sort_by(.updated_at) | last // error("no matching pull-request run")
') || fallback no-matching-pull-request-run
run_id=$(printf '%s' "$run" | jq -er '.id') || fallback malformed-workflow-run
if ! printf '%s' "$run" | jq -e '.conclusion == "success"' >/dev/null; then
  fallback latest-pull-request-run-not-successful
fi

artifacts=$(gh api "repos/$GITHUB_REPOSITORY/actions/runs/$run_id/artifacts") || fallback receipt-artifacts-unavailable
if ! printf '%s' "$artifacts" | jq -e '
  [.artifacts[] | select(.name == "wb-ci-validation-receipt" and .expired == false and .size_in_bytes > 0 and .size_in_bytes <= 8192)] | length == 1
' >/dev/null; then
  fallback missing-or-invalid-receipt-artifact
fi

emit candidate true
emit run_id "$run_id"
emit pull_number "$pull_number"
emit head_sha "$head_sha"
emit reason selected
