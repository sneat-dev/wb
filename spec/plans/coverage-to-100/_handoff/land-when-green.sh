#!/bin/bash
# usage: WB_CLONE=<canonical sneat-dev/wb clone> land-when-green.sh <pr-number> <review-file>
# Waits (up to 60 min) for the PR's checks to finish, then lands it with wb pr land
# (merge commit), retrying once on a canonical-sync race. The review file is the
# Opus whole-batch review (decision 22). After landing, recreate cov/integration:
#   git push origin origin/main:refs/heads/cov/integration
set -uo pipefail
n=$1; R=$(cd "$(dirname "$2")" && pwd)/$(basename "$2")
cd "${WB_CLONE:?set WB_CLONE to the canonical clone}"
T=$(command -v timeout || command -v gtimeout || true)
for i in $(seq 1 60); do
  st=$(gh pr view "$n" -R sneat-dev/wb --json state,statusCheckRollup -q '.state + " " + ([.statusCheckRollup[]|select(.status!="COMPLETED")]|length|tostring)')
  case "$st" in MERGED*|"OPEN 0"|CLOSED*) break;; esac
  sleep 60
done
echo "#$n before land: $st"
for try in 1 2; do
  wb sync --filter sneat-dev/wb >/dev/null 2>&1
  out=$(${T:+$T 45m} wb pr land "sneat-dev/wb#$n" --approved-by "${APPROVED_BY:-claude-opus-5-5@claude-code}" --review-comment-file "$R" --allow-unfenced --timeout 40m --non-interactive 2>&1)
  echo "$out" | grep -E 'landed|MERGED|refusal|error:|retired' | head -4
  echo "$out" | grep -q canonical-sync-blocked || break
done
