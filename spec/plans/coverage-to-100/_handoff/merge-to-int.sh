#!/bin/bash
# usage: COV_INT_WT=<helper worktree> merge-to-int.sh <lane-branch> <review-file> [pr-number]
# Verifies the review covers the branch head, merges it into cov/integration with a
# merge commit (decision 21), pushes, and opens the standing PR if none is open.
# COV_INT_WT: a wb worktree of sneat-dev/wb on a local helper branch (for example
# cov-int-setup). wb hooks refuse commits on a detached HEAD, so the script resets
# that helper branch to origin/cov/integration and merges there.
set -euo pipefail
B=$1; R=$2; PR=${3:-}
W=${COV_INT_WT:?set COV_INT_WT to the helper worktree}
R=$(cd "$(dirname "$R")" && pwd)/$(basename "$R")
cd "$W"
helper=$(git branch --show-current)
[ -n "$helper" ] || { echo "REFUSE: $W is on a detached HEAD"; exit 1; }
git fetch -q origin
head=$(git rev-parse "origin/$B")
rev=$(head -1 "$R" | sed 's/^Reviewed-Head: //')
[ "$head" = "$rev" ] || { echo "REFUSE: review $rev != branch head $head"; exit 1; }
git reset -q --hard origin/cov/integration
git merge --no-ff -q -m "Merge $B into cov/integration (reviewed $head)

$(sed -n '2,6p' "$R" | cut -c1-200)" "origin/$B" || { git merge --abort; echo "CONFLICT merging $B"; exit 2; }
if [ -r /proc/loadavg ]; then while [ "$(cut -d. -f1 /proc/loadavg)" -ge 8 ]; do sleep 30; done; fi
git push -q origin HEAD:cov/integration
echo "merged $B -> cov/integration $(git rev-parse --short HEAD)"
open=$(gh pr list -R sneat-dev/wb --head cov/integration --base main --state open --json number -q '.[0].number')
if [ -z "$open" ]; then
  gh pr create -R sneat-dev/wb --base main --head cov/integration --title "coverage-to-100: integration batch (standing PR, decision 21)" --body "Standing PR for the wb coverage programme (spec/plans/coverage-to-100, decision 21): reviewed lane branches merged into cov/integration one per push; this PR's CI (ratchet included) runs on each push and is kept green; lands via wb pr land with a whole-batch adversarial review.

🤖 Generated with [Claude Code](https://claude.com/claude-code)" | tail -1
else echo "standing PR #$open"; fi
if [ -n "$PR" ]; then gh pr close "$PR" -R sneat-dev/wb --comment "Merged into \`cov/integration\` as $(git rev-parse --short HEAD) (plan decision 21); lands on main through the standing integration PR." >/dev/null && echo "closed #$PR"; fi
