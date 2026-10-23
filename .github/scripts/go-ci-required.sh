#!/usr/bin/env bash
# Keep the branch-protection aggregate aligned with the scope decision.
set -euo pipefail

: "${ELIGIBILITY_RESULT:?}"
: "${REUSE_JOB_RESULT:?}"
: "${GO_SCOPE_RESULT:?}"
: "${GO_REQUIRED:?}"
: "${CONTRACT_REQUIRED:?}"

if [[ $ELIGIBILITY_RESULT != success || $REUSE_JOB_RESULT != success || $GO_SCOPE_RESULT != success ]]; then
  echo "A required decision job failed or was skipped" >&2
  exit 1
fi
case "$GO_REQUIRED:$CONTRACT_REQUIRED" in
  true:false|false:true|false:false) ;;
  *) echo "Invalid Go/contract scope: $GO_REQUIRED/$CONTRACT_REQUIRED" >&2; exit 1 ;;
esac

expected_contract=skipped
if [[ $CONTRACT_REQUIRED == true ]]; then
  expected_contract=success
fi
if [[ $CONTRACT_RESULT != "$expected_contract" ]]; then
  echo "Non-Go contract inputs concluded $CONTRACT_RESULT; expected $expected_contract" >&2
  exit 1
fi

expected=success
if [[ $GO_REQUIRED == false || $REUSE_RESULT == true && $EVENT_NAME == push ]]; then
  expected=skipped
fi
expected_coverage=$expected

for check in "Formatting and dependencies:$SOURCE_RESULT:$expected" \
             "Build and vet:$STATIC_RESULT:$expected" \
             "Lint:$LINT_RESULT:$expected" \
             "Tests and coverage:$COVERAGE_RESULT:$expected_coverage" \
             "Race tests:$RACE_RESULT:$expected" \
             "E2E tier (real git):$E2E_RESULT:$expected"; do
  name=${check%%:*}
  rest=${check#*:}
  result=${rest%%:*}
  want=${rest#*:}
  if [[ $result != "$want" ]]; then
    echo "$name concluded $result; expected $want" >&2
    exit 1
  fi
done
case "$WINDOWS_RESULT" in
  success|skipped) ;;
  *) echo "Native Windows compile and test concluded $WINDOWS_RESULT" >&2; exit 1 ;;
esac

if [[ $GO_REQUIRED == false ]]; then
  echo 'No Go-relevant file changed; Go validation was not required.'
elif [[ $REUSE_RESULT == true ]]; then
  echo 'Reused exact trusted pull-request validation.'
else
  echo 'All required checks passed.'
fi
