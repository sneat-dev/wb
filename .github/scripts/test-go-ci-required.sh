#!/usr/bin/env bash
set -euo pipefail
script=$(cd "$(dirname "$0")" && pwd)/go-ci-required.sh

check() {
  local want=$1 event=$2 required=$3 contract=$4 contract_result=$5
  local source=$6 coverage=$7 reuse=$8
  shift 8
  local actual
  if env EVENT_NAME="$event" GO_REQUIRED="$required" CONTRACT_REQUIRED="$contract" \
    CONTRACT_RESULT="$contract_result" REUSE_RESULT="$reuse" \
    ELIGIBILITY_RESULT=success REUSE_JOB_RESULT=success GO_SCOPE_RESULT=success \
    SOURCE_RESULT="$source" STATIC_RESULT="${1:-$source}" LINT_RESULT="$source" \
    COVERAGE_RESULT="$coverage" RACE_RESULT="$source" E2E_RESULT="$source" \
    WINDOWS_RESULT=skipped bash "$script" >/dev/null 2>&1; then
    actual=pass
  else
    actual=fail
  fi
  if [[ $actual != "$want" ]]; then
    printf '%s: got %s, want %s\n' "$event/$required/$contract" "$actual" "$want" >&2
    exit 1
  fi
}

# Required checks passed remains green when scope intentionally skips Go jobs.
check pass pull_request false false skipped skipped skipped false
check pass push false false skipped skipped skipped false
check pass pull_request false true success skipped skipped false
check pass push false true success skipped skipped false
check pass pull_request true false skipped success success false
# Push reuse skips static jobs but coverage still publishes its baseline.
check pass push true false skipped skipped success true

# A skipped or failed job that should have run must fail branch protection.
check fail pull_request true false skipped success skipped false
check fail push true false skipped skipped skipped true
check fail pull_request false true skipped skipped skipped false
check fail pull_request false false skipped skipped skipped false failure

echo 'go-ci-required tests passed'
